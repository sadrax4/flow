package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
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
	wmKeyDown        = 0x0100
	wmKeyUp          = 0x0101
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
	dtNoPrefix    = 0x0800

	swpNoSize     = 0x0001
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010

	lwaAlpha              = 0x2
	wdaExcludeFromCapture = 0x11
	animTimer             = 1
	saveTimer             = 2
	opacity               = 217

	springOmega  = 14.0
	holdSpeedEms = 8.0
	separator    = "   •   "
	hintText     = "Ctrl+V  =  paste your text"
)

const helpText = "Ctrl+V = paste your text\n" +
	"Right / Left arrow = move the text (hold to glide)\n" +
	"Ctrl + / Ctrl - = text size\n" +
	"Ctrl + arrows = bar size\n" +
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
	ssa          uintptr
	buf          []uint16
	start, width float64
}

type config struct {
	X, Y, W, H, Font int32
	Pos              float64
}

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	usp10    = syscall.NewLazyDLL("usp10.dll")

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

	pGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	pGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	pGlobalFree       = kernel32.NewProc("GlobalFree")
	pGlobalLock       = kernel32.NewProc("GlobalLock")
	pGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	pLstrlenW         = kernel32.NewProc("lstrlenW")
	pRtlMoveMemory    = kernel32.NewProc("RtlMoveMemory")

	pScriptStringAnalyse = usp10.NewProc("ScriptStringAnalyse")
	pScriptStringOut     = usp10.NewProc("ScriptStringOut")
	pScriptStringFree    = usp10.NewProc("ScriptStringFree")
	pScriptStringPSize   = usp10.NewProc("ScriptString_pSize")
)

var (
	hMain, memDC, memBmp, curFont uintptr
	bgBrush, selBrush             uintptr
	fadeDC                        [2]uintptr
	bufW, bufH, fadeW, lineH      int32
	scale                               = 1.0
	fontSize                      int32 = 36

	text        string
	chunks      []chunk
	total       float64
	rtl         bool
	allSelected bool

	animating        bool
	pos, target, vel float64
	hold             int
	holdVK           uintptr
	lastTick         time.Time

	dragging            bool
	dragCursor, dragWin point

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
	s = strings.ReplaceAll(s, "\x00", "")
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

// Paragraphs joined by a separator, cut at spaces into runs small enough to shape quickly.
func pieces(s string) []string {
	var paras, out []string
	for _, p := range strings.Split(s, "\n") {
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			paras = append(paras, p)
		}
	}
	for i, p := range paras {
		if i < len(paras)-1 {
			p += separator
		}
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
			out = append(out, p[:cut])
			p = p[cut:]
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
	for _, piece := range pieces(text) {
		buf, err := syscall.UTF16FromString(piece)
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
		chunks = append(chunks, chunk{ssa: ssa, buf: buf, start: total, width: float64(size.X)})
		total += float64(size.X)
	}
}

func inset() int32 { return fadeW + px(8) }

func maxPos() float64 { return math.Max(0, total-float64(bufW-2*inset())) }

func clampPos(p float64) float64 { return math.Max(0, math.Min(p, maxPos())) }

func step() float64 { return float64(bufW-2*inset()) / 3 }

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
	freeChunks()
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
	buildChunks()
	pos = clampPos(keep * total)
	target = pos
}

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
	rad := uintptr(px(28))
	call(pSetWindowRgn, hMain, call(pCreateRoundRectRgn, 0, 0, uintptr(w+1), uintptr(h+1), rad, rad), 1)
}

func draw() {
	full := rect{0, 0, bufW, bufH}
	call(pFillRect, memDC, uintptr(unsafe.Pointer(&full)), bgBrush)
	if len(chunks) == 0 {
		call(pSetTextColor, memDC, 0x808080)
		call(pDrawTextW, memDC, uintptr(unsafe.Pointer(str(hintText))), ^uintptr(0), uintptr(unsafe.Pointer(&full)),
			dtCenter|dtVCenter|dtSingleLine|dtNoPrefix)
		call(pSetTextColor, memDC, 0xFFFFFF)
	}
	y := (bufH - lineH) / 2
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
	blend := uintptr(255<<16 | 1<<24) // SourceConstantAlpha 255, AC_SRC_ALPHA
	call(pGdiAlphaBlend, memDC, 0, 0, uintptr(fadeW), uintptr(bufH), fadeDC[0], 0, 0, uintptr(fadeW), 1, blend)
	call(pGdiAlphaBlend, memDC, uintptr(bufW-fadeW), 0, uintptr(fadeW), uintptr(bufH), fadeDC[1], 0, 0, uintptr(fadeW), 1, blend)
}

func present(hdc uintptr) {
	ensureBuffer()
	draw()
	call(pBitBlt, hdc, 0, 0, uintptr(bufW), uintptr(bufH), memDC, 0, 0, srcCopy)
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

func glideTo(p float64) {
	startAnim()
	target = clampPos(p)
}

func glideBy(d float64) {
	startAnim()
	target = clampPos(target + d)
}

// A tap glides a third of the bar; key auto-repeat (a held arrow) switches to a steady glide.
func move(dir int, vk, lp uintptr) {
	if lp&(1<<30) == 0 {
		glideBy(float64(dir) * step())
	} else if hold == 0 {
		startAnim()
		hold, holdVK = dir, vk
	}
}

func tick() {
	now := time.Now()
	dt := math.Min(now.Sub(lastTick).Seconds(), 0.05)
	lastTick = now
	if hold != 0 {
		if keyDown(holdVK) {
			target = clampPos(target + float64(hold)*holdSpeedEms*float64(px(fontSize))*dt)
		} else {
			hold = 0
		}
	}
	// Critically damped spring: starts from rest, accelerates, settles without overshoot.
	for t := 0.0; t < dt; t += 0.004 {
		h := math.Min(0.004, dt-t)
		vel += (springOmega*springOmega*(target-pos) - 2*springOmega*vel) * h
		pos += vel * h
	}
	if hold == 0 && math.Abs(target-pos) < 0.3 && math.Abs(vel) < 6 {
		pos = target
		stopAnim()
	}
	render()
}

func setText(s string) {
	stopAnim()
	text = normalize(s)
	allSelected = false
	buildChunks()
	pos, target = 0, 0
	render()
	call(pSetTimer, hMain, saveTimer, 500, 0)
}

func clipboardText() (string, bool) {
	if call(pOpenClipboard, hMain) == 0 {
		return "", false
	}
	defer call(pCloseClipboard)
	h := call(pGetClipboardData, cfUnicodeText)
	if h == 0 {
		return "", false
	}
	p := call(pGlobalLock, h)
	if p == 0 {
		return "", false
	}
	defer call(pGlobalUnlock, h)
	n := call(pLstrlenW, p)
	buf := make([]uint16, n+1)
	if n > 0 {
		call(pRtlMoveMemory, uintptr(unsafe.Pointer(&buf[0])), p, n*2)
	}
	return syscall.UTF16ToString(buf), true
}

func copyText() {
	buf, err := syscall.UTF16FromString(strings.ReplaceAll(text, "\n", "\r\n"))
	if err != nil || call(pOpenClipboard, hMain) == 0 {
		return
	}
	defer call(pCloseClipboard)
	call(pEmptyClipboard)
	size := uintptr(len(buf) * 2)
	h := call(pGlobalAlloc, gmemMoveable, size)
	if h == 0 {
		return
	}
	call(pRtlMoveMemory, call(pGlobalLock, h), uintptr(unsafe.Pointer(&buf[0])), size)
	call(pGlobalUnlock, h)
	if call(pSetClipboardData, cfUnicodeText, h) == 0 {
		call(pGlobalFree, h)
	}
}

func paste() {
	if s, ok := clipboardText(); ok {
		setText(s)
	}
}

func resizeBy(dw, dh int32) {
	r := windowRect()
	w := clamp(r.Right-r.Left+dw, px(200), sm(0))
	h := clamp(r.Bottom-r.Top+dh, px(40), sm(1))
	x := (r.Left+r.Right)/2 - w/2
	call(pSetWindowPos, hMain, 0, uintptr(x), uintptr(r.Top), uintptr(w), uintptr(h), swpNoZOrder|swpNoActivate)
	ensureBuffer()
	pos, target = clampPos(pos), clampPos(target)
}

func onKey(vk, lp uintptr) {
	if vk == vkShift || vk == vkControl || vk == vkMenu {
		return
	}
	wasSelected := allSelected
	allSelected = false
	if keyDown(vkControl) && !keyDown(vkMenu) {
		switch vk {
		case 'V':
			paste()
		case 'C':
			copyText()
			allSelected = wasSelected
		case 'A':
			allSelected = true
		case vkOemPlus, vkAdd:
			applyFont(fontSize + 2)
		case vkOemMinus, vkSubtract:
			applyFont(fontSize - 2)
		case vkUp:
			resizeBy(0, -px(10))
		case vkDown:
			resizeBy(0, px(10))
		case vkLeft:
			resizeBy(-px(40), 0)
		case vkRight:
			resizeBy(px(40), 0)
		}
		render()
		return
	}
	rightward := -1
	if rtl {
		rightward = 1
	}
	switch vk {
	case vkRight:
		move(rightward, vk, lp)
	case vkLeft:
		move(-rightward, vk, lp)
	case vkDown:
		move(1, vk, lp)
	case vkUp:
		move(-1, vk, lp)
	case vkNext:
		glideBy(float64(bufW - 2*inset()))
	case vkPrior:
		glideBy(-float64(bufW - 2*inset()))
	case vkEnd:
		glideTo(maxPos())
	case vkHome:
		glideTo(0)
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
	b, _ := json.Marshal(config{r.Left, r.Top, r.Right - r.Left, r.Bottom - r.Top, fontSize, pos})
	if p := dataPath("settings.json"); p != "" {
		os.WriteFile(p, b, 0o644)
	}
}

func loadSettings() (config, bool) {
	var c config
	b, err := os.ReadFile(dataPath("settings.json"))
	if err != nil || json.Unmarshal(b, &c) != nil {
		return c, false
	}
	vx, vy, vw, vh := sm(76), sm(77), sm(78), sm(79)
	cx := c.X + c.W/2
	onScreen := cx >= vx && cx <= vx+vw && c.Y >= vy && c.Y <= vy+vh-px(20)
	return c, onScreen && c.W >= px(100) && c.H >= px(30) && c.Font >= 12
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
	case wmKeyUp:
		if hold != 0 && wp == holdVK {
			hold = 0
		}
		return 0
	case wmKillFocus:
		hold = 0
	case wmMouseWheel:
		delta := float64(int16(wp >> 16))
		if wp&mkControl != 0 {
			if delta > 0 {
				applyFont(fontSize + 2)
			} else {
				applyFont(fontSize - 2)
			}
			render()
		} else {
			glideBy(-delta / 120 * step() / 2)
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
	cls := str("TeleprompterBar")
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   syscall.NewCallback(mainProc),
		hInstance:     hInst,
		hCursor:       call(pLoadCursorW, 0, 32512),
		hbrBackground: bgBrush,
		lpszClassName: cls,
	}
	call(pRegisterClassExW, uintptr(unsafe.Pointer(&wc)))

	sw := sm(0)
	w, h := sw*2/5, px(80)
	x, y := (sw-w)/2, int32(0)
	cfg, haveCfg := loadSettings()
	if haveCfg {
		x, y, w, h, fontSize = cfg.X, cfg.Y, cfg.W, cfg.H, cfg.Font
	}

	hMain = call(pCreateWindowExW, wsExLayered|wsExTopmost, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(str("Teleprompter"))),
		wsPopup, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, hInst, 0)
	call(pSetLayeredWindowAttributes, hMain, 0, opacity, lwaAlpha)

	memDC = call(pCreateCompatibleDC, 0)
	call(pSetBkMode, memDC, bkTransparent)
	call(pSetTextColor, memDC, 0xFFFFFF)
	fadeW = px(40)
	fadeDC = [2]uintptr{makeFade(true), makeFade(false)}
	ensureBuffer()

	s, saved := loadText()
	if !saved {
		s = helpText
	}
	text = normalize(s)
	applyFont(fontSize)
	if haveCfg {
		pos = clampPos(cfg.Pos)
		target = pos
	}

	call(pShowWindow, hMain, 5)
	if pSetWindowDisplayAffinity.Find() == nil {
		call(pSetWindowDisplayAffinity, hMain, wdaExcludeFromCapture)
	}
	call(pSetForegroundWindow, hMain)
	call(pSetFocus, hMain)
	render()

	var m msgT
	for int32(call(pGetMessageW, uintptr(unsafe.Pointer(&m)), 0, 0, 0)) > 0 {
		call(pTranslateMessage, uintptr(unsafe.Pointer(&m)))
		call(pDispatchMessageW, uintptr(unsafe.Pointer(&m)))
	}
}
