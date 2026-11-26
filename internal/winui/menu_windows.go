package winui

import (
	"image"
	"strings"
	"unsafe"

	"gioui.org/gpu/headless"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/unit"
	"quailcrate/internal/gioui"
	apptheme "quailcrate/internal/theme"
)

type popupMenu struct {
	hwnd           uintptr
	frame          widgetFrame
	items          []gioui.MenuItem
	viewport       gioui.MenuViewport
	scroll         int
	wheelRemainder int
	dpi            int
	palette        apptheme.Palette
	hovered        int
	hasHover       bool
	trackingLeave  bool
	closing        bool
	tray           bool
	onSelect       func(string)
}

func (u *UI) showContextMenu(anchor point, pal apptheme.Palette, items []gioui.MenuItem, onSelect func(string)) {
	u.closeContextMenu("")
	if len(items) == 0 {
		return
	}
	mon := desktopMonitorAt(anchor)
	dpi := int(mon.dpi)
	if dpi < 96 {
		dpi = 96
	}
	work := mon.info.Work
	if work.height() <= 0 || work.width() <= 0 {
		u.fallbackNativeMenu(anchor, items, onSelect)
		return
	}
	viewport := gioui.NewMenuViewport(items, dpi, int(work.height()))
	size := viewport.Size
	pad := int32(10 * dpi / 96)
	x := anchor.X - pad
	y := anchor.Y - pad
	if x+int32(size.X) > work.Right {
		x = anchor.X - int32(size.X) + pad
	}
	if y+int32(size.Y) > work.Bottom {
		y = anchor.Y - int32(size.Y) + pad
	}
	x = max(work.Left, min(x, work.Right-int32(size.X)))
	y = max(work.Top, min(y, work.Bottom-int32(size.Y)))

	h, err := window(menuClass, "QuailCrateMenu", wsExToolWindow|wsExTopmost|wsExLayered, wsPopup,
		rect{x, y, x + int32(size.X), y + int32(size.Y)}, 0, 0, u.module)
	if err != nil {
		u.fallbackNativeMenu(anchor, items, onSelect)
		return
	}
	m := &popupMenu{
		hwnd:     h,
		items:    items,
		viewport: viewport,
		dpi:      dpi,
		palette:  pal,
		hovered:  -1,
		onSelect: onSelect,
	}
	u.menu = m
	if !u.presentContextMenu() {
		u.closeContextMenu("")
		u.fallbackNativeMenu(anchor, items, onSelect)
		return
	}
	showWindow.Call(h, swShow)
	setForegroundWindow.Call(h)
	setFocus.Call(h)
}

func (u *UI) presentContextMenu() bool {
	m := u.menu
	if m == nil || m.hwnd == 0 {
		return false
	}
	size := m.viewport.Size
	if size.X < 1 || size.Y < 1 {
		return false
	}
	r := &u.widgetRenderer
	var target image.Point
	if r.window != nil {
		target = r.window.Size()
	}
	if r.window == nil || size.X > target.X || size.Y > target.Y {
		if r.window != nil {
			r.window.Release()
			r.window = nil
		}
		var err error
		r.window, err = headless.NewWindow(max(size.X, target.X), max(size.Y, target.Y))
		if err != nil {
			return false
		}
	}
	if r.shaper == nil {
		r.shaper = gioui.NewTextShaper()
	}
	r.ops.Reset()
	scale := float32(m.dpi) / 96
	gtx := layout.Context{
		Ops:         &r.ops,
		Constraints: layout.Exact(size),
		Metric:      unit.Metric{PxPerDp: scale, PxPerSp: scale},
		Locale:      system.Locale{Language: u.locale},
	}
	opt := gioui.MenuOptions{
		DPI:             m.dpi,
		Locale:          u.locale,
		Palette:         m.palette,
		HasHover:        m.hasHover,
		Hovered:         m.hovered,
		Viewport:        m.viewport,
		Scroll:          m.scroll,
		CornerRadius:    u.appearance.Normalize().EffectiveCornerRadius(),
		HasCornerRadius: true,
	}
	gioui.LayoutMenu(&gtx, r.shaper, m.items, opt)
	if err := r.window.Frame(&r.ops); err != nil {
		return false
	}
	if m.frame.image == nil || m.frame.image.Rect.Size() != size {
		m.frame.image = image.NewRGBA(image.Rectangle{Max: size})
	}
	if err := r.window.Screenshot(m.frame.image); err != nil {
		return false
	}
	return presentLayeredBitmap(m.hwnd, &m.frame)
}

func (u *UI) closeContextMenu(selectedID string) {
	m := u.menu
	if m == nil || m.closing {
		return
	}
	m.closing = true
	u.menu = nil
	cb := m.onSelect
	m.frame.release()
	if m.hwnd != 0 {
		destroyWindow.Call(m.hwnd)
	}
	if selectedID != "" && cb != nil {
		cb(selectedID)
	}
}

func (u *UI) scrollMenuBy(rows int, pt point) {
	m := u.menu
	scroll := max(0, min(m.viewport.MaxScroll, m.scroll+rows*m.viewport.RowHeight))
	if scroll != m.scroll {
		m.scroll = scroll
		m.hovered = m.viewport.HitIndex(image.Pt(int(pt.X), int(pt.Y)), scroll)
		m.hasHover = m.hovered >= 0
		u.presentContextMenu()
	}
}

func (u *UI) stepMenuSelection(delta int) {
	m := u.menu
	if m == nil || len(m.items) == 0 {
		return
	}
	idx := m.hovered
	if !m.hasHover {
		if delta > 0 {
			idx = -1
		} else {
			idx = len(m.items)
		}
	}
	for range m.items {
		idx = (idx + delta + len(m.items)) % len(m.items)
		if !m.items[idx].Separator && !m.items[idx].Disabled {
			m.hasHover = true
			m.hovered = idx
			m.scroll = m.viewport.Reveal(idx, m.scroll)
			u.presentContextMenu()
			return
		}
	}
}

func (u *UI) menuProcedure(msg, wParam, lParam uintptr) (uintptr, bool) {
	m := u.menu
	if m == nil {
		return 0, false
	}
	switch msg {
	case wmEraseBkgnd:
		return 1, true
	case wmPaint:
		var ps paintStruct
		beginPaint.Call(m.hwnd, uintptr(unsafe.Pointer(&ps)))
		endPaint.Call(m.hwnd, uintptr(unsafe.Pointer(&ps)))
	case wmActivate:
		if low(wParam) == 0 {
			u.closeContextMenu("")
		}
	case wmClose:
		u.closeContextMenu("")
	case wmMouseMove:
		if !m.trackingLeave {
			tme := trackMouseEventInfo{Size: uint32(unsafe.Sizeof(trackMouseEventInfo{})), Flags: tmeLeave, HWnd: m.hwnd}
			if ok, _, _ := trackMouseEvent.Call(uintptr(unsafe.Pointer(&tme))); ok != 0 {
				m.trackingLeave = true
			}
		}
		pt := mousePoint(lParam)
		idx := m.viewport.HitIndex(image.Pt(int(pt.X), int(pt.Y)), m.scroll)
		changed := (idx >= 0) != m.hasHover || (idx >= 0 && m.hovered != idx)
		if changed {
			m.hasHover = idx >= 0
			m.hovered = idx
			u.presentContextMenu()
		}
	case wmMouseLeave:
		m.trackingLeave = false
		if m.hasHover {
			m.hasHover = false
			m.hovered = -1
			u.presentContextMenu()
		}
	case wmMouseWheel:
		const wheelDelta = 120
		m.wheelRemainder += int(int16(wParam >> 16))
		rows := -m.wheelRemainder / wheelDelta
		m.wheelRemainder %= wheelDelta
		if rows != 0 && m.viewport.MaxScroll > 0 {
			pt := mousePoint(lParam)
			screenToClient.Call(m.hwnd, uintptr(unsafe.Pointer(&pt)))
			u.scrollMenuBy(rows, pt)
		}
	case wmLButtonUp:
		pt := mousePoint(lParam)
		p := image.Pt(int(pt.X), int(pt.Y))
		switch {
		case p.In(m.viewport.Up):
			u.scrollMenuBy(-1, pt)
		case p.In(m.viewport.Down):
			u.scrollMenuBy(1, pt)
		default:
			idx := m.viewport.HitIndex(p, m.scroll)
			if idx >= 0 {
				u.closeContextMenu(m.items[idx].ID)
			} else {
				u.closeContextMenu("")
			}
		}
	case wmRButtonUp:
		u.closeContextMenu("")
	case wmKeyDown:
		switch wParam {
		case vkEscape:
			u.closeContextMenu("")
		case vkUp:
			u.stepMenuSelection(-1)
		case vkDown:
			u.stepMenuSelection(1)
		case vkReturn, vkSpace:
			if m.hasHover && m.hovered >= 0 && m.hovered < len(m.items) {
				u.closeContextMenu(m.items[m.hovered].ID)
			}
		}
	default:
		return 0, false
	}
	return 0, true
}

func (u *UI) fallbackNativeMenu(anchor point, items []gioui.MenuItem, onSelect func(string)) {
	menu, _, _ := createPopupMenu.Call()
	if menu == 0 {
		return
	}
	defer destroyMenu.Call(menu)
	const mfSeparator = 0x0800
	byCmd := make(map[uintptr]string)
	next := uintptr(1)
	for _, it := range items {
		if it.Separator {
			appendMenu.Call(menu, mfSeparator, 0, 0)
			continue
		}
		label := strings.ReplaceAll(it.Label, "&", "&&")
		if it.Shortcut != "" {
			label += "\t" + it.Shortcut
		}
		var flags uintptr
		if it.Checked {
			flags |= 8 // MF_CHECKED
		}
		if it.Disabled {
			flags |= 1 // MF_GRAYED
		}
		appendMenu.Call(menu, flags, next, uintptr(unsafe.Pointer(wstr(label))))
		byCmd[next] = it.ID
		next++
	}
	setForegroundWindow.Call(u.host)
	cmd, _, _ := trackPopupMenu.Call(menu, 0x102, coord(anchor.X), coord(anchor.Y), 0, u.host, 0)
	postMessage.Call(u.host, 0, 0, 0)
	if id, ok := byCmd[cmd]; ok && onSelect != nil {
		onSelect(id)
	}
}
