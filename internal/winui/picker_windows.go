package winui

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	v1 "quailcrate/pluginapi/v1"
)

type optionPicker struct {
	ctx                                              context.Context
	hwnd, owner, edit, font, editProc                uintptr
	brush, brushColor                                uintptr
	title, query                                     string
	options                                          []v1.Option
	filtered                                         []int
	selected                                         map[string]bool
	multiple                                         bool
	cursor, scroll                                   int
	wheelRemainder                                   int
	frame                                            dibSurface
	chrome                                           popupFrame
	dragging                                         bool
	dragStartMouse, dragStartWin                     point
	editFocused                                      bool
	rowIcons                                         [pickerMaxRows]cachedIconBitmap
	titleFont, rowFont, boldFont, subFont, glyphFont uintptr
	dpi                                              int32
	hoverBtn                                         int // 0=none, 1=ok, 2=cancel, 3=close
	hoverRow                                         int
	trackingLeave                                    bool
	headerRect, listRect                             rect
	okRect, cancelRect, closeRect                    rect
	done                                             func([]string, bool)
	finish                                           func()
}

var pickerEditCallback = syscall.NewCallback(pickerEditProcedure)

const (
	pickerEditID   = 320
	pickerRowH     = 40
	pickerMaxRows  = 7
	pickerBtnOK    = 1
	pickerBtnCan   = 2
	pickerBtnClose = 3
)

// PickOptions opens a themed searchable single- or multi-select modal dialog (used for
// adding installed apps/games, picking a project opener tool, or moving items across tabs/panels).
func (u *UI) PickOptions(title string, options []v1.Option, multiple bool, done func([]string, bool)) {
	u.PickOptionsFor(nil, 0, title, options, multiple, done)
}

func (u *UI) PickOptionsFor(ctx context.Context, requestedOwner uintptr, title string, options []v1.Option, multiple bool, done func([]string, bool)) {
	var opened *optionPicker
	cancel := func() {
		if opened != nil && u.picker == opened {
			u.closePicker(false)
		} else if done != nil {
			done(nil, false)
		}
	}
	u.postDialogFor(ctx, cancel, func(finish func()) {
		if requestedOwner != 0 && !validWindow(requestedOwner) {
			cancel()
			finish()
			return
		}
		owner := u.resolveDialogOwner(requestedOwner)
		var m monitorInfo
		if owner != 0 && owner != u.host {
			m = monitorOf(owner)
		} else {
			fore, _, _ := getForegroundWindow.Call()
			m = monitorOf(fore)
		}
		if m.Work.width() == 0 {
			m = monitorOfPoint(cursorPosition())
		}
		dpi := dpiForWindow(owner)
		baseW := int32(476)
		visibleRows := max(3, min(pickerMaxRows, len(options)))
		baseH := int32(108 + visibleRows*pickerRowH + 56)
		width, height := scale(baseW, dpi), min(scale(baseH, dpi), m.Work.height()-scale(40, dpi))
		left := m.Work.Left + max(0, (m.Work.width()-width)/2)
		top := m.Work.Top + max(0, (m.Work.height()-height)/2)

		h, err := window(pickerClass, title, wsExControlParent|wsExToolWindow, wsPopup|wsClipChildren, rect{left, top, left + width, top + height}, owner, 0, u.module)
		if err != nil {
			if done != nil {
				done(nil, false)
			}
			finish()
			return
		}
		_, isDark := u.promptPalette()
		darkVal := uint32(0)
		if isDark {
			darkVal = 1
		}
		settingsDwmAttribute.Call(h, 20, uintptr(unsafe.Pointer(&darkVal)), 4)

		dpi = dpiForWindow(h)
		px := func(v int32) int32 { return scale(v, dpi) }
		edit, err := window("EDIT", "", 0, wsTabStop|wsChild|wsVisible|0x0080,
			rect{px(44), px(62), width - px(28), px(86)}, h, pickerEditID, u.module)
		if err != nil {
			destroyWindow.Call(h)
			if done != nil {
				done(nil, false)
			}
			finish()
			return
		}
		f := fontFace(dpi, 14, false, "Cascadia Mono")
		sendMessage.Call(edit, wmSetFont, f, 1)
		sendMessage.Call(edit, 0x00c5, 256, 0)
		sendMessage.Call(edit, 0x1501, 0, uintptr(unsafe.Pointer(wstr(u.label("输入关键词筛选…", "Type to filter…")))))
		editProc, _, _ := setWindowLong.Call(edit, ^uintptr(3), pickerEditCallback)

		sel := make(map[string]bool)
		for _, opt := range options {
			if opt.Selected {
				sel[opt.ID] = true
			}
		}
		p := &optionPicker{
			ctx:  ctx,
			hwnd: h, owner: owner, edit: edit, font: f, editProc: editProc,
			title: title, options: options, selected: sel, multiple: multiple,
			dpi: dpi, hoverRow: -1, editFocused: true, done: done, finish: finish,
		}
		p.chrome.attach(h, u.module, false)
		p.refilter()
		p.titleFont = font(dpi, 14, true)
		p.rowFont = font(dpi, 13, false)
		p.boldFont = font(dpi, 13, true)
		p.subFont = fontFace(dpi, 11, false, "Cascadia Mono")
		p.glyphFont = fontFace(dpi, 15, true, "Cascadia Mono")
		opened = p
		u.picker = p
		u.layoutPickerRects()
		u.syncPickerChrome()
		if validWindow(owner) {
			enableWindow.Call(owner, 0)
		}
		showWindow.Call(h, swShow)
		p.chrome.show(true)
		setForegroundWindow.Call(h)
		setFocus.Call(edit)
		repaint(h)
	})
}

func (p *optionPicker) refilter() {
	q := strings.ToLower(strings.TrimSpace(p.query))
	p.filtered = p.filtered[:0]
	for i, opt := range p.options {
		if q == "" ||
			strings.Contains(strings.ToLower(opt.Text), q) ||
			strings.Contains(strings.ToLower(opt.Subtitle), q) ||
			strings.Contains(strings.ToLower(opt.ID), q) {
			p.filtered = append(p.filtered, i)
		}
	}
	if len(p.filtered) == 0 {
		p.cursor, p.scroll = 0, 0
		return
	}
	p.cursor = max(0, min(p.cursor, len(p.filtered)-1))
	p.scroll = max(0, min(p.scroll, max(0, len(p.filtered)-p.visibleRows())))
}

func (p *optionPicker) visibleRows() int {
	rowH := max(int32(1), scale(pickerRowH, p.dpi))
	rows := int(p.listRect.height() / rowH)
	if rows < 1 {
		return pickerMaxRows
	}
	return rows
}

func (u *UI) layoutPickerRects() {
	p := u.picker
	if p == nil {
		return
	}
	r := clientRect(p.hwnd)
	px := func(v int32) int32 { return scale(v, p.dpi) }
	p.headerRect = rect{0, 0, r.Right, px(44)}
	p.closeRect = rect{r.Right - px(38), px(8), r.Right - px(10), px(36)}
	btnW, btnH := px(92), px(34)
	btnY := r.Bottom - px(48)
	p.cancelRect = rect{r.Right - px(18) - btnW, btnY, r.Right - px(18), btnY + btnH}
	p.okRect = rect{p.cancelRect.Left - px(10) - btnW, btnY, p.cancelRect.Left - px(10), btnY + btnH}
	p.listRect = rect{px(18), px(102), r.Right - px(18), btnY - px(12)}
	move(p.edit, rect{px(44), px(62), r.Right - px(28), px(86)})
}

func (u *UI) syncPickerChrome() {
	p := u.picker
	if p == nil || p.hwnd == 0 {
		return
	}
	pal, isDark := u.promptPalette()
	p.chrome.sync(p.hwnd, p.dpi, int32(u.popupCornerRadius()), pal.border, isDark, isSystemHighContrast(), 255)
}

func pickerEditProcedure(hwnd, msg, wParam, lParam uintptr) uintptr {
	u := activeUI
	if u == nil || u.picker == nil {
		result, _, _ := defWindowProc.Call(hwnd, msg, wParam, lParam)
		return result
	}
	p := u.picker
	if msg == wmKeyDown && !imeComposing(hwnd) {
		switch wParam {
		case vkUp:
			u.stepPicker(-1)
			return 0
		case vkDown:
			u.stepPicker(1)
			return 0
		case vkReturn:
			if !p.multiple {
				u.acceptCurrentPickerRow()
			} else {
				u.closePicker(true)
			}
			return 0
		case vkEscape:
			u.closePicker(false)
			return 0
		}
	}
	if msg == wmChar && (wParam == vkReturn || wParam == vkEscape) && !imeComposing(hwnd) {
		return 0
	}
	result, _, _ := callWindowProc.Call(p.editProc, hwnd, msg, wParam, lParam)
	return result
}

func (u *UI) stepPicker(delta int) {
	p := u.picker
	if p == nil || len(p.filtered) == 0 {
		return
	}
	p.cursor = max(0, min(p.cursor+delta, len(p.filtered)-1))
	rows := p.visibleRows()
	if p.cursor < p.scroll {
		p.scroll = p.cursor
	}
	if p.cursor >= p.scroll+rows {
		p.scroll = p.cursor - rows + 1
	}
	p.repaintList()
}

func (u *UI) acceptCurrentPickerRow() {
	p := u.picker
	if p == nil {
		return
	}
	if p.cursor >= 0 && p.cursor < len(p.filtered) {
		opt := p.options[p.filtered[p.cursor]]
		p.selected = map[string]bool{opt.ID: true}
		u.closePicker(true)
		return
	}
	u.closePicker(false)
}

func (u *UI) pickerEditBrush(dc uintptr) uintptr {
	p := u.picker
	pal, _ := u.promptPalette()
	if p.brush == 0 || p.brushColor != pal.background {
		if p.brush != 0 {
			deleteObject.Call(p.brush)
		}
		p.brush, _, _ = createSolidBrush.Call(pal.background)
		p.brushColor = pal.background
	}
	settingsSetBkColor.Call(dc, pal.background)
	setTextColor.Call(dc, pal.text)
	return p.brush
}

func (u *UI) pickerRowAt(pt point) int {
	p := u.picker
	if p == nil || !p.listRect.contains(pt) {
		return -1
	}
	rowH := max(int32(1), scale(pickerRowH, p.dpi))
	idx := p.scroll + int((pt.Y-p.listRect.Top)/rowH)
	if idx >= min(len(p.filtered), p.scroll+p.visibleRows()) {
		return -1
	}
	return idx
}

func (p *optionPicker) itemRects(index int) (row, icon rect) {
	px := func(v int32) int32 { return scale(v, p.dpi) }
	rowH := px(pickerRowH)
	y := p.listRect.Top + int32(index-p.scroll)*rowH
	row = rect{p.listRect.Left + px(4), y + px(2), p.listRect.Right - px(4), min(y+rowH-px(2), p.listRect.Bottom-px(2))}
	iconX := row.Left + px(10)
	if p.multiple {
		iconX += px(16) + px(8)
	}
	size := px(24)
	iconY := (row.Top + row.Bottom - size) / 2
	return row, rect{iconX, iconY, iconX + size, iconY + size}
}

func (p *optionPicker) repaintList() {
	invalidateRect.Call(p.hwnd, uintptr(unsafe.Pointer(&p.listRect)), 0)
}

func (p *optionPicker) setHover(btn, row int) {
	if btn != p.hoverBtn {
		repaint(p.hwnd)
	} else if row != p.hoverRow {
		for _, index := range [...]int{row, p.hoverRow} {
			if index >= p.scroll && index < p.scroll+p.visibleRows() {
				box, _ := p.itemRects(index)
				invalidateRect.Call(p.hwnd, uintptr(unsafe.Pointer(&box)), 0)
			}
		}
	}
	p.hoverBtn, p.hoverRow = btn, row
}

func (u *UI) pickerProcedure(msg, wParam, lParam uintptr) (uintptr, bool) {
	p := u.picker
	if p == nil {
		return 0, false
	}
	switch msg {
	case wmEraseBkgnd:
		return 1, true
	case wmCtlColorEdit:
		return u.pickerEditBrush(wParam), true
	case wmSize:
		u.layoutPickerRects()
		u.syncPickerChrome()
	case wmWindowPosChanged:
		u.syncPickerChrome()
		return 0, false
	case wmPaint:
		u.paintPicker()
	case wmClose:
		u.closePicker(false)
	case wmCommand:
		if low(wParam) == pickerEditID {
			switch high(wParam) {
			case 0x0300: // EN_CHANGE
				q := currentText(p.edit)
				if q != p.query {
					p.query = q
					p.refilter()
					p.repaintList()
				}
				return 0, true
			case 0x0100: // EN_SETFOCUS
				if !p.editFocused {
					p.editFocused = true
					repaint(p.hwnd)
				}
				return 0, true
			case 0x0200: // EN_KILLFOCUS
				if p.editFocused {
					p.editFocused = false
					repaint(p.hwnd)
				}
				return 0, true
			}
		}
		return 0, false
	case wmMouseWheel:
		const wheelDelta = 120
		p.wheelRemainder += int(int16(wParam >> 16))
		steps := p.wheelRemainder / wheelDelta
		p.wheelRemainder %= wheelDelta
		scroll := max(0, min(p.scroll-steps, max(0, len(p.filtered)-p.visibleRows())))
		if scroll != p.scroll {
			p.scroll = scroll
			pt := mousePoint(lParam)
			screenToClient.Call(p.hwnd, uintptr(unsafe.Pointer(&pt)))
			p.hoverRow = u.pickerRowAt(pt)
			p.repaintList()
		}
	case wmMouseMove:
		if p.dragging {
			cur := cursorPosition()
			p.chrome.moveWith(p.hwnd, p.dragStartWin.X+(cur.X-p.dragStartMouse.X), p.dragStartWin.Y+(cur.Y-p.dragStartMouse.Y))
			return 0, true
		}
		if !p.trackingLeave {
			tme := trackMouseEventInfo{Size: uint32(unsafe.Sizeof(trackMouseEventInfo{})), Flags: tmeLeave, HWnd: p.hwnd}
			if ok, _, _ := trackMouseEvent.Call(uintptr(unsafe.Pointer(&tme))); ok != 0 {
				p.trackingLeave = true
			}
		}
		pt := mousePoint(lParam)
		btn := 0
		switch {
		case p.okRect.contains(pt):
			btn = pickerBtnOK
		case p.cancelRect.contains(pt):
			btn = pickerBtnCan
		case p.closeRect.contains(pt):
			btn = pickerBtnClose
		}
		p.setHover(btn, u.pickerRowAt(pt))
	case wmMouseLeave:
		p.trackingLeave = false
		p.setHover(0, -1)
	case wmCaptureChanged:
		p.dragging = false
	case wmLButtonDown:
		pt := mousePoint(lParam)
		if p.headerRect.contains(pt) && !p.closeRect.contains(pt) {
			p.dragging = true
			p.dragStartMouse = cursorPosition()
			wr := windowRect(p.hwnd)
			p.dragStartWin = point{wr.Left, wr.Top}
			setCapture.Call(p.hwnd)
			return 0, true
		}
	case wmLButtonUp:
		if p.dragging {
			p.dragging = false
			releaseCapture.Call()
			return 0, true
		}
		pt := mousePoint(lParam)
		switch {
		case p.okRect.contains(pt):
			if !p.multiple && len(p.selected) == 0 {
				u.acceptCurrentPickerRow()
			} else {
				u.closePicker(true)
			}
		case p.cancelRect.contains(pt), p.closeRect.contains(pt):
			u.closePicker(false)
		default:
			if row := u.pickerRowAt(pt); row >= 0 {
				p.cursor = row
				opt := p.options[p.filtered[row]]
				if p.multiple {
					if p.selected[opt.ID] {
						delete(p.selected, opt.ID)
					} else {
						p.selected[opt.ID] = true
					}
					repaint(p.hwnd)
				} else {
					p.selected = map[string]bool{opt.ID: true}
					u.closePicker(true)
				}
			}
		}
	default:
		return 0, false
	}
	return 0, true
}

func (u *UI) paintPicker() {
	p := u.picker
	if p == nil {
		return
	}
	p.frame.paint(p.hwnd, u.drawPicker)
}

func (u *UI) drawPicker(hdc uintptr, r, dirty rect) {
	p := u.picker
	pal, _ := u.promptPalette()
	px := func(v int32) int32 { return scale(v, p.dpi) }
	border := max(int32(1), px(1))
	rad := u.popupCornerRadius()
	hasChrome := p.chrome.hasChrome()
	pillDiam := scaleCornerDiam(rad, 2, p.dpi)

	listOnly := dirty.Left >= p.listRect.Left && dirty.Top >= p.listRect.Top && dirty.Right <= p.listRect.Right && dirty.Bottom <= p.listRect.Bottom
	if !listOnly {
		headerH := px(44)
		if hasChrome {
			fill(hdc, r, pal.surface)
			fill(hdc, rect{0, 0, r.Right, headerH}, pal.sidebar)
			fill(hdc, rect{0, headerH, r.Right, headerH + border}, pal.border)
		} else {
			fill(hdc, r, pal.border)
			fill(hdc, rect{border, border, r.Right - border, r.Bottom - border}, pal.surface)
			fill(hdc, rect{border, border, r.Right - border, headerH}, pal.sidebar)
			fill(hdc, rect{border, headerH, r.Right - border, headerH + border}, pal.border)
		}
		titleX := px(18)
		if u.appearance.Normalize().TitleAccentBar {
			settingsRoundFill(hdc, rect{px(16), px(14), px(19), headerH - px(14)}, pillDiam, pal.accent)
			titleX = px(28)
		}

		text(hdc, rect{titleX, border, p.closeRect.Left - px(8), headerH}, p.title, p.titleFont, pal.text, 0)
		closeInk := pal.secondary
		if p.hoverBtn == pickerBtnClose {
			settingsRoundFill(hdc, p.closeRect, scaleCornerDiam(rad, 6, p.dpi), pal.selected)
			closeInk = pal.warning
		}
		text(hdc, p.closeRect, "×", p.glyphFont, closeInk, 1)

		// Search dock with clean concentric rounded outline (no sharp bottom bar clash).
		dock := rect{px(18), px(54), r.Right - px(18), px(92)}
		dockDiam := scaleCornerDiam(rad, 8, p.dpi)
		dockInnerDiam := max(int32(0), dockDiam-2*border)
		dockBorder := pal.border
		if p.editFocused {
			dockBorder = pal.accent
		}
		settingsRoundFill(hdc, dock, dockDiam, dockBorder)
		settingsRoundFill(hdc, settingsInset(dock, border), dockInnerDiam, pal.background)
		text(hdc, rect{dock.Left + px(10), dock.Top, dock.Left + px(24), dock.Bottom}, ">", p.glyphFont, pal.accent, 1)
	}

	// List frame
	// Keep cached rows outside the painted slice intact for a later partial blit.
	listDiam := scaleCornerDiam(rad, 8, p.dpi)
	listInnerDiam := max(int32(0), listDiam-2*border)
	if listOnly && dirty != p.listRect {
		fill(hdc, dirty, pal.background)
	} else {
		settingsRoundFill(hdc, p.listRect, listDiam, pal.border)
		settingsRoundFill(hdc, settingsInset(p.listRect, border), listInnerDiam, pal.background)
	}
	subFont := p.subFont

	if len(p.filtered) == 0 {
		text(hdc, p.listRect, u.label("没有匹配项", "No matching items"), subFont, pal.secondary, 1)
	} else {
		rows := p.visibleRows()
		end := min(len(p.filtered), p.scroll+rows)
		rowDiam := scaleCornerDiam(rad, 6, p.dpi)
		boxDiam := scaleCornerDiam(rad, 4, p.dpi)
		boxInnerDiam := max(int32(0), boxDiam-2*border)
		for i := p.scroll; i < end; i++ {
			opt := p.options[p.filtered[i]]
			row, icon := p.itemRects(i)
			if listOnly && !row.intersects(dirty) {
				continue
			}
			isSel := p.selected[opt.ID]
			isCur := i == p.cursor || i == p.hoverRow
			if isSel || isCur {
				bg := pal.hover
				if isSel {
					bg = pal.selected
				}
				settingsRoundFill(hdc, row, rowDiam, bg)
				if isSel {
					pill := rect{row.Left + px(3), row.Top + px(7), row.Left + px(6), row.Bottom - px(7)}
					settingsRoundFill(hdc, pill, pillDiam, pal.accent)
				}
			}
			curX := row.Left + px(10)
			if p.multiple {
				boxSize := px(16)
				boxY := (row.Top + row.Bottom - boxSize) / 2
				box := rect{curX, boxY, curX + boxSize, boxY + boxSize}
				if isSel {
					settingsRoundFill(hdc, box, boxDiam, pal.accent)
					text(hdc, box, "✓", subFont, pal.white, 1)
				} else {
					settingsRoundFill(hdc, box, boxDiam, pal.border)
					settingsRoundFill(hdc, settingsInset(box, border), boxInnerDiam, pal.surface)
				}
				curX = box.Right + px(8)
			}
			if opt.Icon != "" {
				if img := u.launcherIcon(opt.Icon); img != nil {
					p.rowIcons[i%len(p.rowIcons)].draw(hdc, icon, img)
				} else if v1.IsIconName(opt.Icon) {
					text(hdc, icon, symbolGlyph(opt.Icon), p.rowFont, pal.accent, 1)
				}
				curX = icon.Right + px(8)
			}
			titleR := rect{curX, row.Top + px(2), row.Right - px(10), row.Bottom - px(2)}
			if opt.Subtitle != "" {
				titleR.Bottom = row.Top + px(20)
				subR := rect{curX, row.Top + px(19), row.Right - px(10), row.Bottom - px(2)}
				text(hdc, subR, opt.Subtitle, subFont, pal.secondary, 0)
			}
			tf := p.rowFont
			if isSel {
				tf = p.boldFont
			}
			text(hdc, titleR, opt.Text, tf, pal.text, 0)
		}
	}

	if listOnly {
		return
	}
	btnFont := p.boldFont
	okLabel := u.label("确定", "OK")
	if p.multiple && len(p.selected) > 0 {
		okLabel = fmt.Sprintf("%s (%d)", u.label("添加", "Add"), len(p.selected))
	}
	hint := u.label("↑↓ 选择  ·  ↵ 确认  ·  ESC 取消", "↑↓ Move  ·  ↵ Confirm  ·  ESC Cancel")
	text(hdc, rect{px(18), p.okRect.Top, p.okRect.Left - px(10), p.okRect.Bottom}, hint, subFont, pal.secondary, 0)

	btnDiam := scaleCornerDiam(rad, 6, p.dpi)
	btnInnerDiam := max(int32(0), btnDiam-2*border)
	if p.hoverBtn == pickerBtnOK {
		settingsRoundFill(hdc, p.okRect, btnDiam, pal.accent)
		settingsRoundFill(hdc, settingsInset(p.okRect, border), btnInnerDiam, pal.selected)
		text(hdc, p.okRect, okLabel, btnFont, pal.accent, 1)
	} else {
		settingsRoundFill(hdc, p.okRect, btnDiam, pal.accent)
		text(hdc, p.okRect, okLabel, btnFont, pal.white, 1)
	}
	cBorder, cBg, cInk := pal.border, pal.background, pal.text
	if p.hoverBtn == pickerBtnCan {
		cBorder, cBg, cInk = pal.accent, pal.selected, pal.accent
	}
	settingsRoundFill(hdc, p.cancelRect, btnDiam, cBorder)
	settingsRoundFill(hdc, settingsInset(p.cancelRect, border), btnInnerDiam, cBg)
	text(hdc, p.cancelRect, u.label("取消", "Cancel"), btnFont, cInk, 1)
}

func (u *UI) closePicker(accept bool) {
	p := u.picker
	if p == nil {
		return
	}
	var out []string
	if p.ctx != nil && p.ctx.Err() != nil {
		accept = false
	}
	if accept {
		for _, opt := range p.options {
			if p.selected[opt.ID] {
				out = append(out, opt.ID)
			}
		}
		if len(out) == 0 {
			accept = false
		}
	}
	ownerAlive := validWindow(p.owner)
	if ownerAlive {
		enableWindow.Call(p.owner, 1)
	}
	p.chrome.release()
	destroyWindow.Call(p.hwnd)
	p.frame.release()
	for i := range p.rowIcons {
		p.rowIcons[i].release()
	}
	for _, f := range [...]uintptr{p.titleFont, p.rowFont, p.boldFont, p.subFont, p.glyphFont} {
		if f != 0 {
			deleteObject.Call(f)
		}
	}
	u.picker = nil
	if p.font != 0 {
		deleteObject.Call(p.font)
	}
	if p.brush != 0 {
		deleteObject.Call(p.brush)
	}
	if ownerAlive && p.owner != u.host {
		setForegroundWindow.Call(p.owner)
	}
	if p.done != nil {
		p.done(out, accept)
	}
	p.finish()
}
