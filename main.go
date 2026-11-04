package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
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

	esCenter      = 0x0001
	esMultiline   = 0x0004
	esAutoVScroll = 0x0040

	wmDestroy        = 0x0002
	wmSetFocus       = 0x0007
	wmClose          = 0x0010
	wmSetFont        = 0x0030
	wmContextMenu    = 0x007B
	wmKeyDown        = 0x0100
	wmChar           = 0x0102
	wmCommand        = 0x0111
	wmTimer          = 0x0113
	wmCtlColorEdit   = 0x0133
	wmMouseMove      = 0x0200
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
	emLineScroll          = 0x00B6
	emScrollCaret         = 0x00B7
	emLineIndex           = 0x00BB
	emSetLimitText        = 0x00C5
	emUndo                = 0x00C7
	emGetFirstVisibleLine = 0x00CE
	enChange              = 0x0300

	vkEscape   = 0x1B
	vkLeft     = 0x25
	vkUp       = 0x26
	vkRight    = 0x27
	vkDown     = 0x28
	vkControl  = 0x11
	vkMenu     = 0x12
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
	opacity               = 217
)

const helpText = "Ctrl+V = paste your text      Arrows = scroll\r\n" +
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
	pCreateFontW        = gdi32.NewProc("CreateFontW")
	pCreateRoundRectRgn = gdi32.NewProc("CreateRoundRectRgn")
	pDeleteObject       = gdi32.NewProc("DeleteObject")
	pSetTextColor       = gdi32.NewProc("SetTextColor")
	pSetBkColor         = gdi32.NewProc("SetBkColor")
	pGetDeviceCaps      = gdi32.NewProc("GetDeviceCaps")
	pGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
)

var (
	hMain, hEdit, origEditProc uintptr
	bgBrush, curFont           uintptr
	scale                            = 1.0
	fontSize                   int32 = 28
	dragging                   bool
	dragCursor, dragWin        point

	// Negative Win32 constants live in variables: converting a negative constant to uintptr is a compile error.
	gwlpWndProc int32 = -4
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

//go:uintptrescapes
func send(msg, wp, lp uintptr) uintptr { return call(pSendMessageW, hEdit, msg, wp, lp) }

func clamp(v, lo, hi int32) int32 { return max(lo, min(v, hi)) }

func windowRect() rect {
	var r rect
	call(pGetWindowRect, hMain, uintptr(unsafe.Pointer(&r)))
	return r
}

func layout() {
	r := windowRect()
	w, h := r.Right-r.Left, r.Bottom-r.Top
	call(pMoveWindow, hEdit, 0, 0, uintptr(w), uintptr(h), 1)
	pad := rect{px(16), px(6), w - px(16), h - px(4)}
	send(emSetRect, 0, uintptr(unsafe.Pointer(&pad)))
	rad := uintptr(px(28))
	call(pSetWindowRgn, hMain, call(pCreateRoundRectRgn, 0, 0, uintptr(w+1), uintptr(h+1), rad, rad), 1)
}

func applyFont(size int32) {
	fontSize = clamp(size, 10, 200)
	height := -px(fontSize)
	f := call(pCreateFontW, uintptr(height), 0, 0, 0, 600, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(str("Segoe UI"))))
	send(wmSetFont, f, 1)
	if curFont != 0 {
		call(pDeleteObject, curFont)
	}
	curFont = f
	layout()
}

func resizeBy(dw, dh int32) {
	r := windowRect()
	w := clamp(r.Right-r.Left+dw, px(200), sm(0))
	h := clamp(r.Bottom-r.Top+dh, px(40), sm(1))
	x := (r.Left+r.Right)/2 - w/2
	call(pSetWindowPos, hMain, 0, uintptr(x), uintptr(r.Top), uintptr(w), uintptr(h), swpNoZOrder|swpNoActivate)
	layout()
}

// Caret follows the view so typing lands on the line being read.
func scrollLines(n int32) {
	send(emLineScroll, 0, uintptr(n))
	i := send(emLineIndex, send(emGetFirstVisibleLine, 0, 0), 0)
	send(emSetSel, i, i)
}

func textLen() uintptr { return call(pGetWindowTextLengthW, hEdit) }

// Selecting backwards keeps the caret, and so the view, at the start of the text.
func selectAll() { send(emSetSel, textLen(), 0) }

func paste() {
	var start, end uint32
	send(emGetSel, uintptr(unsafe.Pointer(&start)), uintptr(unsafe.Pointer(&end)))
	replaceAll := start == 0 && uintptr(end) == textLen()
	send(wmPaste, 0, 0)
	if replaceAll {
		send(emSetSel, 0, 0)
		send(emScrollCaret, 0, 0)
	}
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
	n := call(pGetWindowTextLengthW, hEdit)
	buf := make([]uint16, n+1)
	call(pGetWindowTextW, hEdit, uintptr(unsafe.Pointer(&buf[0])), n+1)
	if p := dataPath("text.txt"); p != "" {
		os.WriteFile(p, []byte(syscall.UTF16ToString(buf)), 0o644)
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

func handleKey(vk uintptr) bool {
	if keyDown(vkControl) {
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
		case vkOemPlus, vkAdd:
			applyFont(fontSize + 2)
		case vkOemMinus, vkSubtract:
			applyFont(fontSize - 2)
		case vkUp:
			resizeBy(0, -px(20))
		case vkDown:
			resizeBy(0, px(20))
		case vkLeft:
			resizeBy(-px(40), 0)
		case vkRight:
			resizeBy(px(40), 0)
		default:
			return false
		}
		return true
	}
	switch vk {
	case vkLeft, vkUp:
		scrollLines(-1)
	case vkRight, vkDown:
		scrollLines(1)
	case vkEscape:
		call(pPostMessageW, hMain, wmClose, 0, 0)
	default:
		return false
	}
	return true
}

func editProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case wmKeyDown:
		if handleKey(wp) {
			return 0
		}
	case wmChar:
		// Ctrl shortcuts are handled on key-down; their control chars must not reach the edit (double paste, junk glyphs).
		if wp == vkEscape || (keyDown(vkControl) && !keyDown(vkMenu)) {
			return 0
		}
	case wmMouseWheel:
		if wp&mkControl != 0 {
			if int16(wp>>16) > 0 {
				applyFont(fontSize + 2)
			} else {
				applyFont(fontSize - 2)
			}
			return 0
		}
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
	case wmCtlColorEdit:
		call(pSetTextColor, wp, 0xFFFFFF)
		call(pSetBkColor, wp, 0)
		return bgBrush
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
		if wp == saveTimer {
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
	hdc := call(pGetDC, 0)
	if dpi := call(pGetDeviceCaps, hdc, 90); dpi > 0 {
		scale = float64(dpi) / 96
	}
	call(pReleaseDC, 0, hdc)

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

	hEdit = call(pCreateWindowExW, 0, uintptr(unsafe.Pointer(str("EDIT"))), 0,
		wsChild|wsVisible|esMultiline|esAutoVScroll|esCenter, 0, 0, uintptr(w), uintptr(h), hMain, 1, hInst, 0)
	send(emSetLimitText, 0, 0)
	origEditProc = call(pSetWindowLongPtrW, hEdit, uintptr(gwlpWndProc), syscall.NewCallback(editProc))
	applyFont(fontSize)

	text, saved := loadText()
	if !saved {
		text = helpText
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	call(pSetWindowTextW, hEdit, uintptr(unsafe.Pointer(str(text))))
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
