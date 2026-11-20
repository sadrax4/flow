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
	"image"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
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
	shotMaxAge                     = 90 * time.Second
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
	cfPNG      uintptr      // registered "PNG" clipboard format, offered by some screenshot tools
	aiLastSeen atomic.Int64 // unix nanos of the extension's last request
	aiNextID   atomic.Int64
	aiCurrent  atomic.Int64 // id of the newest screenshot; older answers are ignored

	aiShots   = make(chan aiShot, 1) // only the newest screenshot waits for the extension
	aiShotsMu sync.Mutex

	aiMu     sync.Mutex // guards aiAnswer and aiErr, written by HTTP handlers
	aiAnswer aiReply
	aiErr    string

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
}

func aiStart() {
	aiToken = aiLoadToken()
	cfPNG = call(pRegisterClipboardFormatW, uintptr(unsafe.Pointer(str("PNG"))))
	if err := aiWriteExtension(); err != nil {
		showToast("Could not write the ChatGPT extension", colorError, 4*time.Second)
	}
	call(pAddClipboardFormatListener, hMain)
	go aiServe()
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
	srv := &http.Server{Addr: "127.0.0.1:" + strconv.Itoa(aiPort), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		aiPostError("ChatGPT link failed: port " + strconv.Itoa(aiPort) + " is busy")
	}
}

func aiAuthorized(w http.ResponseWriter, r *http.Request) bool {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-TP-Token")), []byte(aiToken)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	aiLastSeen.Store(time.Now().UnixNano())
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
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	defer aiLastSeen.Store(time.Now().UnixNano())
	for {
		select {
		case s := <-aiShots:
			if time.Since(s.at) > shotMaxAge {
				continue
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(s); err != nil {
				aiOffer(s)
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
	aiMu.Lock()
	aiAnswer = a
	aiMu.Unlock()
	call(pPostMessageW, hMain, wmAIAnswer, 0, 0)
	w.WriteHeader(http.StatusNoContent)
}

func aiOnClipboard() {
	isDIB := call(pIsClipboardFormatAvailable, cfDIB) != 0
	isPNG := !isDIB && cfPNG != 0 && call(pIsClipboardFormatAvailable, cfPNG) != 0
	if !(isDIB || isPNG) || !aiFromSnippingTool() {
		return
	}
	if locked {
		showToast("Enter the password first", colorWarn, 2*time.Second)
		render()
		return
	}
	format := uintptr(cfDIB)
	if isPNG {
		format = cfPNG
	}
	data, ok := clipboardBytes(format)
	if !ok {
		return
	}
	// Snipping Tool can publish the same snip more than once.
	if h := sha256.Sum256(data); h != aiLastHash || time.Since(aiLastHashAt) > 10*time.Second {
		aiLastHash, aiLastHashAt = h, time.Now()
	} else {
		return
	}
	// Without a connected ChatGPT tab the snip is dropped rather than queued, so it can't be sent unexpectedly later.
	if time.Since(time.Unix(0, aiLastSeen.Load())) > 45*time.Second {
		showToast("Open chatgpt.com in Chrome", colorError, 4*time.Second)
		render()
		return
	}
	id := aiNextID.Add(1)
	aiCurrent.Store(id)
	showToast("Asking ChatGPT...", colorAccent, 3*time.Second)
	render()
	taken := time.Now()
	go func() {
		img := data
		if isDIB {
			var err error
			if img, err = dibToPNG(data); err != nil {
				aiPostError("Screenshot format not supported")
				return
			}
		}
		aiOffer(aiShot{ID: id, PNG: base64.StdEncoding.EncodeToString(img), Prompt: aiPrompt, at: taken})
	}()
}

// Only Win+Shift+S snips are sent, not every image copied elsewhere. If the owner can't be identified, the image is accepted.
func aiFromSnippingTool() bool {
	if os.Getenv("TELEPROMPTER_ANY_IMAGE") == "1" {
		return true
	}
	owner := call(pGetClipboardOwner)
	if owner == 0 {
		return true
	}
	var pid uint32
	call(pGetWindowThreadProcessId, owner, uintptr(unsafe.Pointer(&pid)))
	h := call(pOpenProcess, processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return true
	}
	defer call(pCloseHandle, h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if call(pQueryFullProcessImageNameW, h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))) == 0 {
		return true
	}
	name := strings.ToLower(filepath.Base(syscall.UTF16ToString(buf[:n])))
	return strings.Contains(name, "snipping") || strings.Contains(name, "screenclipping") || strings.Contains(name, "screensketch")
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
		return
	}
	if a.Error != "" {
		showToast("ChatGPT: "+a.Error, colorError, 5*time.Second)
		render()
		return
	}
	if strings.TrimSpace(a.Text) == "" {
		return
	}
	showText(a.Text, a.ID == aiShownID)
	aiShownID = a.ID
}
