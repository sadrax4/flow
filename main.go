package main

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"
)

// Win32 windows are bound to their creating thread; without this the Go scheduler can migrate main and freeze the message loop.
func init() { runtime.LockOSThread() }

const (
	wsPopup     = 0x80000000
	wsExLayered = 0x00080000
	wsExTopmost = 0x00000008

	wmDestroy        = 0x0002
	wmKillFocus      = 0x0008
	wmPaint          = 0x000F
	wmClose          = 0x0010
	wmEraseBkgnd     = 0x0014
	wmEndSession     = 0x0016
	wmKeyDown        = 0x0100
	wmKeyUp          = 0x0101
	wmChar           = 0x0102
	wmTimer          = 0x0113
	wmMouseMove      = 0x0200
	wmLButtonDown    = 0x0201
	wmLButtonUp      = 0x0202
	wmRButtonDown    = 0x0204
	wmRButtonUp      = 0x0205
	wmMouseWheel     = 0x020A
	wmCaptureChanged = 0x0215

	vkBack     = 0x08
	vkShift    = 0x10
	vkControl  = 0x11
	vkMenu     = 0x12
	vkEscape   = 0x1B
	vkPrior    = 0x21
	vkNext     = 0x22
	vkEnd      = 0x23
	vkHome     = 0x24
	vkLeft     = 0x25
	vkUp       = 0x26
	vkRight    = 0x27
	vkDown     = 0x28
	vkInsert   = 0x2D
	vkDelete   = 0x2E
	vkAdd      = 0x6B
	vkSubtract = 0x6D
	vkOemPlus  = 0xBB
	vkOemMinus = 0xBD
	mkControl  = 0x0008

	ssaFallback = 0x0020
	ssaGlyphs   = 0x0080
	ssaRTL      = 0x0100
	ssaLink     = 0x1000

	cfUnicodeText = 13
	gmemMoveable  = 0x0002
	srcCopy       = 0x00CC0020
	bkTransparent = 1
	dtCenter      = 0x0001
	dtVCenter     = 0x0004
	dtSingleLine  = 0x0020
	dtCalcRect    = 0x0400
	dtNoPrefix    = 0x0800

	errAlreadyExists = 183

	swpNoSize     = 0x0001
	swpNoMove     = 0x0002
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010

	lwaAlpha              = 0x2
	wdaExcludeFromCapture = 0x11
	animTimer             = 1
	saveTimer             = 2
	toastTimer            = 3
	opacity               = 217

	omegaSlow     = 7.0  // taps, held arrows, wheel: a calm ~0.8s glide at the default speed
	omegaFast     = 22.0 // sentence and page jumps
	tapTime       = 180 * time.Millisecond
	minSpeed      = 1 // held-arrow glide speed, in font-heights per second
	maxSpeed      = 12
	defaultSpeed  = 5
	configVersion = 3
	separator     = "   •   "
	hintText      = "Ctrl+V  =  paste your text"
	maxPassword   = 64
	maxTextBytes  = 1 << 20
	largeText     = 100_000 // bytes; above this "Loading..." is shown while shaping

	colorText   = 0xFFFFFF // COLORREF is 0x00BBGGRR
	colorDim    = 0xA0A0A0
	colorError  = 0x5A5AFF
	colorWarn   = 0x4FC8FF
	colorAccent = 0xE0C060
)

// PBKDF2-SHA256 (200k rounds) of the unlock password; the password itself is not stored anywhere.
const (
	passwordSalt   = "c51f62f616066ed66722dd9d9ea99e58"
	passwordKey    = "9261c9813b79d7b7a7647f4321be3ff6a8f7922f8273fb7c873044c12e5d13ac"
	passwordRounds = 200000
)

const helpText = "Ctrl+V = paste your text\n" +
	"Right / Left arrow = move the text (hold to glide)\n" +
	"Up / Down arrow = jump to previous / next sentence\n" +
	"+ / - = speed\n" +
	"Ctrl + Up / Down = text and bar height\n" +
	"Ctrl + Left / Right = bar width\n" +
	"Drag = move the bar\n" +
	"Esc = close"

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type msgT struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type paintStruct struct {
	hdc         uintptr
	fErase      int32
	rcPaint     rect
	fRestore    int32
	fIncUpdate  int32
	rgbReserved [32]byte
}

type bitmapInfo struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
	colors          [1]uint32
}

// One shaped run of the script; the whole script is laid out as a single line of these.
type chunk struct {
	ssa           uintptr
	buf           []uint16
	start, width  float64
	sentenceStart bool
}

type piece struct {
	text          string
	sentenceStart bool
}

type config struct {
	V                int
	X, Y, W, H, Font int32
	Pos              float64
	Speed            int
}

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	usp10    = syscall.NewLazyDLL("usp10.dll")
	dwmapi   = syscall.NewLazyDLL("dwmapi.dll")

	pRegisterClassExW           = user32.NewProc("RegisterClassExW")
	pCreateWindowExW            = user32.NewProc("CreateWindowExW")
	pDefWindowProcW             = user32.NewProc("DefWindowProcW")
	pShowWindow                 = user32.NewProc("ShowWindow")
	pSetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	pGetMessageW                = user32.NewProc("GetMessageW")
	pTranslateMessage           = user32.NewProc("TranslateMessage")
	pDispatchMessageW           = user32.NewProc("DispatchMessageW")
	pPostQuitMessage            = user32.NewProc("PostQuitMessage")
	pPostMessageW               = user32.NewProc("PostMessageW")
	pDestroyWindow              = user32.NewProc("DestroyWindow")
	pSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	pSetWindowDisplayAffinity   = user32.NewProc("SetWindowDisplayAffinity")
	pSetProcessDPIAware         = user32.NewProc("SetProcessDPIAware")
	pGetSystemMetrics           = user32.NewProc("GetSystemMetrics")
	pSetWindowPos               = user32.NewProc("SetWindowPos")
	pGetWindowRect              = user32.NewProc("GetWindowRect")
	pSetWindowRgn               = user32.NewProc("SetWindowRgn")
	pSetCapture                 = user32.NewProc("SetCapture")
	pReleaseCapture             = user32.NewProc("ReleaseCapture")
	pGetCursorPos               = user32.NewProc("GetCursorPos")
	pGetKeyState                = user32.NewProc("GetKeyState")
	pLoadCursorW                = user32.NewProc("LoadCursorW")
	pLoadImageW                 = user32.NewProc("LoadImageW")
	pSetFocus                   = user32.NewProc("SetFocus")
	pSetTimer                   = user32.NewProc("SetTimer")
	pKillTimer                  = user32.NewProc("KillTimer")
	pGetDC                      = user32.NewProc("GetDC")
	pReleaseDC                  = user32.NewProc("ReleaseDC")
	pBeginPaint                 = user32.NewProc("BeginPaint")
	pEndPaint                   = user32.NewProc("EndPaint")
	pFillRect                   = user32.NewProc("FillRect")
	pDrawTextW                  = user32.NewProc("DrawTextW")
	pOpenClipboard              = user32.NewProc("OpenClipboard")
	pCloseClipboard             = user32.NewProc("CloseClipboard")
	pEmptyClipboard             = user32.NewProc("EmptyClipboard")
	pGetClipboardData           = user32.NewProc("GetClipboardData")
	pSetClipboardData           = user32.NewProc("SetClipboardData")
	pFindWindowW                = user32.NewProc("FindWindowW")

	pCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	pCreateRoundRectRgn     = gdi32.NewProc("CreateRoundRectRgn")
	pCreateFontW            = gdi32.NewProc("CreateFontW")
	pCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	pCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	pSelectObject           = gdi32.NewProc("SelectObject")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pBitBlt                 = gdi32.NewProc("BitBlt")
	pGdiAlphaBlend          = gdi32.NewProc("GdiAlphaBlend")
	pSetTextColor           = gdi32.NewProc("SetTextColor")
	pSetBkMode              = gdi32.NewProc("SetBkMode")
	pGetTextMetricsW        = gdi32.NewProc("GetTextMetricsW")
	pGetDeviceCaps          = gdi32.NewProc("GetDeviceCaps")
	pGdiFlush               = gdi32.NewProc("GdiFlush")

	pGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	pGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	pGlobalFree       = kernel32.NewProc("GlobalFree")
	pGlobalLock       = kernel32.NewProc("GlobalLock")
	pGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	pGlobalSize       = kernel32.NewProc("GlobalSize")
	pRtlMoveMemory    = kernel32.NewProc("RtlMoveMemory")
	pCreateMutexW     = kernel32.NewProc("CreateMutexW")

	pDwmFlush = dwmapi.NewProc("DwmFlush")

	pScriptStringAnalyse = usp10.NewProc("ScriptStringAnalyse")
	pScriptStringOut     = usp10.NewProc("ScriptStringOut")
	pScriptStringFree    = usp10.NewProc("ScriptStringFree")
	pScriptStringPSize   = usp10.NewProc("ScriptString_pSize")
)

var (
	hMain, memDC, memBmp, curFont uintptr
	bgBrush, selBrush, toastBrush uintptr
	fadeDC                        [2]uintptr
	bufW, bufH, fadeW, lineH      int32
	scale                               = 1.0
	fontSize                      int32 = 24

	text        string
	chunks      []chunk
	total       float64
	rtl         bool
	allSelected bool

	animating        bool
	pos, target, vel float64
	omega            = omegaSlow
	hold             int
	holdStart        time.Time
	holdOrigin       float64
	holdVK           uintptr
	lastTick         time.Time

	dragging            bool
	dragCursor, dragWin point

	speed = defaultSpeed

	locked   bool
	loading  bool
	password []rune

	toast      string
	toastColor uintptr
	toastUntil time.Time

	rtlState uint16 = 1 // package-level so its address stays fixed while Uniscribe reads it
)

//go:uintptrescapes
func call(p *syscall.LazyProc, a ...uintptr) uintptr {
	r, _, _ := p.Call(a...)
	return r
}

func str(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(strings.ReplaceAll(s, "\x00", ""))
	return p
}

func sm(i uintptr) int32 { return int32(call(pGetSystemMetrics, i)) }

func px(v int32) int32 { return int32(float64(v)*scale + 0.5) }

func keyDown(vk uintptr) bool { return call(pGetKeyState, vk)&0x8000 != 0 }

func clamp(v, lo, hi int32) int32 { return max(lo, min(v, hi)) }

func windowRect() rect {
	var r rect
	call(pGetWindowRect, hMain, uintptr(unsafe.Pointer(&r)))
	return r
}

func normalize(s string) string {
	s = strings.NewReplacer("\x00", "", "\uFEFF", "").Replace(s)
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

func isRTL(s string) bool {
	rtlCount, ltrCount := 0, 0
	for i, r := range s {
		if i > 4000 {
			break
		}
		switch {
		case r >= 0x0590 && r <= 0x08FF, r >= 0xFB1D && r <= 0xFDFF, r >= 0xFE70 && r <= 0xFEFF:
			rtlCount++
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			ltrCount++
		}
	}
	return rtlCount > ltrCount
}

// Splits after sentence-ending punctuation that is followed by a space.
func sentences(p string) []string {
	var out []string
	start := 0
	prevEnd := false
	for i, r := range p {
		if prevEnd && r == ' ' {
			out = append(out, p[start:i+1])
			start = i + 1
		}
		prevEnd = strings.ContainsRune(".!?؟…", r)
	}
	if start < len(p) {
		out = append(out, p[start:])
	}
	return out
}

// Paragraphs joined by a separator, one run per sentence (long ones cut at spaces) so runs shape quickly
// and sentence starts are known for the Up/Down jumps.
func pieces(s string) []piece {
	var paras []string
	var out []piece
	for _, p := range strings.Split(s, "\n") {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			paras = append(paras, p)
		}
	}
	for i, para := range paras {
		sents := sentences(para)
		if i < len(paras)-1 {
			sents[len(sents)-1] += separator
		}
		for _, p := range sents {
			first := true
			for p != "" {
				cut := len(p)
				if cut > 600 {
					cut = strings.LastIndex(p[:600], " ") + 1
					if cut <= 0 {
						cut = 600
						for cut > 0 && !utf8.RuneStart(p[cut]) {
							cut--
						}
					}
				}
				out = append(out, piece{p[:cut], first})
				p, first = p[cut:], false
			}
		}
	}
	return out
}

func freeChunks() {
	for i := range chunks {
		call(pScriptStringFree, uintptr(unsafe.Pointer(&chunks[i].ssa)))
	}
	chunks, total = nil, 0
}

func buildChunks() {
	freeChunks()
	rtl = isRTL(text)
	flags := uintptr(ssaGlyphs | ssaFallback | ssaLink)
	// SCRIPT_STATE with uBidiLevel=1 as well as SSA_RTL: some Uniscribe builds only honor one of them.
	statePtr := uintptr(0)
	if rtl {
		flags |= ssaRTL
		statePtr = uintptr(unsafe.Pointer(&rtlState))
	}
	for _, pc := range pieces(text) {
		buf, err := syscall.UTF16FromString(pc.text)
		n := len(buf) - 1
		if err != nil || n <= 0 {
			continue
		}
		var ssa uintptr
		hr := call(pScriptStringAnalyse, memDC, uintptr(unsafe.Pointer(&buf[0])), uintptr(n), uintptr(n*3/2+16),
			^uintptr(0), flags, 0, 0, statePtr, 0, 0, 0, uintptr(unsafe.Pointer(&ssa)))
		if int32(hr) != 0 || ssa == 0 {
			continue
		}
		var size point
		call(pRtlMoveMemory, uintptr(unsafe.Pointer(&size)), call(pScriptStringPSize, ssa), 8)
		chunks = append(chunks, chunk{ssa: ssa, buf: buf, start: total, width: float64(size.X), sentenceStart: pc.sentenceStart})
		total += float64(size.X)
	}
}

func inset() int32 { return fadeW + px(8) }

func maxPos() float64 { return math.Max(0, total-float64(bufW-2*inset())) }

func clampPos(p float64) float64 { return math.Max(0, math.Min(p, maxPos())) }

func step() float64 { return float64(bufW-2*inset()) / 4 }

// pos grows in reading order; RTL text starts at the right edge and advances by moving right.
func chunkX(c *chunk) int32 {
	if rtl {
		return int32(math.Round(float64(bufW-inset()) + pos - c.start - c.width))
	}
	return int32(math.Round(float64(inset()) - pos + c.start))
}

func applyFont(size int32) {
	keep := 0.0
	if total > 0 {
		keep = pos / total
	}
	freeChunks() // before the old font is deleted: the shaped runs reference it
	setFont(size)
	buildChunks()
	pos = clampPos(keep * total)
	target = pos
}

func setFont(size int32) {
	fontSize = clamp(size, 12, 200)
	f := call(pCreateFontW, uintptr(-px(fontSize)), 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(str("Segoe UI"))))
	call(pSelectObject, memDC, f)
	if curFont != 0 {
		call(pDeleteObject, curFont)
	}
	curFont = f
	var tm [16]int32
	call(pGetTextMetricsW, memDC, uintptr(unsafe.Pointer(&tm[0])))
	lineH = tm[0]
	// The bar hugs one line of text so it sits flush under the top edge of the screen.
	r := windowRect()
	call(pSetWindowPos, hMain, 0, 0, 0, uintptr(r.Right-r.Left), uintptr(lineH+bottomMargin()), swpNoMove|swpNoZOrder|swpNoActivate)
	ensureBuffer()
}

// Keeps shaping time to a couple of seconds (about 1M English or 500k Persian characters).
func limitText(s string) (string, bool) {
	if len(s) <= maxTextBytes {
		return s, false
	}
	cut := maxTextBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// Text sits at the very top of the bar; the font's own internal leading keeps a small gap above the glyphs.
func bottomMargin() int32 { return px(2) }

func makeFade(opaqueLeft bool) uintptr {
	bi := bitmapInfo{biSize: 40, biWidth: fadeW, biHeight: 1, biPlanes: 1, biBitCount: 32}
	var bits uintptr
	dc := call(pCreateCompatibleDC, 0)
	bmp := call(pCreateDIBSection, dc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	pix := make([]byte, fadeW*4)
	for i := range fadeW {
		t := float64(i) / float64(fadeW-1)
		if opaqueLeft {
			t = 1 - t
		}
		pix[i*4+3] = byte(255 * t * t * (3 - 2*t))
	}
	if bits != 0 {
		call(pRtlMoveMemory, bits, uintptr(unsafe.Pointer(&pix[0])), uintptr(len(pix)))
	}
	call(pSelectObject, dc, bmp)
	return dc
}

func ensureBuffer() {
	r := windowRect()
	w, h := r.Right-r.Left, r.Bottom-r.Top
	if memBmp != 0 && w == bufW && h == bufH {
		return
	}
	screen := call(pGetDC, 0)
	bmp := call(pCreateCompatibleBitmap, screen, uintptr(w), uintptr(h))
	call(pReleaseDC, 0, screen)
	call(pSelectObject, memDC, bmp)
	if memBmp != 0 {
		call(pDeleteObject, memBmp)
	}
	memBmp, bufW, bufH = bmp, w, h
	// Region starts above the window so only the bottom corners are rounded, like a notch under the camera.
	rad := px(24)
	call(pSetWindowRgn, hMain, call(pCreateRoundRectRgn, 0, uintptr(-rad), uintptr(w+1), uintptr(h+1), uintptr(rad), uintptr(rad)), 1)
}

func drawLabel(s string, r rect, color uintptr) {
	call(pSetTextColor, memDC, color)
	call(pDrawTextW, memDC, uintptr(unsafe.Pointer(str(s))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), dtCenter|dtVCenter|dtSingleLine|dtNoPrefix)
	call(pSetTextColor, memDC, colorText)
}

func toastActive() bool { return toast != "" && time.Now().Before(toastUntil) }

func showToast(msg string, color uintptr, d time.Duration) {
	toast, toastColor, toastUntil = msg, color, time.Now().Add(d)
	call(pSetTimer, hMain, toastTimer, uintptr(d.Milliseconds()+50), 0)
}

func drawFades() {
	blend := uintptr(255<<16 | 1<<24) // SourceConstantAlpha 255, AC_SRC_ALPHA
	call(pGdiAlphaBlend, memDC, 0, 0, uintptr(fadeW), uintptr(bufH), fadeDC[0], 0, 0, uintptr(fadeW), 1, blend)
	call(pGdiAlphaBlend, memDC, uintptr(bufW-fadeW), 0, uintptr(fadeW), uintptr(bufH), fadeDC[1], 0, 0, uintptr(fadeW), 1, blend)
}

func draw() {
	full := rect{0, 0, bufW, bufH}
	call(pFillRect, memDC, uintptr(unsafe.Pointer(&full)), bgBrush)
	if loading && !locked {
		drawLabel("Loading...", full, colorDim)
		return
	}
	if locked {
		msg, color := "Password:  "+strings.Repeat("•", len(password)), uintptr(colorText)
		if len(password) == 0 {
			msg, color = "Enter password", colorDim
		}
		if toastActive() {
			msg, color = toast, toastColor
		}
		drawLabel(msg, full, color)
		return
	}
	if len(chunks) == 0 {
		drawLabel(hintText, full, colorDim)
	}
	y := int32(0)
	for i := range chunks {
		c := &chunks[i]
		x := chunkX(c)
		if x > bufW || x+int32(c.width) < 0 {
			continue
		}
		if allSelected {
			band := rect{x, y, x + int32(c.width), y + lineH}
			call(pFillRect, memDC, uintptr(unsafe.Pointer(&band)), selBrush)
		}
		call(pScriptStringOut, c.ssa, uintptr(x), uintptr(y), 0, 0, 0, 0, 0)
	}
	drawFades()
	if toastActive() {
		var size rect
		call(pDrawTextW, memDC, uintptr(unsafe.Pointer(str(toast))), ^uintptr(0), uintptr(unsafe.Pointer(&size)), dtCalcRect|dtSingleLine|dtNoPrefix)
		pill := rect{bufW - px(10) - (size.Right - size.Left) - px(20), 0, bufW - px(10), bufH}
		call(pFillRect, memDC, uintptr(unsafe.Pointer(&pill)), toastBrush)
		drawLabel(toast, pill, toastColor)
	}
}

func present(hdc uintptr) {
	ensureBuffer()
	draw()
	call(pBitBlt, hdc, 0, 0, uintptr(bufW), uintptr(bufH), memDC, 0, 0, srcCopy)
	call(pGdiFlush) // submit now, so "Loading..." shows even while the thread is busy shaping
}

func render() {
	if hMain == 0 {
		return
	}
	hdc := call(pGetDC, hMain)
	present(hdc)
	call(pReleaseDC, hMain, hdc)
}

func startAnim() {
	if animating {
		return
	}
	animating = true
	target, vel = pos, 0
	lastTick = time.Now()
	call(pSetTimer, hMain, animTimer, 10, 0)
}

func stopAnim() {
	if animating {
		call(pKillTimer, hMain, animTimer)
	}
	animating, hold, vel = false, 0, 0
}

func glideTo(p, w float64) {
	startAnim()
	omega = w
	target = clampPos(p)
}

func glideBy(d, w float64) { glideTo(target+d, w) }

// Spring stiffness for taps and the wheel follows the speed setting, so faster speeds also glide quicker.
func tapOmega() float64 { return omegaSlow * math.Pow(float64(speed)/defaultSpeed, 0.4) }

func changeSpeed(d int) {
	speed = max(minSpeed, min(maxSpeed, speed+d))
	showToast("Speed "+strconv.Itoa(speed), colorAccent, 1500*time.Millisecond)
	call(pSetTimer, hMain, saveTimer, 500, 0)
}

// The glide starts the moment the arrow goes down; auto-repeat key-downs are ignored.
func move(dir int, vk, lp uintptr) {
	if lp&(1<<30) != 0 {
		return
	}
	startAnim()
	omega = tapOmega()
	hold, holdVK = dir, vk
	holdStart, holdOrigin = time.Now(), target
}

// A short press counts as a tap and completes a quarter-bar step; a long press just stops gliding.
func releaseHold() {
	if hold == 0 {
		return
	}
	if time.Since(holdStart) < tapTime {
		if t := holdOrigin + float64(hold)*step(); (t-target)*float64(hold) > 0 {
			target = clampPos(t)
		}
	}
	hold = 0
}

// Up goes to the start of the current sentence (or the previous one if already there); Down to the next.
func jumpSentence(dir int) {
	startAnim()
	if dir > 0 {
		dest := maxPos()
		for _, c := range chunks {
			if c.sentenceStart && c.start > target+2 {
				dest = c.start
				break
			}
		}
		glideTo(dest, omegaFast)
		return
	}
	dest := 0.0
	for i := len(chunks) - 1; i >= 0; i-- {
		if c := chunks[i]; c.sentenceStart && c.start < target-2 {
			dest = c.start
			break
		}
	}
	glideTo(dest, omegaFast)
}

func tick() {
	now := time.Now()
	dt := math.Min(now.Sub(lastTick).Seconds(), 0.05)
	lastTick = now
	if hold != 0 {
		if keyDown(holdVK) {
			target = clampPos(target + float64(hold)*float64(speed)*float64(px(fontSize))*dt)
		} else {
			releaseHold()
		}
	}
	// Critically damped spring: starts from rest, accelerates, settles without overshoot.
	for t := 0.0; t < dt; t += 0.004 {
		h := math.Min(0.004, dt-t)
		vel += (omega*omega*(target-pos) - 2*omega*vel) * h
		pos += vel * h
	}
	if hold == 0 && math.Abs(target-pos) < 0.3 && math.Abs(vel) < 6 {
		pos = target
		stopAnim()
	}
	render()
	// Wait for the next display refresh so every frame lands on a vsync (no 64Hz-vs-60Hz judder).
	if animating && pDwmFlush.Find() == nil {
		call(pDwmFlush)
	}
}

func setText(s string) {
	stopAnim()
	var truncated bool
	text, truncated = limitText(normalize(s))
	allSelected = false
	shape()
	pos, target = 0, 0
	render()
	if truncated {
		showToast("Too long: first part loaded", colorWarn, 4*time.Second)
	}
	call(pSetTimer, hMain, saveTimer, 500, 0)
}

// Shapes the script, showing "Loading..." first when that will take noticeable time.
func shape() {
	if len(text) > largeText {
		loading = true
		render()
	}
	buildChunks()
	loading = false
}

// Another app may hold the clipboard for a moment, so retry briefly.
func openClipboard() bool {
	for range 10 {
		if call(pOpenClipboard, hMain) != 0 {
			return true
		}
		time.Sleep(15 * time.Millisecond)
	}
	return false
}

// Reads at most the allocation size, so text from an app that forgot the terminating NUL is still safe.
func clipboardText() (string, bool) {
	if !openClipboard() {
		return "", false
	}
	defer call(pCloseClipboard)
	h := call(pGetClipboardData, cfUnicodeText)
	if h == 0 {
		return "", false
	}
	size := call(pGlobalSize, h) / 2 * 2
	p := call(pGlobalLock, h)
	if p == 0 {
		return "", false
	}
	defer call(pGlobalUnlock, h)
	if size == 0 {
		return "", true
	}
	buf := make([]uint16, size/2)
	call(pRtlMoveMemory, uintptr(unsafe.Pointer(&buf[0])), p, size)
	return syscall.UTF16ToString(buf), true
}

func copyText() {
	buf, err := syscall.UTF16FromString(strings.ReplaceAll(text, "\n", "\r\n"))
	if err != nil || !openClipboard() {
		return
	}
	defer call(pCloseClipboard)
	call(pEmptyClipboard)
	size := uintptr(len(buf) * 2)
	h := call(pGlobalAlloc, gmemMoveable, size)
	if h == 0 {
		return
	}
	p := call(pGlobalLock, h)
	if p == 0 {
		call(pGlobalFree, h)
		return
	}
	call(pRtlMoveMemory, p, uintptr(unsafe.Pointer(&buf[0])), size)
	call(pGlobalUnlock, h)
	if call(pSetClipboardData, cfUnicodeText, h) == 0 {
		call(pGlobalFree, h)
	}
}

// An empty or whitespace-only clipboard is ignored so a stray Ctrl+V cannot wipe the script.
func paste() {
	if s, ok := clipboardText(); ok && strings.TrimSpace(s) != "" {
		setText(s)
	}
}

func checkPassword(p string) bool {
	salt, _ := hex.DecodeString(passwordSalt)
	want, _ := hex.DecodeString(passwordKey)
	got, err := pbkdf2.Key(sha256.New, p, salt, passwordRounds, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

func submitPassword() {
	if checkPassword(string(password)) {
		locked, password = false, nil
	} else {
		password = nil
		showToast("Wrong password", colorError, 1500*time.Millisecond)
	}
	render()
}

func onPasswordChar(r rune) {
	switch {
	case r == '\r':
		submitPassword()
		return
	case r == '\b':
		if len(password) > 0 {
			password = password[:len(password)-1]
		}
	case r < 0x20 || r == 0x7F:
		return
	case r > 0x7E:
		showToast("Switch keyboard to English", colorWarn, 1500*time.Millisecond)
		render()
		return
	case len(password) < maxPassword:
		password = append(password, r)
	}
	toastUntil = time.Time{}
	render()
}

func pastePassword() {
	if s, ok := clipboardText(); ok {
		line, _, _ := strings.Cut(strings.TrimSpace(normalize(s)), "\n")
		if r := []rune(strings.TrimSpace(line)); len(r) > 0 {
			password = r[:min(len(r), maxPassword)]
			toastUntil = time.Time{}
			render()
		}
	}
}

func resizeWidth(dw int32) {
	r := windowRect()
	w := clamp(r.Right-r.Left+dw, px(200), sm(0))
	x := (r.Left+r.Right)/2 - w/2
	call(pSetWindowPos, hMain, 0, uintptr(x), uintptr(r.Top), uintptr(w), uintptr(r.Bottom-r.Top), swpNoZOrder|swpNoActivate)
	ensureBuffer()
	pos, target = clampPos(pos), clampPos(target)
}

func onKey(vk, lp uintptr) {
	if vk == vkShift || vk == vkControl || vk == vkMenu {
		return
	}
	ctrl := keyDown(vkControl) && !keyDown(vkMenu)
	if locked {
		switch {
		case vk == vkEscape:
			call(pPostMessageW, hMain, wmClose, 0, 0)
		case ctrl && vk == 'V', vk == vkInsert && keyDown(vkShift):
			pastePassword()
		}
		return
	}
	wasSelected := allSelected
	allSelected = false
	if ctrl {
		switch vk {
		case 'V':
			paste()
		case 'C':
			copyText()
			allSelected = wasSelected
		case 'A':
			allSelected = true
		case vkOemPlus, vkAdd, vkUp:
			applyFont(fontSize + 2)
		case vkOemMinus, vkSubtract, vkDown:
			applyFont(fontSize - 2)
		case vkLeft:
			resizeWidth(-px(40))
		case vkRight:
			resizeWidth(px(40))
		}
		render()
		return
	}
	// Right arrow slides the text left (the view moves right), whatever the script direction.
	slideLeft := 1
	if rtl {
		slideLeft = -1
	}
	switch vk {
	case vkRight:
		move(slideLeft, vk, lp)
	case vkLeft:
		move(-slideLeft, vk, lp)
	case vkDown:
		jumpSentence(1)
	case vkUp:
		jumpSentence(-1)
	case vkNext:
		glideBy(float64(bufW-2*inset()), omegaFast)
	case vkPrior:
		glideBy(-float64(bufW-2*inset()), omegaFast)
	case vkEnd:
		glideTo(maxPos(), omegaFast)
	case vkHome:
		glideTo(0, omegaFast)
	case vkOemPlus, vkAdd:
		changeSpeed(1)
	case vkOemMinus, vkSubtract:
		changeSpeed(-1)
	case vkDelete, vkBack:
		if wasSelected {
			setText("")
		}
	case vkInsert:
		if keyDown(vkShift) {
			paste()
		}
	case vkEscape:
		call(pPostMessageW, hMain, wmClose, 0, 0)
	}
	render()
}

func dataPath(name string) string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	dir = filepath.Join(dir, "Teleprompter")
	os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, name)
}

func saveText() {
	if p := dataPath("text.txt"); p != "" {
		os.WriteFile(p, []byte(strings.ReplaceAll(text, "\n", "\r\n")), 0o644)
	}
}

func saveSettings() {
	r := windowRect()
	b, _ := json.Marshal(config{V: configVersion, X: r.Left, Y: r.Top, W: r.Right - r.Left, H: r.Bottom - r.Top, Font: fontSize, Pos: pos, Speed: speed})
	if p := dataPath("settings.json"); p != "" {
		os.WriteFile(p, b, 0o644)
	}
}

func loadSettings() (config, bool) {
	var c config
	b, err := os.ReadFile(dataPath("settings.json"))
	// Version 1 layouts are dropped entirely; version 2 keeps everything but the width (see main).
	if err != nil || json.Unmarshal(b, &c) != nil || c.V < 2 {
		return c, false
	}
	vx, vy, vw, vh := sm(76), sm(77), sm(78), sm(79)
	cx := c.X + c.W/2
	onScreen := cx >= vx && cx <= vx+vw && c.Y >= vy && c.Y <= vy+vh-px(20)
	return c, onScreen && c.W >= px(100) && c.Font >= 12
}

func loadText() (string, bool) {
	b, err := os.ReadFile(dataPath("text.txt"))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func mainProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case wmPaint:
		var ps paintStruct
		hdc := call(pBeginPaint, hwnd, uintptr(unsafe.Pointer(&ps)))
		present(hdc)
		call(pEndPaint, hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0
	case wmEraseBkgnd:
		return 1
	case wmKeyDown:
		onKey(wp, lp)
		return 0
	case wmChar:
		if locked {
			onPasswordChar(rune(wp))
		}
		return 0
	case wmKeyUp:
		if hold != 0 && wp == holdVK {
			releaseHold()
		}
		return 0
	case wmKillFocus:
		releaseHold()
	case wmMouseWheel:
		if locked {
			return 0
		}
		delta := float64(int16(wp >> 16))
		if wp&mkControl != 0 {
			if delta > 0 {
				applyFont(fontSize + 2)
			} else {
				applyFont(fontSize - 2)
			}
			render()
		} else {
			glideBy(-delta/120*step()/2, tapOmega())
		}
		return 0
	case wmLButtonDown, wmRButtonDown:
		if allSelected {
			allSelected = false
			render()
		}
		dragging = true
		call(pSetCapture, hwnd)
		call(pGetCursorPos, uintptr(unsafe.Pointer(&dragCursor)))
		r := windowRect()
		dragWin = point{r.Left, r.Top}
		return 0
	case wmMouseMove:
		if dragging {
			var p point
			call(pGetCursorPos, uintptr(unsafe.Pointer(&p)))
			x, y := dragWin.X+p.X-dragCursor.X, dragWin.Y+p.Y-dragCursor.Y
			call(pSetWindowPos, hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
		}
		return 0
	case wmLButtonUp, wmRButtonUp:
		if dragging {
			dragging = false
			call(pReleaseCapture)
		}
		return 0
	case wmCaptureChanged:
		dragging = false
		return 0
	case wmTimer:
		switch wp {
		case animTimer:
			if animating {
				tick()
			}
		case saveTimer:
			call(pKillTimer, hwnd, saveTimer)
			saveText()
			saveSettings()
		case toastTimer:
			call(pKillTimer, hwnd, toastTimer)
			render()
		}
		return 0
	case wmEndSession:
		if wp != 0 {
			saveText()
			saveSettings()
		}
		return 0
	case wmClose:
		saveText()
		saveSettings()
		call(pDestroyWindow, hwnd)
		return 0
	case wmDestroy:
		call(pPostQuitMessage, 0)
		return 0
	}
	return call(pDefWindowProcW, hwnd, uintptr(msg), wp, lp)
}

func main() {
	// One bar at a time: a second launch just brings the running one to the front.
	if _, _, err := pCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(str(`Local\SG.Teleprompter`)))); err == syscall.Errno(errAlreadyExists) {
		if w := call(pFindWindowW, uintptr(unsafe.Pointer(str("TeleprompterBar"))), 0); w != 0 {
			call(pShowWindow, w, 5)
			call(pSetForegroundWindow, w)
		}
		return
	}
	if pSetProcessDPIAware.Find() == nil {
		call(pSetProcessDPIAware)
	}
	screen := call(pGetDC, 0)
	if dpi := call(pGetDeviceCaps, screen, 90); dpi > 0 {
		scale = float64(dpi) / 96
	}
	call(pReleaseDC, 0, screen)

	hInst := call(pGetModuleHandleW, 0)
	bgBrush = call(pCreateSolidBrush, 0)
	selBrush = call(pCreateSolidBrush, 0x00B4641E)
	toastBrush = call(pCreateSolidBrush, 0x00302820)
	cls := str("TeleprompterBar")
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   syscall.NewCallback(mainProc),
		hInstance:     hInst,
		hCursor:       call(pLoadCursorW, 0, 32512),
		hIcon:         call(pLoadImageW, hInst, 1, 1, uintptr(sm(11)), uintptr(sm(12)), 0), // icon resource 1, SM_CXICON
		hIconSm:       call(pLoadImageW, hInst, 1, 1, uintptr(sm(49)), uintptr(sm(50)), 0), // SM_CXSMICON
		hbrBackground: bgBrush,
		lpszClassName: cls,
	}
	call(pRegisterClassExW, uintptr(unsafe.Pointer(&wc)))

	sw := sm(0)
	w, h := sw*22/100, px(60)
	x, y := (sw-w)/2, int32(0)
	cfg, haveCfg := loadSettings()
	if haveCfg {
		y, fontSize = cfg.Y, cfg.Font
		if cfg.V == configVersion {
			x, w = cfg.X, cfg.W
		}
		if cfg.Speed >= minSpeed && cfg.Speed <= maxSpeed {
			speed = cfg.Speed
		}
	}
	// The password is asked on every launch; an unlock file left by version 1.1.0 is removed.
	locked = true
	os.Remove(dataPath("unlock"))

	hMain = call(pCreateWindowExW, wsExLayered|wsExTopmost, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(str("Teleprompter"))),
		wsPopup, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, hInst, 0)
	call(pSetLayeredWindowAttributes, hMain, 0, opacity, lwaAlpha)

	memDC = call(pCreateCompatibleDC, 0)
	call(pSetBkMode, memDC, bkTransparent)
	call(pSetTextColor, memDC, 0xFFFFFF)
	fadeW = px(32)
	fadeDC = [2]uintptr{makeFade(true), makeFade(false)}
	ensureBuffer()

	s, saved := loadText()
	if !saved {
		s = helpText
	}
	text, _ = limitText(normalize(s))
	setFont(fontSize)

	// Show the bar before shaping so a long saved script displays "Loading..." instead of nothing.
	call(pShowWindow, hMain, 5)
	if pSetWindowDisplayAffinity.Find() == nil {
		call(pSetWindowDisplayAffinity, hMain, wdaExcludeFromCapture)
	}
	call(pSetForegroundWindow, hMain)
	call(pSetFocus, hMain)
	shape()
	if haveCfg {
		pos = clampPos(cfg.Pos)
		target = pos
	}
	render()

	var m msgT
	for int32(call(pGetMessageW, uintptr(unsafe.Pointer(&m)), 0, 0, 0)) > 0 {
		call(pTranslateMessage, uintptr(unsafe.Pointer(&m)))
		call(pDispatchMessageW, uintptr(unsafe.Pointer(&m)))
	}
}
