package winui

import (
	"slices"
	"syscall"
	"time"
	"unsafe"

	"quailcrate/internal/gioui"
	apptheme "quailcrate/internal/theme"
	v1 "quailcrate/pluginapi/v1"
)

func (u *UI) applyLauncherAppearance() {
	if u.launcher == 0 {
		return
	}
	f := &u.launcherFrame
	f.highContrast = isSystemHighContrast()
	res := u.appearance.Resolve(apptheme.ComponentLauncher)
	opacity := 100
	f.isDark = res.IsDark
	f.cornerRadius = res.CornerRadius
	if f.highContrast {
		f.colors = highContrastPalette()
		f.isDark = false
	} else {
		f.colors, opacity = paletteFromTheme(res.Palette), res.Opacity
	}
	alpha := byte(max(89, opacity*255/100))
	f.opacity = alpha
	setLayeredWindowAttributes.Call(u.launcher, 0, uintptr(alpha), lwaAlpha)
	u.syncLauncherChrome()
	repaint(u.launcher)
	if u.edit != 0 {
		repaint(u.edit)
	}
}

func (u *UI) syncLauncherChrome() {
	if u.launcher == 0 {
		return
	}
	f := &u.launcherFrame
	dpi := u.launcherDPI
	if dpi == 0 {
		dpi = dpiForWindow(u.launcher)
	}
	opacity := f.opacity
	if opacity == 0 {
		opacity = 255
	}
	u.launcherChrome.sync(u.launcher, dpi, int32(u.launcherCornerRadius()), f.colors.border, f.isDark, f.highContrast, opacity)
}

func (u *UI) launcherCornerRadius() int {
	if u.launcherChrome.hasChrome() {
		return u.launcherFrame.cornerRadius
	}
	return u.appearance.Normalize().EffectiveCornerRadius()
}

func (u *UI) ensureLauncher() error {
	if u.launcher != 0 {
		return nil
	}
	h, err := window(launcherClass, u.launcherShell.View.Title, wsExToolWindow|wsExTopmost|wsExLayered, wsPopup|wsClipChildren, rect{Right: 1, Bottom: 1}, 0, 0, u.module)
	if err != nil {
		return err
	}
	u.launcherChrome.attach(h, u.module, true)
	u.launcher = h
	// Borderless child EDIT seamlessly styled via WM_CTLCOLOREDIT (preserves native IME).
	e, err := window("EDIT", "", 0, wsChild|wsVisible|wsTabStop|0x0080, rect{Right: 1, Bottom: 1}, h, 100, u.module)
	if err != nil {
		u.launcherChrome.release()
		destroyWindow.Call(h)
		u.launcher = 0
		return err
	}
	u.edit = e
	sendMessage.Call(e, 0x00c5, 256, 0)
	u.applyLauncherAppearance()
	u.editProc, _, _ = setWindowLong.Call(e, ^uintptr(3), syscall.NewCallback(launcherEditProcedure))
	return nil
}

func (u *UI) refreshLauncherLabels() {
	if u.launcherShell.View == nil || u.launcherLayout.input.node == nil {
		return
	}
	setText(u.launcher, u.launcherShell.View.Title)
	sendMessage.Call(u.edit, 0x1501, 0, uintptr(unsafe.Pointer(wstr(u.launcherLayout.input.node.Props.Label))))
	repaint(u.launcher)
}

func (u *UI) showLauncher() {
	if !u.launchersReady {
		u.launcherRequested = true
		return
	}
	if u.launcherShell.View == nil {
		u.setSettingsPageID(gioui.PagePlugins)
		u.showSettings()
		return
	}
	start := time.Now()
	if err := u.ensureLauncher(); err != nil {
		u.Message("Quick launcher failed", err.Error())
		return
	}
	previous, _, _ := getForegroundWindow.Call()
	if previous != u.launcher {
		u.prevActive = previous
	}
	m := monitorOf(u.prevActive)
	if m.Work.width() == 0 {
		m = monitorOfPoint(cursorPosition())
	}
	u.launcherQuery, u.launcherStatus = "", ""
	u.results = nil
	u.resultIndex, u.resultScroll, u.launcherWheelRemainder = 0, 0, 0
	setText(u.edit, "")

	// Move first so the initial font and dimensions match the destination monitor.
	move(u.launcher, rect{m.Work.Left, m.Work.Top, m.Work.Left + int32(u.launcherShell.View.Root.Props.Width), m.Work.Top + 96})
	u.sizeLauncher(true)
	u.refreshLauncherLabels()
	u.applyLauncherAppearance()
	u.launcherOpen = true
	showWindow.Call(u.launcher, swShow)
	u.syncLauncherChrome()
	u.launcherChrome.show(true)
	setForegroundWindow.Call(u.launcher)
	setFocus.Call(u.edit)
	u.notifyLauncherFocused(start)
	u.emitSearch()
}

func (u *UI) closeLauncher(restore bool) {
	if !u.launcherOpen {
		return
	}
	u.hideLauncher(restore)
	if u.cb.Search != nil {
		u.launcherRevision = u.cb.Search("")
	}
}

func (u *UI) hideLauncher(restore bool) {
	if !u.launcherOpen {
		return
	}
	u.launcherOpen = false
	u.results = nil
	u.launcherChrome.show(false)
	showWindow.Call(u.launcher, swHide)
	if restore && u.prevActive != 0 {
		if result, _, _ := isWindow.Call(u.prevActive); result != 0 {
			setForegroundWindow.Call(u.prevActive)
		}
	}
}

func (u *UI) sizeLauncher(center bool) {
	if u.launcher == 0 || u.edit == 0 || u.launcherShell.View == nil {
		return
	}
	current := windowRect(u.launcher)
	m := monitorOf(u.launcher)
	dpi := dpiForWindow(u.launcher)
	fontChanged := u.launcherDPI != dpi || u.launcherFont == 0
	if fontChanged {
		old := u.launcherFont
		u.launcherFont = fontFace(dpi, 16, false, "Cascadia Mono")
		u.launcherDPI = dpi
		sendMessage.Call(u.edit, wmSetFont, u.launcherFont, 1)
		if old != 0 {
			deleteObject.Call(old)
		}
	}
	root := u.launcherShell.View.Root
	width := min(scale(int32(root.Props.Width), dpi), m.Work.width())
	u.launcherLayout.prepare(root, width, m.Work.height(), dpi, len(u.results))
	rows := u.launcherLayout.visible
	u.resultScroll = min(u.resultScroll, max(0, len(u.results)-rows))
	u.resultScroll = max(u.resultIndex-rows+1, min(u.resultScroll, u.resultIndex))
	height := min(u.launcherLayout.height, m.Work.height())
	left, top := current.Left, current.Top
	if center {
		left = m.Work.Left + (m.Work.width()-width)/2
		top = m.Work.Top + m.Work.height()/7
	}
	left = max(m.Work.Left, min(left, m.Work.Right-width))
	top = max(m.Work.Top, min(top, m.Work.Bottom-height))
	r := rect{left, top, left + width, top + height}
	if r != current {
		move(u.launcher, r)
	}
	u.syncLauncherChrome()
	input := u.launcherLayout.input
	editLeft := int32(20)
	if input.node.Props.Text != "" {
		editLeft = 40
	}
	editTop := (input.box.Top + input.box.Bottom - scale(24, dpi)) / 2
	edit := rect{scale(editLeft, dpi), editTop, width - scale(20, dpi), editTop + scale(24, dpi)}
	old := windowRect(u.edit)
	if fontChanged || old.Left != left+edit.Left || old.Top != top+edit.Top || old.width() != edit.width() || old.height() != edit.height() {
		move(u.edit, edit)
	}
	repaint(u.launcher)
}

func (u *UI) SetResults(revision uint64, query string, results []Result, status string) {
	u.post(func() {
		if !u.launcherOpen || revision < u.launcherRevision || query != u.launcherQuery {
			return
		}
		u.launcherRevision = revision
		index := min(u.resultIndex, max(0, len(results)-1))
		if u.resultIndex < len(u.results) {
			prev := u.results[u.resultIndex]
			if next := slices.IndexFunc(results, func(r Result) bool {
				return r.ID == prev.ID && r.PluginID == prev.PluginID && r.Generation == prev.Generation
			}); next >= 0 {
				index = next
			}
		}
		u.results, u.launcherStatus = results, status
		u.resultIndex = index
		u.sizeLauncher(false)
	})
}

func (u *UI) launcherEditBrush(dc uintptr) uintptr {
	p := u.launcherFrame.colors
	if u.launcherBrush == 0 || u.launcherBrushColor != p.background {
		if u.launcherBrush != 0 {
			deleteObject.Call(u.launcherBrush)
		}
		u.launcherBrush, _, _ = createSolidBrush.Call(p.background)
		u.launcherBrushColor = p.background
	}
	settingsSetBkColor.Call(dc, p.background)
	setTextColor.Call(dc, p.text)
	return u.launcherBrush
}

func (u *UI) launcherProcedure(msg, wParam, lParam uintptr) (uintptr, bool) {
	switch msg {
	case wmEraseBkgnd:
		return 1, true
	case wmCtlColorEdit:
		return u.launcherEditBrush(wParam), true
	case wmClose:
		u.closeLauncher(true)
	case wmActivate:
		if low(wParam) == 0 {
			u.closeLauncher(false)
		}
	case wmCommand:
		if low(wParam) != 100 || high(wParam) != 0x0300 {
			return 0, false
		}
		if !imeComposing(u.edit) {
			u.updateLauncherQuery()
		}
	case wmLButtonUp, wmRButtonUp:
		p := mousePoint(lParam)
		i := u.resultAt(p)
		if msg == wmRButtonUp {
			u.resultMenu(i, cursorPosition())
		} else {
			u.openResult(i)
		}
	case wmMouseWheel:
		u.launcherWheelRemainder += int(int16(wParam >> 16))
		const wheelDelta = 120
		u.selectResult(-u.launcherWheelRemainder / wheelDelta)
		u.launcherWheelRemainder %= wheelDelta
	case wmDpiChanged:
		suggested := *nativePointer[rect](lParam)
		move(u.launcher, suggested)
		u.sizeLauncher(false)
	case wmWindowPosChanged:
		u.syncLauncherChrome()
		return 0, false
	case wmPaint:
		u.paintLauncher()
	default:
		return 0, false
	}
	return 0, true
}

func (u *UI) updateLauncherQuery() {
	query := currentText(u.edit)
	if query == u.launcherQuery {
		return
	}
	u.launcherQuery, u.launcherStatus = query, ""
	u.results = nil
	u.resultIndex, u.resultScroll, u.launcherWheelRemainder = 0, 0, 0
	u.sizeLauncher(false)
	u.emitSearch()
}

func launcherEditProcedure(hwnd, msg, wParam, lParam uintptr) uintptr {
	u := activeUI
	if u == nil {
		result, _, _ := defWindowProc.Call(hwnd, msg, wParam, lParam)
		return result
	}
	if msg == wmKeyDown && !imeComposing(hwnd) {
		switch wParam {
		case vkUp:
			u.selectResult(-1)
			return 0
		case vkDown:
			u.selectResult(1)
			return 0
		case vkReturn:
			if u.launcherLayout.input.node.Events["submit"] == "search.open" {
				u.openResult(u.resultIndex)
			}
			return 0
		case 0x79, 0x5d: // Shift+F10 or the Applications key.
			shift, _, _ := getKeyState.Call(vkShift)
			if wParam == 0x5d || int16(shift) < 0 {
				row := u.launcherLayout.row(clientRect(u.launcher), u.launcherDPI, u.resultIndex-u.resultScroll)
				bounds := windowRect(u.launcher)
				u.resultMenu(u.resultIndex, point{bounds.Left + row.Left, bounds.Top + row.Bottom})
				return 0
			}
		case vkEscape:
			u.closeLauncher(true)
			return 0
		}
	}
	if msg == wmChar && (wParam == vkReturn || wParam == vkEscape) && !imeComposing(hwnd) {
		return 0
	}
	result, _, _ := callWindowProc.Call(u.editProc, hwnd, msg, wParam, lParam)
	if msg == 0x010e {
		u.updateLauncherQuery()
	} // WM_IME_ENDCOMPOSITION
	return result
}

func imeComposing(hwnd uintptr) bool {
	context, _, _ := immGetContext.Call(hwnd)
	if context == 0 {
		return false
	}
	defer immReleaseContext.Call(hwnd, context)
	length, _, _ := immGetCompositionString.Call(context, 0x0008, 0, 0)
	if int32(length) > 0 {
		return true
	}
	length, _, _ = immGetCompositionString.Call(context, 0x0001, 0, 0)
	return int32(length) > 0
}

func (u *UI) selectResult(delta int) {
	if len(u.results) == 0 || delta == 0 {
		return
	}
	previousIndex, previousScroll := u.resultIndex, u.resultScroll
	u.resultIndex = max(0, min(u.resultIndex+delta, len(u.results)-1))
	if u.resultIndex == previousIndex {
		return
	}
	if u.resultIndex < u.resultScroll {
		u.resultScroll = u.resultIndex
	}
	rows := u.launcherLayout.visible
	if u.resultIndex >= u.resultScroll+rows {
		u.resultScroll = u.resultIndex - rows + 1
	}
	client := clientRect(u.launcher)
	for i := u.resultScroll; i < min(len(u.results), u.resultScroll+rows); i++ {
		if previousScroll != u.resultScroll || i == u.resultIndex || i == previousIndex {
			row := u.launcherLayout.row(client, u.launcherDPI, i-u.resultScroll)
			invalidateRect.Call(u.launcher, uintptr(unsafe.Pointer(&row)), 0)
		}
	}
}

func (u *UI) openResult(i int) {
	if i < 0 || i >= len(u.results) {
		return
	}
	result := u.results[i]
	u.hideLauncher(false)
	if u.cb.OpenResult != nil {
		u.cb.OpenResult(result)
	}
}

type launcherSurface struct {
	dibSurface
	colors                  palette
	highContrast            bool
	isDark                  bool
	opacity                 byte
	cornerRadius            int
	icons                   [16]cachedIconBitmap
	dpi                     int32
	prompt, title, subtitle uintptr
}

func (f *launcherSurface) releaseFonts() {
	for _, handle := range [...]uintptr{f.prompt, f.title, f.subtitle} {
		if handle != 0 {
			deleteObject.Call(handle)
		}
	}
	f.dpi, f.prompt, f.title, f.subtitle = 0, 0, 0, 0
}

func (f *launcherSurface) release() {
	f.dibSurface.release()
	f.releaseFonts()
	for i := range f.icons {
		f.icons[i].release()
		f.icons[i].source = nil
	}
}

func (f *launcherSurface) prepareFonts(dpi int32) {
	if dpi == f.dpi {
		return
	}
	f.releaseFonts()
	f.prompt = fontFace(dpi, 16, true, "Cascadia Mono")
	f.title = font(dpi, 14, true)
	f.subtitle = fontFace(dpi, 11, false, "Cascadia Mono")
	f.dpi = dpi
}

func (u *UI) repaintLauncherIcons(keys []string) {
	l := &u.launcherLayout
	if u.launcher == 0 || l.icon.width() == 0 {
		return
	}
	for i := u.resultScroll; i < min(len(u.results), u.resultScroll+l.visible); i++ {
		if slices.Contains(keys, u.results[i].Icon) {
			icon := offsetRow(l.icon, l.rowHeight*int32(i-u.resultScroll))
			invalidateRect.Call(u.launcher, uintptr(unsafe.Pointer(&icon)), 0)
		}
	}
}

func offsetRow(r rect, dy int32) rect { r.Top += dy; r.Bottom += dy; return r }

func (r rect) intersects(other rect) bool {
	return r.Left < other.Right && other.Left < r.Right && r.Top < other.Bottom && other.Top < r.Bottom
}

func (u *UI) paintLauncher() {
	u.launcherFrame.paint(u.launcher, u.drawLauncher)
}

func (u *UI) drawLauncher(hdc uintptr, r, dirty rect) {
	p := u.launcherFrame.colors
	dpi := u.launcherDPI
	if dpi == 0 {
		dpi = 96
	}
	border := max(int32(1), scale(1, dpi))
	hasChrome := u.launcherChrome.hasChrome()
	rad := u.launcherCornerRadius()
	if hasChrome {
		fill(hdc, dirty, p.surface)
	} else {
		fill(hdc, dirty, p.border)
		inside := rect{max(dirty.Left, border), max(dirty.Top, border), min(dirty.Right, r.Right-border), min(dirty.Bottom, r.Bottom-border)}
		if inside.width() > 0 && inside.height() > 0 {
			fill(hdc, inside, p.surface)
		}
	}
	frame := &u.launcherFrame
	frame.prepareFonts(dpi)

	layout := &u.launcherLayout
	if layout.input.node == nil {
		return
	}
	pad := scale(12, dpi)
	input := layout.input
	inputBox := rect{pad, input.box.Top + scale(10, dpi), r.Right - pad, input.box.Bottom - scale(10, dpi)}
	if inputBox.intersects(dirty) {
		dockDiam := scaleCornerDiam(rad, 8, dpi)
		dockInnerDiam := max(int32(0), dockDiam-2*border)
		settingsRoundFill(hdc, inputBox, dockDiam, p.accent)
		settingsRoundFill(hdc, settingsInset(inputBox, border), dockInnerDiam, p.background)
		text(hdc, rect{inputBox.Left + scale(10, dpi), inputBox.Top, inputBox.Left + scale(26, dpi), inputBox.Bottom}, input.node.Props.Text, frame.prompt, p.accent, 1)
	}
	if len(u.results) == 0 {
		b := layout.list.box
		if b.intersects(dirty) {
			status := layout.list.node.Props.Label
			if u.launcherQuery != "" {
				status = layout.list.node.Props.Text
			}
			switch u.launcherStatus {
			case "indexing":
				status = u.launcherShell.View.Indexing
			case "empty":
				status = u.launcherShell.View.NoResults
			}
			b.Left += pad
			b.Right -= pad
			text(hdc, b, status, frame.subtitle, p.secondary, 0)
		}
	} else {
		for i := u.resultScroll; i < min(len(u.results), u.resultScroll+layout.visible); i++ {
			slot := i - u.resultScroll
			row := layout.row(r, dpi, slot)
			if row.intersects(dirty) {
				u.drawLauncherRow(hdc, i, row, p, dpi)
			}
		}
	}
	for _, label := range layout.labels {
		b := label.box
		if !b.intersects(dirty) {
			continue
		}
		edgeL, edgeR, edgeB := border, b.Right-border, b.Bottom-border
		if hasChrome {
			edgeL, edgeR, edgeB = 0, b.Right, b.Bottom
		}
		fill(hdc, rect{edgeL, b.Top, edgeR, b.Top + border}, p.border)
		fill(hdc, rect{edgeL, b.Top + border, edgeR, edgeB}, p.sidebar)
		b.Left += pad
		b.Right -= pad
		ink := p.secondary
		if label.node.Props.Variant == "accent" {
			ink = p.accent
		}
		text(hdc, b, label.node.Props.Text, frame.subtitle, ink, 0)
	}
}

func (u *UI) drawLauncherRow(hdc uintptr, i int, row rect, p palette, dpi int32) {
	result := u.results[i]
	frame := &u.launcherFrame
	selected := i == u.resultIndex
	titleInk, mutedInk := p.text, p.secondary
	rad := u.launcherCornerRadius()
	border := max(int32(1), scale(1, dpi))
	if selected {
		settingsRoundFill(hdc, row, scaleCornerDiam(rad, 7, dpi), p.selected)
		pill := rect{row.Left + scale(3, dpi), row.Top + scale(7, dpi), row.Left + scale(6, dpi), row.Bottom - scale(7, dpi)}
		settingsRoundFill(hdc, pill, scaleCornerDiam(rad, 2, dpi), p.accent)
		if frame.highContrast {
			titleInk, mutedInk = p.white, p.white
		}
	}
	l := &u.launcherLayout
	iconDiam := scaleCornerDiam(rad, 6, dpi)
	iconInnerDiam := max(int32(0), iconDiam-2*border)
	badgeDiam := scaleCornerDiam(rad, 5, dpi)
	for _, cell := range l.cells {
		b := offsetRow(cell.box, int32(i-u.resultScroll)*l.rowHeight)
		props := cell.node.Props
		if cell.node.Type == "icon" {
			settingsRoundFill(hdc, b, iconDiam, p.border)
			settingsRoundFill(hdc, settingsInset(b, border), iconInnerDiam, p.background)
			if icon := u.launcherIcon(result.Icon); icon != nil {
				frame.icons[i%l.visible].draw(hdc, settingsInset(b, scale(4, dpi)), icon)
			} else if v1.IsIconName(result.Icon) {
				text(hdc, b, symbolGlyph(result.Icon), frame.subtitle, p.accent, 1)
			} else {
				text(hdc, b, props.Text, frame.subtitle, p.accent, 1)
			}
			continue
		}
		value, ink, face, align := props.Text, titleInk, frame.title, uintptr(0)
		switch props.Variant {
		case "accent":
			ink, face = p.accent, frame.prompt
		case "caption", "muted":
			ink, face = mutedInk, frame.subtitle
		case "warning":
			ink = p.warning
		}
		switch props.Field {
		case "title":
			value = result.Title
		case "subtitle":
			value = result.Subtitle
		case "badge":
			value = result.Subtitle
			if value == "" {
				continue
			}
			settingsRoundFill(hdc, b, badgeDiam, p.background)
			face, ink, align = frame.subtitle, mutedInk, 1
		case "selection":
			if !selected {
				continue
			}
			align = 1
		}
		text(hdc, b, value, face, ink, align)
	}
}
