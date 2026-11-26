package winui

import (
	"fmt"
	"image/color"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	apptheme "quailcrate/internal/theme"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	imm32    = windows.NewLazySystemDLL("imm32.dll")

	getModuleHandle            = kernel32.NewProc("GetModuleHandleW")
	registerClass              = user32.NewProc("RegisterClassExW")
	createWindow               = user32.NewProc("CreateWindowExW")
	defWindowProc              = user32.NewProc("DefWindowProcW")
	callWindowProc             = user32.NewProc("CallWindowProcW")
	setWindowLong              = user32.NewProc("SetWindowLongPtrW")
	destroyWindow              = user32.NewProc("DestroyWindow")
	isWindow                   = user32.NewProc("IsWindow")
	isWindowVisible            = user32.NewProc("IsWindowVisible")
	getMessage                 = user32.NewProc("GetMessageW")
	translateMessage           = user32.NewProc("TranslateMessage")
	dispatchMessage            = user32.NewProc("DispatchMessageW")
	isDialogMessage            = user32.NewProc("IsDialogMessageW")
	postMessage                = user32.NewProc("PostMessageW")
	postQuitMessage            = user32.NewProc("PostQuitMessage")
	sendMessage                = user32.NewProc("SendMessageW")
	registerWindowMessage      = user32.NewProc("RegisterWindowMessageW")
	showWindow                 = user32.NewProc("ShowWindow")
	setForegroundWindow        = user32.NewProc("SetForegroundWindow")
	getForegroundWindow        = user32.NewProc("GetForegroundWindow")
	getShellWindow             = user32.NewProc("GetShellWindow")
	getWindow                  = user32.NewProc("GetWindow")
	findWindowEx               = user32.NewProc("FindWindowExW")
	getWindowText              = user32.NewProc("GetWindowTextW")
	getWindowTextLength        = user32.NewProc("GetWindowTextLengthW")
	setWindowText              = user32.NewProc("SetWindowTextW")
	getClientRect              = user32.NewProc("GetClientRect")
	getWindowRect              = user32.NewProc("GetWindowRect")
	getCursorPos               = user32.NewProc("GetCursorPos")
	screenToClient             = user32.NewProc("ScreenToClient")
	setWindowPos               = user32.NewProc("SetWindowPos")
	beginDeferWindowPos        = user32.NewProc("BeginDeferWindowPos")
	deferWindowPos             = user32.NewProc("DeferWindowPos")
	endDeferWindowPos          = user32.NewProc("EndDeferWindowPos")
	setWindowRgn               = user32.NewProc("SetWindowRgn")
	moveWindow                 = user32.NewProc("MoveWindow")
	enableWindow               = user32.NewProc("EnableWindow")
	setFocus                   = user32.NewProc("SetFocus")
	getFocus                   = user32.NewProc("GetFocus")
	setCapture                 = user32.NewProc("SetCapture")
	getCapture                 = user32.NewProc("GetCapture")
	releaseCapture             = user32.NewProc("ReleaseCapture")
	trackMouseEvent            = user32.NewProc("TrackMouseEvent")
	getKeyState                = user32.NewProc("GetKeyState")
	monitorFromPoint           = user32.NewProc("MonitorFromPoint")
	monitorFromWindow          = user32.NewProc("MonitorFromWindow")
	getMonitorInfo             = user32.NewProc("GetMonitorInfoW")
	enumDisplayMonitors        = user32.NewProc("EnumDisplayMonitors")
	getDpiForWindow            = user32.NewProc("GetDpiForWindow")
	setDpiAwareness            = user32.NewProc("SetProcessDpiAwarenessContext")
	loadCursor                 = user32.NewProc("LoadCursorW")
	setCursor                  = user32.NewProc("SetCursor")
	loadIcon                   = user32.NewProc("LoadIconW")
	setTimer                   = user32.NewProc("SetTimer")
	killTimer                  = user32.NewProc("KillTimer")
	createPopupMenu            = user32.NewProc("CreatePopupMenu")
	appendMenu                 = user32.NewProc("AppendMenuW")
	trackPopupMenu             = user32.NewProc("TrackPopupMenu")
	destroyMenu                = user32.NewProc("DestroyMenu")
	registerHotKey             = user32.NewProc("RegisterHotKey")
	unregisterHotKey           = user32.NewProc("UnregisterHotKey")
	messageBox                 = user32.NewProc("MessageBoxW")
	invalidateRect             = user32.NewProc("InvalidateRect")
	beginPaint                 = user32.NewProc("BeginPaint")
	endPaint                   = user32.NewProc("EndPaint")
	fillRect                   = user32.NewProc("FillRect")
	drawText                   = user32.NewProc("DrawTextW")
	getSysColor                = user32.NewProc("GetSysColor")
	systemParametersInfo       = user32.NewProc("SystemParametersInfoW")
	updateLayeredWindow        = user32.NewProc("UpdateLayeredWindow")
	setLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	createCompatibleDC         = gdi32.NewProc("CreateCompatibleDC")
	bitBlt                     = gdi32.NewProc("BitBlt")
	deleteDC                   = gdi32.NewProc("DeleteDC")
	createDIBSection           = gdi32.NewProc("CreateDIBSection")
	extCreateRegion            = gdi32.NewProc("ExtCreateRegion")
	createSolidBrush           = gdi32.NewProc("CreateSolidBrush")
	deleteObject               = gdi32.NewProc("DeleteObject")
	selectObject               = gdi32.NewProc("SelectObject")
	setBkMode                  = gdi32.NewProc("SetBkMode")
	setTextColor               = gdi32.NewProc("SetTextColor")
	createFont                 = gdi32.NewProc("CreateFontW")
	shellNotify                = shell32.NewProc("Shell_NotifyIconW")
	oleInitialize              = ole32.NewProc("OleInitialize")
	oleUninitialize            = ole32.NewProc("OleUninitialize")
	registerClipboardFormat    = user32.NewProc("RegisterClipboardFormatW")
	globalLock                 = kernel32.NewProc("GlobalLock")
	globalUnlock               = kernel32.NewProc("GlobalUnlock")
	immGetContext              = imm32.NewProc("ImmGetContext")
	immReleaseContext          = imm32.NewProc("ImmReleaseContext")
	immGetCompositionString    = imm32.NewProc("ImmGetCompositionStringW")
)

const (
	wmDestroy        = 0x0002
	wmMove           = 0x0003
	wmSize           = 0x0005
	wmActivate       = 0x0006
	wmSetFocus       = 0x0007
	wmKillFocus      = 0x0008
	wmPaint          = 0x000f
	wmClose          = 0x0010
	wmEraseBkgnd       = 0x0014
	wmSetCursor        = 0x0020
	wmMouseActivate    = 0x0021
	wmWindowPosChanged = 0x0047
	wmNcHitTest        = 0x0084
	wmTimeChange       = 0x001e
	wmGetDlgCode     = 0x0087
	wmCommand        = 0x0111
	wmTimer          = 0x0113
	wmCtlColorEdit   = 0x0133
	wmKeyDown        = 0x0100
	wmChar           = 0x0102
	wmMouseMove      = 0x0200
	wmLButtonDown    = 0x0201
	wmLButtonUp      = 0x0202
	wmLButtonDblClk  = 0x0203
	wmRButtonUp      = 0x0205
	wmMouseWheel     = 0x020a
	wmCaptureChanged = 0x0215
	wmPowerBroadcast = 0x0218
	wmMouseLeave     = 0x02a3
	wmDisplayChange  = 0x007e
	wmDpiChanged     = 0x02e0
	wmHotKey         = 0x0312
	wmSetFont        = 0x0030
	wmSettingChange  = 0x001a
	wmTray           = 0x8001
	wmDispatch       = 0x8002

	gwlStyle   = ^uintptr(15)
	gwlExStyle = ^uintptr(19)

	wsPopup           = 0x80000000
	wsVisible         = 0x10000000
	wsMinimized       = 0x20000000
	wsChild           = 0x40000000
	wsCaption         = 0x00c00000
	wsSysMenu         = 0x00080000
	wsVScroll         = 0x00200000
	wsTabStop         = 0x00010000
	wsExToolWindow    = 0x00000080
	wsExClientEdge    = 0x00000200
	wsExControlParent = 0x00010000
	wsExTopmost       = 0x00000008
	wsExTransparent   = 0x00000020
	wsExLayered       = 0x00080000
	wsExNoActivate    = 0x08000000

	ulwAlpha   = 0x00000002
	lwaAlpha   = 0x00000002
	acSrcOver  = 0x00
	acSrcAlpha = 0x01
	tmeLeave   = 0x00000002

	swHide                 = 0
	swShowNormalNoActivate = 4
	swShow                 = 5
	swShowNoActivate       = 8
	swpNoSize              = 0x0001
	swpNoMove              = 0x0002
	swpNoZOrder            = 0x0004
	swpNoActivate          = 0x0010
	swpNoOwnerZOrder       = 0x0200
	zFlags                 = swpNoSize | swpNoMove | swpNoActivate | swpNoOwnerZOrder
	hwndBottom             = 1
	hwndTopmost            = ^uintptr(0)
	hwndNotTopmost         = ^uintptr(1)

	vkEscape = 0x1b
	vkReturn = 0x0d
	vkTab    = 0x09
	vkSpace  = 0x20
	vkLeft   = 0x25
	vkUp     = 0x26
	vkRight  = 0x27
	vkDown   = 0x28
	vkShift   = 0x10
	vkControl = 0x11
	vkMenu    = 0x12
	mkControl = 0x0008
)

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }

type blendFunction struct {
	BlendOp             byte
	BlendFlags          byte
	SourceConstantAlpha byte
	AlphaFormat         byte
}

type trackMouseEventInfo struct {
	Size      uint32
	Flags     uint32
	HWnd      uintptr
	HoverTime uint32
}

func (r rect) width() int32  { return r.Right - r.Left }
func (r rect) height() int32 { return r.Bottom - r.Top }
func (r rect) contains(p point) bool {
	return p.X >= r.Left && p.X < r.Right && p.Y >= r.Top && p.Y < r.Bottom
}

type message struct {
	HWnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Point          point
	Private        uint32
}

type wndClass struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	IconSmall                          uintptr
}

type monitorInfo struct {
	Size          uint32
	Monitor, Work rect
	Flags         uint32
	Device        [32]uint16
}

type paintStruct struct {
	DC                 uintptr
	Erase              int32
	Rect               rect
	Restore, IncUpdate int32
	Reserved           [32]byte
}

func wstr(s string) *uint16 {
	value, err := syscall.UTF16PtrFromString(s)
	if err == nil {
		return value
	}
	return syscall.StringToUTF16Ptr("")
}

// Windows supplies these addresses in callback parameters or COM interface
// pointers. They are not Go addresses and are valid for the native call.
func nativePointer[T any](address uintptr) *T {
	return *(**T)(unsafe.Pointer(&address))
}

func win32Error(err error) string {
	if code, ok := err.(syscall.Errno); ok {
		return fmt.Sprintf("Win32 error %d", uint32(code))
	}
	if err == nil {
		return "unknown Win32 error"
	}
	return err.Error()
}
func coord(v int32) uintptr      { return uintptr(v) }
func low(v uintptr) int          { return int(uint16(v)) }
func high(v uintptr) int         { return int(uint16(v >> 16)) }
func mousePoint(v uintptr) point { return point{int32(int16(v)), int32(int16(v >> 16))} }

func cursorPosition() point {
	var p point
	getCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return p
}

func windowRect(h uintptr) rect {
	var r rect
	getWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	return r
}

func clientRect(h uintptr) rect {
	var r rect
	getClientRect.Call(h, uintptr(unsafe.Pointer(&r)))
	return r
}

func move(h uintptr, r rect) {
	moveWindow.Call(h, coord(r.Left), coord(r.Top), coord(r.width()), coord(r.height()), 1)
}

func currentText(h uintptr) string {
	n, _, _ := getWindowTextLength.Call(h)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	getWindowText.Call(h, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
}

func setText(h uintptr, text string) { setWindowText.Call(h, uintptr(unsafe.Pointer(wstr(text)))) }
func repaint(h uintptr)              { invalidateRect.Call(h, 0, 0) }

func dpiForWindow(h uintptr) int32 {
	dpi, _, _ := getDpiForWindow.Call(h)
	if dpi == 0 {
		return 96
	}
	return int32(dpi)
}

func monitorOf(h uintptr) monitorInfo {
	m, _, _ := monitorFromWindow.Call(h, 2)
	return monitorDetails(m)
}

func monitorHandleAt(p point) uintptr {
	m, _, _ := monitorFromPoint.Call(*(*uintptr)(unsafe.Pointer(&p)), 2)
	return m
}

func monitorOfPoint(p point) monitorInfo { return monitorDetails(monitorHandleAt(p)) }

func monitorDetails(h uintptr) monitorInfo {
	m := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	getMonitorInfo.Call(h, uintptr(unsafe.Pointer(&m)))
	return m
}

func scale(dip, dpi int32) int32      { return dip * dpi / 96 }
func unscale(pixels, dpi int32) int32 { return pixels * 96 / dpi }

func window(class, title string, ex, style uintptr, r rect, parent, menu, instance uintptr) (uintptr, error) {
	h, _, err := createWindow.Call(ex, uintptr(unsafe.Pointer(wstr(class))), uintptr(unsafe.Pointer(wstr(title))), style, coord(r.Left), coord(r.Top), coord(r.width()), coord(r.height()), parent, menu, instance, 0)
	if h == 0 {
		return 0, fmt.Errorf("CreateWindowExW %s failed (%s)", class, win32Error(err))
	}
	return h, nil
}

func rgb(r, g, b byte) uintptr { return uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16 }
func colorRef(c color.NRGBA) uintptr {
	return rgb(c.R, c.G, c.B)
}
func sysColor(index uintptr) uintptr {
	value, _, _ := getSysColor.Call(index)
	return value
}

type palette struct {
	background, surface, sidebar, text, secondary, border, accent, selected, hover, warning, white uintptr
}

func isSystemHighContrast() bool {
	var highContrast struct {
		Size, Flags uint32
		Scheme      uintptr
	}
	highContrast.Size = uint32(unsafe.Sizeof(highContrast))
	result, _, _ := systemParametersInfo.Call(0x0042, uintptr(highContrast.Size), uintptr(unsafe.Pointer(&highContrast)), 0)
	return result != 0 && highContrast.Flags&1 != 0
}

func paletteFromTheme(p apptheme.Palette) palette {
	return palette{
		background: colorRef(p.Background),
		surface:    colorRef(p.Surface),
		sidebar:    colorRef(p.Sidebar),
		text:       colorRef(p.Text),
		secondary:  colorRef(p.Secondary),
		border:     colorRef(p.Border),
		accent:     colorRef(p.Accent),
		selected:   colorRef(p.Selected),
		hover:      colorRef(p.Hover),
		warning:    colorRef(p.Warning),
		white:      colorRef(p.OnAccent),
	}
}

func highContrastPalette() palette {
	return palette{
		background: sysColor(15), surface: sysColor(5), sidebar: sysColor(5), text: sysColor(8),
		secondary: sysColor(8), border: sysColor(8), accent: sysColor(13), selected: sysColor(13),
		hover: sysColor(13), warning: sysColor(8), white: sysColor(14),
	}
}

func theme() palette {
	if isSystemHighContrast() {
		return highContrastPalette()
	}
	if activeUI != nil {
		return paletteFromTheme(activeUI.appearance.Resolve(apptheme.ComponentSettings).Palette)
	}
	return paletteFromTheme(apptheme.DefaultPalette())
}

func fill(hdc uintptr, r rect, color uintptr) {
	brush, _, _ := createSolidBrush.Call(color)
	fillRect.Call(hdc, uintptr(unsafe.Pointer(&r)), brush)
	deleteObject.Call(brush)
}

func font(dpi, size int32, bold bool) uintptr {
	return fontFace(dpi, size, bold, "Segoe UI")
}

func fontFace(dpi, size int32, bold bool, face string) uintptr {
	weight := uintptr(400)
	if bold {
		weight = 600
	}
	h, _, _ := createFont.Call(coord(-scale(size, dpi)), 0, 0, 0, weight, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(wstr(face))))
	return h
}

func text(hdc uintptr, r rect, value string, f, color uintptr, align uintptr) {
	previous, _, _ := selectObject.Call(hdc, f)
	setBkMode.Call(hdc, 1)
	setTextColor.Call(hdc, color)
	drawText.Call(hdc, uintptr(unsafe.Pointer(wstr(value))), ^uintptr(0), uintptr(unsafe.Pointer(&r)), align|0x4|0x20|0x800|0x8000)
	selectObject.Call(hdc, previous)
}
