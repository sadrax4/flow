//go:build ai

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"
)

// The ChatGPT side is a small browser extension, written out next to the settings for "Load unpacked".
//
//go:embed extension
var extensionFiles embed.FS

const (
	aiPort   = 38457
	aiPrompt = "Answer the question shown in this screenshot directly and briefly, in the same language as the question."

	wmClipboardUpdate = 0x031D
	wmAIAnswer        = 0x8001 // WM_APP+1
	wmAIError         = 0x8002
	cfDIB             = 8

	processQueryLimitedInformation = 0x1000
	shotMaxAge                     = 5 * time.Minute // a snip may wait this long for a ChatGPT tab busy with the previous one
	aiLogMax                       = 512 << 10       // the log keeps its newer half once it grows past this
)

var (
	pAddClipboardFormatListener = user32.NewProc("AddClipboardFormatListener")
	pRegisterClipboardFormatW   = user32.NewProc("RegisterClipboardFormatW")
	pIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	pGetClipboardOwner          = user32.NewProc("GetClipboardOwner")
	pGetWindowThreadProcessId   = user32.NewProc("GetWindowThreadProcessId")
	pOpenProcess                = kernel32.NewProc("OpenProcess")
	pCloseHandle                = kernel32.NewProc("CloseHandle")
	pQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")

	aiToken    string
	aiTestMode string       // "1" only in test builds (-ldflags "-X main.aiTestMode=1"): TELEPROMPTER_ANY_IMAGE then works
	cfPNG      uintptr      // registered "PNG" clipboard format, offered by some screenshot tools
	aiLastSeen atomic.Int64 // unix nanos of the extension's last request
	aiExtWant  string       // extension version embedded in this exe
	aiExtSeen  atomic.Value // version the connected extension reports ("" before 1.1.0, "-" until it first asks); differs until Chrome reloads it
	aiNextID   atomic.Int64
	aiCurrent  atomic.Int64 // id of the newest screenshot; older answers are ignored

	aiShots   = make(chan aiShot, 1) // only the newest screenshot waits for the extension
	aiShotsMu sync.Mutex

	aiMu     sync.Mutex // guards aiAnswer and aiErr, written by HTTP handlers
	aiAnswer aiReply
	aiErr    string

	aiReloadAsked sync.Map // extension versions already asked to reload themselves in this run

	aiLogMu        sync.Mutex
	aiLogStarted   atomic.Int64 // newest snip whose arriving answer was logged
	aiRefusedLogAt atomic.Int64 // unix nanos of the last logged refusal, so a busy website can't flood the log

	// UI thread only.
	aiShownID    int64
	aiLastHash   [32]byte
	aiLastHashAt time.Time
)

type aiShot struct {
	ID     int64     `json:"id"`
	PNG    string    `json:"png"`
	Prompt string    `json:"prompt"`
	at     time.Time // when it was taken; stale ones are dropped
}

type aiReply struct {
	ID    int64  `json:"id"`
	Text  string `json:"text"`
	Done  bool   `json:"done"`
	Error string `json:"error"`
	Debug string `json:"debug"` // page structure sent with an error, written to the log
	// "" for ChatGPT's answer. With a Phrasly humanizer tab open the bar shows no text until Phrasly's version:
	// the extension sends "answering" and "humanizing" (no text), then "humanized" with Phrasly's text, or an
	// error with "humanize" and ChatGPT's text to show instead.
	Stage string `json:"stage"`
}

func aiStart() {
	aiToken = aiLoadToken()
	aiNextID.Store(time.Now().Unix() % 1000000 * 1000)
	aiExtSeen.Store("-")
	var manifest struct{ Version string }
	if b, err := extensionFiles.ReadFile("extension/manifest.json"); err == nil && json.Unmarshal(b, &manifest) == nil {
		aiExtWant = manifest.Version
	}
	cfPNG = call(pRegisterClipboardFormatW, uintptr(unsafe.Pointer(str("PNG"))))
	aiLog("TeleprompterAI started; it needs Chrome extension %s, loaded from %s", aiExtWant, aiShortPath(aiExtensionDir()))
	if err := aiWriteExtension(); err != nil {
		aiLog("could not write the extension folder: %v", err)
		showToast("Could not write the ChatGPT extension", colorError, 4*time.Second)
	}
	if call(pAddClipboardFormatListener, hMain) == 0 {
		aiLog("could not watch the clipboard, so snips will not be noticed")
	}
	go aiServe()
}

// Appends one line to chatgpt-log.txt (newest last). Safe from any goroutine.
func aiLog(format string, args ...any) {
	p := dataPath("chatgpt-log.txt")
	if p == "" {
		return
	}
	msg := strings.ReplaceAll(strings.TrimRight(fmt.Sprintf(format, args...), "\n"), "\n", "\r\n        ")
	line := time.Now().Format("2006-01-02 15:04:05.000") + "  " + msg + "\r\n"
	aiLogMu.Lock()
	defer aiLogMu.Unlock()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	f.WriteString(line)
	st, err := f.Stat()
	f.Close()
	if err != nil || st.Size() <= aiLogMax {
		return
	}
	if b, err := os.ReadFile(p); err == nil && len(b) > aiLogMax/2 {
		b = b[len(b)-aiLogMax/2:]
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
		os.WriteFile(p, b, 0o644)
	}
}

func aiExtLabel(v string) string {
	if v == "" {
		return "1.0 (too old to report its version)"
	}
	return v
}

// A path with the user's own folder written as %APPDATA%, so a shared log doesn't show the account name.
func aiShortPath(p string) string {
	if appData := os.Getenv("APPDATA"); appData != "" && strings.HasPrefix(strings.ToLower(p), strings.ToLower(appData)) {
		return "%APPDATA%" + p[len(appData):]
	}
	return p
}

func aiMessage(msg uint32, wp, lp uintptr) bool {
	switch msg {
	case wmClipboardUpdate:
		aiOnClipboard()
	case wmAIAnswer:
		aiMu.Lock()
		a := aiAnswer
		aiMu.Unlock()
		aiShowAnswer(a)
	case wmAIError:
		aiMu.Lock()
		e := aiErr
		aiMu.Unlock()
		showToast(e, colorError, 4*time.Second)
		render()
	default:
		return false
	}
	return true
}

// Random per-computer secret shared with the extension, so no website or other app can use the link.
func aiLoadToken() string {
	p := dataPath("ai-token")
	if b, err := os.ReadFile(p); err == nil {
		if t := strings.TrimSpace(string(b)); len(t) == 32 {
			return t
		}
	}
	var r [16]byte
	rand.Read(r[:])
	t := hex.EncodeToString(r[:])
	os.WriteFile(p, []byte(t), 0o600)
	return t
}

func aiExtensionDir() string {
	p := dataPath("ai-token")
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "chatgpt-extension")
}

func aiWriteExtension() error {
	dir := aiExtensionDir()
	if dir == "" {
		return errors.New("no settings folder")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files := map[string][]byte{}
	entries, err := fs.ReadDir(extensionFiles, "extension")
	if err != nil {
		return err
	}
	for _, e := range entries {
		b, err := extensionFiles.ReadFile("extension/" + e.Name())
		if err != nil {
			return err
		}
		files[e.Name()] = b
	}
	files["config.json"], _ = json.Marshal(map[string]any{"port": aiPort, "token": aiToken})
	for name, b := range files {
		p := filepath.Join(dir, name)
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, b) {
			continue
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func aiPostError(msg string) {
	aiMu.Lock()
	aiErr = msg
	aiMu.Unlock()
	call(pPostMessageW, hMain, wmAIError, 0, 0)
}

func aiServe() {
	mux := http.NewServeMux()
	mux.HandleFunc("/wait", aiWaitHandler)
	mux.HandleFunc("/answer", aiAnswerHandler)
	mux.HandleFunc("/log", aiLogHandler)
	mux.HandleFunc("/ping", aiPingHandler)
	addr := "127.0.0.1:" + strconv.Itoa(aiPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		aiLog("cannot listen on %s, so Chrome cannot connect: %v", addr, err)
		aiPostError("ChatGPT link failed: port " + strconv.Itoa(aiPort) + " is busy")
		return
	}
	aiLog("waiting for the Chrome extension on %s", addr)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.Serve(ln); err != nil {
		aiLog("connection to Chrome stopped: %v", err)
		aiPostError("ChatGPT link stopped")
	}
}

func aiAuthorized(w http.ResponseWriter, r *http.Request) bool {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-TP-Token")), []byte(aiToken)) != 1 {
		now := time.Now().UnixNano()
		if last := aiRefusedLogAt.Load(); now-last > int64(time.Minute) && aiRefusedLogAt.CompareAndSwap(last, now) {
			aiLog("refused a request without the right key (%s %s, origin %q); an extension loaded from another folder would cause this",
				r.Method, r.URL.Path, r.Header.Get("Origin"))
		}
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if prev := aiLastSeen.Swap(time.Now().UnixNano()); time.Since(time.Unix(0, prev)) > 45*time.Second {
		aiLog("Chrome extension connected")
	}
	return true
}

// Keeps only the newest screenshot; a shot handed back after a failed delivery never replaces a newer one.
func aiOffer(s aiShot) {
	aiShotsMu.Lock()
	defer aiShotsMu.Unlock()
	select {
	case old := <-aiShots:
		if old.ID > s.ID {
			s = old
		}
	default:
	}
	aiShots <- s
}

// Long poll: answers with the next screenshot, or 204 after 20s (under the extension worker's 30s fetch limit).
func aiWaitHandler(w http.ResponseWriter, r *http.Request) {
	if !aiAuthorized(w, r) {
		return
	}
	// The polling tab's extension is the one that will handle the next snip.
	ver := r.Header.Get("X-TP-Ext")
	if aiExtSeen.Swap(ver) != ver {
		aiLog("Chrome is running extension %s; this app needs %s", aiExtLabel(ver), aiExtWant)
	}
	// From 1.3.0 the extension reloads itself on a 409, picking up the copy aiStart wrote. Asked once per version,
	// so an extension loaded from some other folder can't loop; versions before 1.3.0 just retry.
	if ver != aiExtWant && ver != "" {
		if _, asked := aiReloadAsked.LoadOrStore(ver, true); !asked {
			aiLog("asking Chrome to load extension %s from disk in place of %s", aiExtWant, ver)
			w.WriteHeader(http.StatusConflict)
			return
		}
	}
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	defer func() { aiLastSeen.Store(time.Now().UnixNano()) }()
	for {
		select {
		case s := <-aiShots:
			if time.Since(s.at) > shotMaxAge {
				aiLog("snip #%d dropped: no ChatGPT tab took it within %s", s.ID, shotMaxAge)
				if s.ID == aiCurrent.Load() {
					aiPostError("ChatGPT didn't take the snip")
				}
				continue
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(s); err != nil {
				aiLog("snip #%d: handing it to Chrome failed (%v), keeping it for the next try", s.ID, err)
				aiOffer(s)
			} else {
				aiLog("snip #%d handed to Chrome", s.ID)
			}
			return
		case <-timeout.C:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			return
		}
	}
}

func aiAnswerHandler(w http.ResponseWriter, r *http.Request) {
	if !aiAuthorized(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var a aiReply
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&a); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	switch {
	case a.Error != "" && a.Stage == "humanize":
		aiLog("snip #%d: Phrasly could not humanize the answer: %s", a.ID, a.Error)
		if a.Debug != "" {
			aiLog("Phrasly page at that moment:\n%s", a.Debug)
		}
	case a.Error != "":
		aiLog("snip #%d failed in Chrome: %s", a.ID, a.Error)
		if a.Debug != "" {
			aiLog("ChatGPT page at that moment:\n%s", a.Debug)
		}
	case a.Stage == "answering":
		aiLog("snip #%d: ChatGPT is answering; the bar waits for Phrasly's version", a.ID)
	case a.Stage == "humanizing":
		aiLog("snip #%d: Phrasly is humanizing the answer", a.ID)
	case a.Stage == "humanized":
		aiLog("snip #%d humanized by Phrasly: %d characters", a.ID, utf8.RuneCountInString(a.Text))
	case a.Done:
		aiLog("snip #%d answered: %d characters", a.ID, utf8.RuneCountInString(a.Text))
	case aiLogStarted.Swap(a.ID) != a.ID:
		aiLog("snip #%d: answer arriving in the bar", a.ID)
	}
	if a.ID != aiCurrent.Load() {
		if a.Done || a.Error != "" {
			aiLog("snip #%d: answer not shown, a newer snip was taken", a.ID)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	aiMu.Lock()
	aiAnswer = a
	aiMu.Unlock()
	call(pPostMessageW, hMain, wmAIAnswer, 0, 0)
	w.WriteHeader(http.StatusNoContent)
}

// The extension's heartbeat while a tab is busy with a snip; aiAuthorized notes the contact.
func aiPingHandler(w http.ResponseWriter, r *http.Request) {
	if aiAuthorized(w, r) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// Progress lines from the extension, so the steps inside ChatGPT are in the same log.
func aiLogHandler(w http.ResponseWriter, r *http.Request) {
	if !aiAuthorized(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var m struct {
		Line string `json:"line"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&m); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	aiLog("  chrome: %s", m.Line)
	w.WriteHeader(http.StatusNoContent)
}

func aiOnClipboard() {
	isDIB := call(pIsClipboardFormatAvailable, cfDIB) != 0
	isPNG := !isDIB && cfPNG != 0 && call(pIsClipboardFormatAvailable, cfPNG) != 0
	if !(isDIB || isPNG) {
		return
	}
	owner, fromSnip := aiClipboardOwner()
	if !fromSnip {
		aiLog("image copied by %s: ignored, only Win+Shift+S snips are sent", owner)
		return
	}
	if locked {
		aiLog("snip from %s ignored: the password has not been entered yet", owner)
		showToast("Enter the password first", colorWarn, 2*time.Second)
		render()
		return
	}
	format, kind := uintptr(cfDIB), "bitmap"
	if isPNG {
		format, kind = cfPNG, "PNG"
	}
	data, ok := clipboardBytes(format)
	if !ok {
		aiLog("snip from %s: could not read it from the clipboard", owner)
		return
	}
	// Snipping Tool can publish the same snip more than once.
	hash := sha256.Sum256(data)
	if hash == aiLastHash && time.Since(aiLastHashAt) < 10*time.Second {
		return
	}
	// Without a connected ChatGPT tab the snip is dropped rather than queued, so it can't be sent unexpectedly later.
	if last := aiLastSeen.Load(); time.Since(time.Unix(0, last)) > 45*time.Second {
		if last == 0 {
			aiLog("snip dropped: no ChatGPT tab has connected since the app started (is chatgpt.com open in Chrome, with the extension on?)")
		} else {
			aiLog("snip dropped: the ChatGPT tab was last heard from %s ago", time.Since(time.Unix(0, last)).Round(time.Second))
		}
		showToast("Open chatgpt.com in Chrome", colorError, 4*time.Second)
		render()
		return
	}
	if seen, _ := aiExtSeen.Load().(string); seen != aiExtWant && seen != "-" {
		aiLog("snip dropped: Chrome is running extension %s but this app needs %s. Click reload on it in chrome://extensions, then refresh the ChatGPT tab", aiExtLabel(seen), aiExtWant)
		showToast("Reload extension in Chrome", colorError, 5*time.Second)
		render()
		return
	}
	aiLastHash, aiLastHashAt = hash, time.Now()
	id := aiNextID.Add(1)
	aiCurrent.Store(id)
	aiLog("snip #%d: %s image of %d KB from %s, asking ChatGPT", id, kind, len(data)>>10, owner)
	autoActive = false // the previous answer stops moving; the new one starts once it is final
	showToast("Asking ChatGPT...", colorAccent, 3*time.Second)
	render()
	taken := time.Now()
	go func() {
		img := data
		if isDIB {
			var err error
			if img, err = dibToPNG(data); err != nil {
				aiLog("snip #%d: image format not supported: %v", id, err)
				aiPostError("Screenshot format not supported")
				return
			}
		}
		if cfg, err := png.DecodeConfig(bytes.NewReader(img)); err == nil {
			aiLog("snip #%d: %dx%d PNG of %d KB waiting for the ChatGPT tab", id, cfg.Width, cfg.Height, len(img)>>10)
		} else {
			aiLog("snip #%d: the PNG could not be read back: %v", id, err)
		}
		aiOffer(aiShot{ID: id, PNG: base64.StdEncoding.EncodeToString(img), Prompt: aiPrompt, at: taken})
	}()
}

// Only images Snipping Tool (Win+Shift+S) put on the clipboard are sent. Images from any other app, or from one
// that can't be identified, are ignored, so nothing else copied ever reaches ChatGPT.
func aiClipboardOwner() (name string, fromSnip bool) {
	if aiTestMode == "1" && os.Getenv("TELEPROMPTER_ANY_IMAGE") == "1" {
		return "any app (test mode)", true
	}
	owner := call(pGetClipboardOwner)
	if owner == 0 {
		return "an unknown app", false
	}
	var pid uint32
	call(pGetWindowThreadProcessId, owner, uintptr(unsafe.Pointer(&pid)))
	h := call(pOpenProcess, processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return "process " + strconv.Itoa(int(pid)), false
	}
	defer call(pCloseHandle, h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if call(pQueryFullProcessImageNameW, h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))) == 0 {
		return "process " + strconv.Itoa(int(pid)), false
	}
	name = filepath.Base(syscall.UTF16ToString(buf[:n]))
	lower := strings.ToLower(name)
	return name, strings.Contains(lower, "snipping") || strings.Contains(lower, "screenclipping") || strings.Contains(lower, "screensketch")
}

func clipboardBytes(format uintptr) ([]byte, bool) {
	if !openClipboard() {
		return nil, false
	}
	defer call(pCloseClipboard)
	h := call(pGetClipboardData, format)
	if h == 0 {
		return nil, false
	}
	size := call(pGlobalSize, h)
	p := call(pGlobalLock, h)
	if p == 0 {
		return nil, false
	}
	defer call(pGlobalUnlock, h)
	if size < 40 { // smaller than any DIB header or PNG worth sending
		return nil, false
	}
	buf := make([]byte, size)
	call(pRtlMoveMemory, uintptr(unsafe.Pointer(&buf[0])), p, size)
	return buf, true
}

// Packed DIB (BITMAPINFOHEADER or V4/V5 header, 24 or 32 bits per pixel) to PNG.
func dibToPNG(b []byte) ([]byte, error) {
	u32 := func(o int) uint32 { return binary.LittleEndian.Uint32(b[o:]) }
	hdr := int(u32(0))
	if hdr < 40 || hdr > len(b) {
		return nil, errors.New("bad DIB header")
	}
	w, h := int(int32(u32(4))), int(int32(u32(8)))
	bpp, comp, colors := int(binary.LittleEndian.Uint16(b[14:])), u32(16), int(u32(32))
	topDown := h < 0
	if topDown {
		h = -h
	}
	if w <= 0 || h <= 0 || w > 16384 || h > 16384 || (bpp != 24 && bpp != 32) || (comp != 0 && comp != 3) || colors > 256 {
		return nil, errors.New("unsupported DIB")
	}
	off := hdr + 4*colors
	if comp == 3 && hdr == 40 {
		off += 12 // the three colour masks follow a plain BITMAPINFOHEADER
	}
	stride := (w*bpp + 31) / 32 * 4
	if off+stride*h > len(b) {
		return nil, errors.New("truncated DIB")
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	px := bpp / 8
	for y := range h {
		sy := h - 1 - y
		if topDown {
			sy = y
		}
		src := b[off+sy*stride:]
		dst := img.Pix[y*img.Stride:]
		for x := range w {
			s, d := src[x*px:], dst[x*4:]
			d[0], d[1], d[2], d[3] = s[2], s[1], s[0], 255
		}
	}
	var out bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func aiShowAnswer(a aiReply) {
	if locked || a.ID != aiCurrent.Load() {
		if (a.Done || a.Error != "") && locked {
			aiLog("snip #%d: answer not shown, the bar is locked", a.ID)
		}
		return
	}
	switch {
	case a.Error != "" && a.Stage == "humanize":
		// Phrasly couldn't humanize it, so the bar shows ChatGPT's answer after all.
		if strings.TrimSpace(a.Text) != "" {
			showText(a.Text, false)
			aiShownID = a.ID
			autoStart()
		}
		showToast("Phrasly: "+a.Error, colorWarn, 5*time.Second)
		render()
		return
	case a.Error != "":
		showToast("ChatGPT: "+a.Error, colorError, 5*time.Second)
		render()
		return
	case a.Stage == "answering":
		showToast("ChatGPT is answering...", colorAccent, 30*time.Second)
		render()
		return
	case a.Stage == "humanizing":
		showToast("Humanizing...", colorAccent, 60*time.Second)
		render()
		return
	case strings.TrimSpace(a.Text) == "":
		return
	case a.Stage == "humanized":
		showText(a.Text, false) // a new text, read from the start
		aiShownID = a.ID
		autoStart()
		showToast("Humanized", colorAccent, 2*time.Second)
		render()
		return
	}
	showText(a.Text, a.ID == aiShownID)
	aiShownID = a.ID
	if a.Done { // ChatGPT's final answer (no Phrasly humanizer open)
		autoStart()
	}
}
