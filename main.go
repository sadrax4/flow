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
	"unsafe"
)

// Win32 windows are bound to their creating thread; without this the Go scheduler can migrate main and freeze the message loop.
func init() { runtime.LockOSThread() }

const (
	wsPopup     = 0x80000000
	wsVisible   = 0x10000000
	wsChild     = 0x40000000
	wsExLayered = 0x00080000
	wsExTopmost = 0x00000008

	esMultiline   = 0x0004
	esAutoVScroll = 0x0040

	wmDestroy        = 0x0002
	wmSetFocus       = 0x0007
	wmKillFocus      = 0x0008
	wmClose          = 0x0010
	wmContextMenu    = 0x007B
	wmKeyDown        = 0x0100
	wmKeyUp          = 0x0101
	wmChar           = 0x0102
	wmCommand        = 0x0111
	wmTimer          = 0x0113
	wmMouseMove      = 0x0200
	wmLButtonDown    = 0x0201
	wmRButtonDown    = 0x0204
	wmRButtonUp      = 0x0205
	wmMouseWheel     = 0x020A
	wmCaptureChanged = 0x0215
	wmCut            = 0x0300
	wmCopy           = 0x0301
	wmPaste          = 0x0302

	emGetSel              = 0x00B0
	emSetSel              = 0x00B1
	emSetRect             = 0x00B3
	emGetLineCount        = 0x00BA
	emLineIndex           = 0x00BB
	emUndo                = 0x00C7
	emGetFirstVisibleLine = 0x00CE
	emPosFromChar         = 0x00D6
	emExLimitText         = 0x0435
	emExLineFromChar      = 0x0436
	emSetBkgndColor       = 0x0443
	emSetCharFormat       = 0x0444
	emSetEventMask        = 0x0445
	emSetParaFormat       = 0x0447
	emRedo                = 0x0454
	emSetTextMode         = 0x0459
	emGetScrollPos        = 0x04DD
	emSetScrollPos        = 0x04DE

	enChange  = 0x0300
	enmChange = 0x00000001

	tmPlainText      = 1
	tmMultiLevelUndo = 8
	tmMultiCodePage  = 32

	scfDefault = 0
	scfAll     = 4
	cfeBold    = 0x00000001
	cfmBold    = 0x00000001
	cfmFace    = 0x20000000
	cfmColor   = 0x40000000
	cfmSize    = 0x80000000

	pfmAlignment = 0x00000008
	pfmRTLPara   = 0x00010000
	pfeRTLPara   = 0x0001
	pfaCenter    = 3

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

	swpNoSize     = 0x0001
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010

	lwaAlpha              = 0x2
	wdaExcludeFromCapture = 0x11
	saveTimer             = 1
	animTimer             = 2
	opacity               = 217

	holdLinesPerSec = 2.5
	easeSeconds     = 0.08
)

const helpText = "Ctrl+V = paste your text      Arrows = smooth scroll (hold to glide)\r\n" +
	"Ctrl + / Ctrl - / Ctrl+Wheel = text size\r\n" +
	"Ctrl+Arrows = resize      Right-drag = move      Esc = close"

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

// CHARFORMATW, 92 bytes.
type charFormatW struct {
	cbSize          uint32
	dwMask          uint32
	dwEffects       uint32
	yHeight         int32
	yOffset         int32
	crTextColor     uint32
	bCharSet        byte
	bPitchAndFamily byte
	szFaceName      [32]uint16
}

// PARAFORMAT2, 188 bytes.
type paraFormat2 struct {
	cbSize           uint32
	dwMask           uint32
	wNumbering       uint16
	wEffects         uint16
	dxStartIndent    int32
	dxRightIndent    int32
	dxOffset         int32
	wAlignment       uint16
	cTabCount        int16
	rgxTabs          [32]int32
	dySpaceBefore    int32
	dySpaceAfter     int32
	dyLineSpacing    int32
	sStyle           int16
	bLineSpacingRule byte
	bOutlineLevel    byte
	wShadingWeight   uint16
	wShadingStyle    uint16
	wNumberingStart  uint16
	wNumberingStyle  uint16
	wNumberingTab    uint16
	wBorderSpace     uint16
	wBorderWidth     uint16
	wBorders         uint16
}

type config struct{ X, Y, W, H, Font int32 }

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pRegisterClassExW           = user32.NewProc("RegisterClassExW")
	pCreateWindowExW            = user32.NewProc("CreateWindowExW")
	pDefWindowProcW             = user32.NewProc("DefWindowProcW")
	pCallWindowProcW            = user32.NewProc("CallWindowProcW")
	pSetWindowLongPtrW          = user32.NewProc("SetWindowLongPtrW")
	pShowWindow                 = user32.NewProc("ShowWindow")
	pSetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	pGetMessageW                = user32.NewProc("GetMessageW")
	pTranslateMessage           = user32.NewProc("TranslateMessage")
	pDispatchMessageW           = user32.NewProc("DispatchMessageW")
	pPostQuitMessage            = user32.NewProc("PostQuitMessage")
	pPostMessageW               = user32.NewProc("PostMessageW")
	pSendMessageW               = user32.NewProc("SendMessageW")
	pDestroyWindow              = user32.NewProc("DestroyWindow")
	pSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	pSetWindowDisplayAffinity   = user32.NewProc("SetWindowDisplayAffinity")
	pSetProcessDPIAware         = user32.NewProc("SetProcessDPIAware")
	pGetSystemMetrics           = user32.NewProc("GetSystemMetrics")
	pSetWindowPos               = user32.NewProc("SetWindowPos")
	pMoveWindow                 = user32.NewProc("MoveWindow")
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
	pGetWindowTextLengthW       = user32.NewProc("GetWindowTextLengthW")
	pGetWindowTextW             = user32.NewProc("GetWindowTextW")
	pSetWindowTextW             = user32.NewProc("SetWindowTextW")

	pCreateSolidBrush   = gdi32.NewProc("CreateSolidBrush")
	pCreateRoundRectRgn = gdi32.NewProc("CreateRoundRectRgn")
	pGetDeviceCaps      = gdi32.NewProc("GetDeviceCaps")
	pGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
)

var (
	hMain, hEdit, origEditProc, bgBrush uintptr
	scale                                     = 1.0
	fontSize                            int32 = 28
	rtl                                 bool
	dragging                            bool
	dragCursor, dragWin                 point

	animating     bool
	curY, targetY float64
	hold          int
	snapAtEnd     bool
	holdVK        uintptr
	lastTick      time.Time

	// Negative Win32 constants live in variables: converting a negative constant to uintptr is a compile error.
	gwlpWndProc int32 = -4
)

//go:uintptrescapes
func call(p *syscall.LazyProc, a ...uintptr) uintptr {
	r, _, _ := p.Call(a...)
	return r
}

//go:uintptrescapes
func send(msg, wp, lp uintptr) uintptr { return call(pSendMessageW, hEdit, msg, wp, lp) }

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

func crlf(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

func getText() string {
	n := call(pGetWindowTextLengthW, hEdit)
	buf := make([]uint16, n+1)
	call(pGetWindowTextW, hEdit, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
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

func getScroll() int32 {
	var p point
	send(emGetScrollPos, 0, uintptr(unsafe.Pointer(&p)))
	return p.Y
}

func setScroll(y int32) {
	p := point{0, max(y, 0)}
	send(emSetScrollPos, 0, uintptr(unsafe.Pointer(&p)))
}

func lineTop(line uintptr) int32 {
	var p point
	send(emPosFromChar, uintptr(unsafe.Pointer(&p)), send(emLineIndex, line, 0))
	return p.Y
}

func lineCount() int { return int(send(emGetLineCount, 0, 0)) }

// Scroll offset that puts line i at the top of the view.
func boundary(i int, top0 int32) float64 { return float64(lineTop(uintptr(i)) - top0) }

// Furthest scroll that still shows the last line at the top; the control may clamp earlier, which tick() detects.
func maxScroll() float64 {
	n := lineCount()
	if n < 2 {
		return 0
	}
	return boundary(n-1, lineTop(0))
}

func lineHeight() float64 {
	if n := lineCount(); n >= 2 {
		if m := maxScroll(); m > 0 {
			return m / float64(n-1)
		}
	}
	return float64(px(fontSize)) * 1.33
}

// First line boundary strictly past y in direction dir. Binary search over real line positions,
// since font fallback for mixed scripts can give lines different heights.
func nextBoundary(y float64, dir int) float64 {
	n := lineCount()
	if n < 2 {
		return 0
	}
	top0 := lineTop(0)
	lo, hi := 0, n-1
	if dir > 0 {
		if boundary(hi, top0) <= y+0.5 {
			return boundary(hi, top0)
		}
		for lo < hi {
			mid := (lo + hi) / 2
			if boundary(mid, top0) > y+0.5 {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		return boundary(lo, top0)
	}
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if boundary(mid, top0) < y-0.5 {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return boundary(lo, top0)
}

func clampScroll(y float64) float64 { return math.Max(0, math.Min(y, maxScroll())) }

func layout() {
	r := windowRect()
	w, h := r.Right-r.Left, r.Bottom-r.Top
	call(pMoveWindow, hEdit, 0, 0, uintptr(w), uintptr(h), 1)
	pad := rect{px(16), px(6), w - px(16), h - px(4)}
	send(emSetRect, 0, uintptr(unsafe.Pointer(&pad)))
	rad := uintptr(px(28))
	call(pSetWindowRgn, hMain, call(pCreateRoundRectRgn, 0, 0, uintptr(w+1), uintptr(h+1), rad, rad), 1)
}

// Later size changes set only the size, so fonts the control picked for other scripts (e.g. Persian, emoji) survive.
func applyFont(size int32, mask uint32) {
	stopAnim()
	fontSize = clamp(size, 10, 200)
	cf := charFormatW{dwMask: mask, dwEffects: cfeBold, yHeight: fontSize * 15, crTextColor: 0xFFFFFF}
	cf.cbSize = uint32(unsafe.Sizeof(cf))
	face, _ := syscall.UTF16FromString("Segoe UI")
	copy(cf.szFaceName[:31], face)
	send(emSetCharFormat, scfAll, uintptr(unsafe.Pointer(&cf)))
	send(emSetCharFormat, scfDefault, uintptr(unsafe.Pointer(&cf)))
	layout()
}

func applyPara() {
	pf := paraFormat2{dwMask: pfmAlignment | pfmRTLPara, wAlignment: pfaCenter}
	pf.cbSize = uint32(unsafe.Sizeof(pf))
	if rtl {
		pf.wEffects = pfeRTLPara
	}
	var start, end uint32
	send(emGetSel, uintptr(unsafe.Pointer(&start)), uintptr(unsafe.Pointer(&end)))
	pos := getScroll()
	send(emSetSel, 0, ^uintptr(0))
	send(emSetParaFormat, 0, uintptr(unsafe.Pointer(&pf)))
	send(emSetSel, uintptr(start), uintptr(end))
	setScroll(pos)
}

func updateDirection() {
	if r := isRTL(getText()); r != rtl {
		rtl = r
		applyPara()
	}
}

func resizeBy(dw, dh int32) {
	stopAnim()
	r := windowRect()
	w := clamp(r.Right-r.Left+dw, px(200), sm(0))
	h := clamp(r.Bottom-r.Top+dh, px(40), sm(1))
	x := (r.Left+r.Right)/2 - w/2
	call(pSetWindowPos, hMain, 0, uintptr(x), uintptr(r.Top), uintptr(w), uintptr(h), swpNoZOrder|swpNoActivate)
	layout()
}

func startAnim() {
	if animating {
		return
	}
	curY = float64(getScroll())
	targetY = curY
	animating = true
	lastTick = time.Now()
	call(pSetTimer, hMain, animTimer, 10, 0)
}

func stopAnim() {
	if animating {
		call(pKillTimer, hMain, animTimer)
	}
	animating, hold, snapAtEnd = false, 0, false
}

// Each tap glides to the next/previous line boundary; taps during a glide stack up.
func stepLines(dir int) {
	startAnim()
	targetY = clampScroll(nextBoundary(targetY, dir))
}

func scrollBy(dy float64) {
	startAnim()
	targetY = clampScroll(targetY + dy)
	snapAtEnd = true
}

func nearestBoundary(y float64) float64 {
	down, up := nextBoundary(y-1, 1), nextBoundary(y+1, -1)
	if down-y < y-up {
		return down
	}
	return up
}

func releaseHold() {
	if hold == 0 {
		return
	}
	// Settle on the line boundary at or just past the glide position.
	targetY = clampScroll(nextBoundary(targetY-float64(hold), hold))
	hold = 0
}

func caretToTop() {
	i := send(emLineIndex, send(emGetFirstVisibleLine, 0, 0), 0)
	pos := getScroll()
	send(emSetSel, i, i)
	setScroll(pos)
}

func tick() {
	now := time.Now()
	dt := math.Min(now.Sub(lastTick).Seconds(), 0.05)
	lastTick = now
	if hold != 0 {
		if !keyDown(holdVK) {
			releaseHold()
		} else {
			targetY = clampScroll(targetY + float64(hold)*holdLinesPerSec*lineHeight()*dt)
		}
	}
	curY += (targetY - curY) * (1 - math.Exp(-dt/easeSeconds))
	done := hold == 0 && math.Abs(targetY-curY) < 0.5
	if done {
		curY = targetY
	}
	want := int32(math.Round(curY))
	setScroll(want)
	if got := getScroll(); got < want-1 || got > want+1 {
		curY, targetY = float64(got), float64(got)
		done = hold == 0
	}
	if done && snapAtEnd {
		snapAtEnd = false
		if b := clampScroll(nearestBoundary(targetY)); math.Abs(b-targetY) >= 1 {
			targetY, done = b, false
		}
	}
	if done {
		stopAnim()
		caretToTop()
	}
}

func selectAll() {
	pos := getScroll()
	send(emSetSel, 0, ^uintptr(0))
	setScroll(pos)
}

// The view jumps to where the pasted text begins, so a fresh script always starts at its first line.
func paste() {
	stopAnim()
	var start, end uint32
	send(emGetSel, uintptr(unsafe.Pointer(&start)), uintptr(unsafe.Pointer(&end)))
	send(wmPaste, 0, 0)
	rtl = isRTL(getText())
	applyPara()
	send(emSetSel, uintptr(start), uintptr(start))
	setScroll(lineTop(send(emExLineFromChar, 0, uintptr(start))) - lineTop(0))
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
		os.WriteFile(p, []byte(crlf(getText())), 0o644)
	}
}

func saveSettings() {
	r := windowRect()
	b, _ := json.Marshal(config{r.Left, r.Top, r.Right - r.Left, r.Bottom - r.Top, fontSize})
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
	return c, onScreen && c.W >= px(100) && c.H >= px(30) && c.Font >= 10
}

func loadText() (string, bool) {
	b, err := os.ReadFile(dataPath("text.txt"))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func viewHeight() float64 {
	r := windowRect()
	return float64(r.Bottom - r.Top - px(10))
}

// Returns true when the key was consumed.
func handleKey(vk, lp uintptr) bool {
	if keyDown(vkControl) && !keyDown(vkMenu) {
		switch vk {
		case 'A':
			selectAll()
		case 'C':
			send(wmCopy, 0, 0)
		case 'V':
			paste()
		case 'X':
			send(wmCut, 0, 0)
		case 'Z':
			send(emUndo, 0, 0)
		case 'Y':
			send(emRedo, 0, 0)
		case vkOemPlus, vkAdd:
			applyFont(fontSize+2, cfmSize)
		case vkOemMinus, vkSubtract:
			applyFont(fontSize-2, cfmSize)
		case vkUp:
			resizeBy(0, -px(20))
		case vkDown:
			resizeBy(0, px(20))
		case vkLeft:
			resizeBy(-px(40), 0)
		case vkRight:
			resizeBy(px(40), 0)
		case vkHome, vkEnd, vkPrior, vkNext, vkBack, vkDelete, vkInsert, vkShift, vkControl, vkMenu:
			stopAnim()
			return false
		}
		// Unlisted Ctrl shortcuts (bold, italic, alignment...) would restyle the whole script, so they are dropped.
		return true
	}
	switch vk {
	case vkLeft, vkUp, vkRight, vkDown:
		dir := 1
		if vk == vkLeft || vk == vkUp {
			dir = -1
		}
		if lp&(1<<30) == 0 {
			stepLines(dir)
		} else if hold == 0 {
			startAnim()
			hold, holdVK = dir, vk
		}
	case vkInsert:
		if !keyDown(vkShift) {
			return false
		}
		paste()
	case vkPrior:
		scrollBy(-viewHeight())
	case vkNext:
		scrollBy(viewHeight())
	case vkEscape:
		call(pPostMessageW, hMain, wmClose, 0, 0)
	default:
		stopAnim()
		return false
	}
	return true
}

func editProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case wmKeyDown:
		if handleKey(wp, lp) {
			return 0
		}
	case wmKeyUp:
		switch wp {
		case vkLeft, vkUp, vkRight, vkDown:
			if wp == holdVK {
				releaseHold()
			}
			return 0
		}
	case wmKillFocus:
		releaseHold()
	case wmChar:
		// Ctrl shortcuts are handled on key-down; their control chars must not reach the control.
		if wp == vkEscape || (keyDown(vkControl) && !keyDown(vkMenu)) {
			return 0
		}
	case wmMouseWheel:
		delta := float64(int16(wp >> 16))
		if wp&mkControl != 0 {
			if delta > 0 {
				applyFont(fontSize+2, cfmSize)
			} else {
				applyFont(fontSize-2, cfmSize)
			}
		} else {
			scrollBy(-delta / 120 * lineHeight())
		}
		return 0
	case wmLButtonDown:
		stopAnim()
	case wmRButtonDown:
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
			call(pSetWindowPos, hMain, 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
			return 0
		}
	case wmRButtonUp:
		if dragging {
			dragging = false
			call(pReleaseCapture)
		}
		return 0
	case wmCaptureChanged:
		dragging = false
	case wmContextMenu:
		return 0
	}
	return call(pCallWindowProcW, origEditProc, hwnd, uintptr(msg), wp, lp)
}

func mainProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case wmSetFocus:
		if hEdit != 0 {
			call(pSetFocus, hEdit)
		}
		return 0
	case wmCommand:
		if lp == hEdit && (wp>>16)&0xFFFF == enChange {
			call(pSetTimer, hwnd, saveTimer, 800, 0)
		}
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
			updateDirection()
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
	hdc := call(pGetDC, 0)
	if dpi := call(pGetDeviceCaps, hdc, 90); dpi > 0 {
		scale = float64(dpi) / 96
	}
	call(pReleaseDC, 0, hdc)
	if _, err := syscall.LoadLibrary("Msftedit.dll"); err != nil {
		return
	}

	hInst := call(pGetModuleHandleW, 0)
	bgBrush = call(pCreateSolidBrush, 0)
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
	if c, ok := loadSettings(); ok {
		x, y, w, h, fontSize = c.X, c.Y, c.W, c.H, c.Font
	}

	hMain = call(pCreateWindowExW, wsExLayered|wsExTopmost, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(str("Teleprompter"))),
		wsPopup, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, hInst, 0)
	call(pSetLayeredWindowAttributes, hMain, 0, opacity, lwaAlpha)

	hEdit = call(pCreateWindowExW, 0, uintptr(unsafe.Pointer(str("RICHEDIT50W"))), 0,
		wsChild|wsVisible|esMultiline|esAutoVScroll, 0, 0, uintptr(w), uintptr(h), hMain, 1, hInst, 0)
	if hEdit == 0 {
		return
	}
	send(emSetTextMode, tmPlainText|tmMultiLevelUndo|tmMultiCodePage, 0)
	send(emExLimitText, 0, 1<<26)
	send(emSetBkgndColor, 0, 0)
	send(emSetEventMask, 0, enmChange)
	origEditProc = call(pSetWindowLongPtrW, hEdit, uintptr(gwlpWndProc), syscall.NewCallback(editProc))
	applyFont(fontSize, cfmSize|cfmColor|cfmFace|cfmBold)

	text, saved := loadText()
	if !saved {
		text = helpText
	}
	call(pSetWindowTextW, hEdit, uintptr(unsafe.Pointer(str(crlf(text)))))
	rtl = isRTL(text)
	applyPara()
	send(emSetSel, 0, 0)
	setScroll(0)
	if !saved {
		selectAll()
	}

	call(pShowWindow, hMain, 5)
	if pSetWindowDisplayAffinity.Find() == nil {
		call(pSetWindowDisplayAffinity, hMain, wdaExcludeFromCapture)
	}
	call(pSetForegroundWindow, hMain)
	call(pSetFocus, hEdit)

	var m msgT
	for int32(call(pGetMessageW, uintptr(unsafe.Pointer(&m)), 0, 0, 0)) > 0 {
		call(pTranslateMessage, uintptr(unsafe.Pointer(&m)))
		call(pDispatchMessageW, uintptr(unsafe.Pointer(&m)))
	}
}
