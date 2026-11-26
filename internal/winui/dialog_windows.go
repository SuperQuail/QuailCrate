package winui

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	apptheme "quailcrate/internal/theme"
)

type modalKind int

const (
	modalPrompt modalKind = iota
	modalConfirm
	modalMessage
)

type textPrompt struct {
	kind                              modalKind
	ctx                               context.Context
	closed                            chan struct{}
	closeOnce                         sync.Once
	hwnd, owner, edit, font, editProc uintptr
	brush, brushColor                 uintptr
	title, body                       string
	okLabel, cancelLabel              string
	dpi                               int32
	hover                             int // 0=none, 1=ok, 2=cancel, 3=close
	trackingLeave                     bool
	frame                             dibSurface
	chrome                            popupFrame
	dragging                          bool
	dragStartMouse, dragStartWin      point
	editFocused                       bool
	okRect, cancelRect, closeRect     rect
	headerRect                        rect
	done                              func(string, bool)
	finish                            func()
}

var promptEditCallback = syscall.NewCallback(promptEditProcedure)

const (
	promptEdit   = 300
	promptOK     = 1
	promptCancel = 2
	promptClose  = 3
)

type queuedDialog struct {
	open   func(finish func())
	cancel func()
}

func (u *UI) postDialog(open func(finish func())) {
	u.postDialogFor(nil, nil, open)
}

// Queue cancellation and completion on the UI thread. An obsolete request is
// removed immediately even when a different modal window is still open.
func (u *UI) postDialogFor(ctx context.Context, cancel func(), open func(finish func())) {
	u.post(func() {
		if ctx != nil && ctx.Err() != nil {
			cancel()
			return
		}
		d := &queuedDialog{open: open}
		if ctx != nil {
			finished := false
			stop := context.AfterFunc(ctx, func() {
				u.post(func() {
					if finished {
						return
					}
					if i := slices.Index(u.dialogQueue, d); i >= 0 {
						u.dialogQueue = slices.Delete(u.dialogQueue, i, i+1)
					}
					d.cancel()
				})
			})
			d.cancel = func() {
				if !finished {
					finished = true
					stop()
					cancel()
				}
			}
			d.open = func(next func()) {
				if ctx.Err() != nil {
					d.cancel()
					next()
					return
				}
				open(func() { finished = true; stop(); next() })
			}
		}
		u.dialogQueue = append(u.dialogQueue, d)
		u.nextDialog()
	})
}

func (u *UI) nextDialog() {
	if u.dialogBusy || len(u.dialogQueue) == 0 {
		return
	}
	u.dialogBusy = true
	open := u.dialogQueue[0]
	u.dialogQueue[0] = nil
	u.dialogQueue = u.dialogQueue[1:]
	open.open(func() {
		u.dialogBusy = false
		u.post(u.nextDialog)
	})
}

func (u *UI) promptPalette() (palette, bool) {
	if isSystemHighContrast() {
		return highContrastPalette(), false
	}
	res := u.appearance.Resolve(apptheme.ComponentSettings)
	return paletteFromTheme(res.Palette), res.IsDark
}

func (u *UI) popupCornerRadius() int {
	return u.appearance.Normalize().EffectiveCornerRadius()
}

func (u *UI) syncPromptChrome() {
	p := u.popup
	if p == nil || p.hwnd == 0 {
		return
	}
	pal, isDark := u.promptPalette()
	p.chrome.sync(p.hwnd, p.dpi, int32(u.popupCornerRadius()), pal.border, isDark, isSystemHighContrast(), 255)
}

func validWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	ok, _, _ := isWindow.Call(hwnd)
	return ok != 0
}

func (u *UI) resolveDialogOwner(owner uintptr) uintptr {
	if validWindow(owner) {
		return owner
	}
	return u.dialogOwner()
}

func (u *UI) openModal(kind modalKind, title, body, initial, okLabel, cancelLabel string, done func(string, bool)) {
	u.openModalFor(nil, 0, kind, title, body, initial, okLabel, cancelLabel, done)
}

func (u *UI) openModalFor(ctx context.Context, requestedOwner uintptr, kind modalKind, title, body, initial, okLabel, cancelLabel string, done func(string, bool)) {
	if ctx != nil && ctx.Err() != nil {
		if done != nil {
			done("", false)
		}
		return
	}
	u.postDialog(func(finish func()) {
		if ctx != nil && ctx.Err() != nil {
			if done != nil {
				done("", false)
			}
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
		baseW, baseH := int32(436), int32(174)
		if kind != modalPrompt {
			if len(body) > 160 {
				baseW = 492
			}
			lines := max(2, min(14, strings.Count(body, "\n")+1+len(body)/42))
			baseH = int32(128 + lines*22)
		}
		width, height := scale(baseW, dpi), scale(baseH, dpi)
		left := m.Work.Left + max(0, (m.Work.width()-width)/2)
		top := m.Work.Top + max(0, (m.Work.height()-height)/2)
		h, err := window(promptClass, title, wsExControlParent|wsExToolWindow, wsPopup|wsClipChildren, rect{left, top, left + width, top + height}, owner, 0, u.module)
		if err != nil {
			if done != nil {
				done("", false)
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

		var edit, f, editProc uintptr
		if kind == modalPrompt {
			edit, err = window("EDIT", initial, 0, wsTabStop|wsChild|wsVisible|0x0080,
				rect{px(44), px(68), width - px(28), px(92)}, h, promptEdit, u.module)
			if err != nil {
				destroyWindow.Call(h)
				if done != nil {
					done("", false)
				}
				finish()
				return
			}
			f = fontFace(dpi, 15, false, "Cascadia Mono")
			sendMessage.Call(edit, wmSetFont, f, 1)
			sendMessage.Call(edit, 0x00c5, 1024, 0)
			editProc, _, _ = setWindowLong.Call(edit, ^uintptr(3), promptEditCallback)
		}

		p := &textPrompt{
			kind: kind, ctx: ctx, closed: make(chan struct{}),
			hwnd: h, owner: owner, edit: edit, font: f, editProc: editProc,
			title: title, body: body, okLabel: okLabel, cancelLabel: cancelLabel,
			dpi: dpi, editFocused: edit != 0, done: done, finish: finish,
		}
		p.chrome.attach(h, u.module, false)
		u.popup = p
		u.layoutPromptRects()
		u.syncPromptChrome()
		if validWindow(owner) {
			enableWindow.Call(owner, 0)
		}
		showWindow.Call(h, swShow)
		p.chrome.show(true)
		setForegroundWindow.Call(h)
		if edit != 0 {
			setFocus.Call(edit)
			sendMessage.Call(edit, 0x00b1, 0, ^uintptr(0))
		} else {
			setFocus.Call(h)
		}
		repaint(h)
		if ctx != nil {
			go func(prompt *textPrompt, cancelCtx context.Context) {
				select {
				case <-cancelCtx.Done():
					u.post(func() {
						if u.popup == prompt {
							u.closePrompt(false)
						}
					})
				case <-prompt.closed:
				}
			}(p, ctx)
		}
	})
}

func (u *UI) PromptText(title, initial string, done func(string, bool)) {
	u.PromptTextFor(nil, 0, title, initial, done)
}

func (u *UI) PromptTextFor(ctx context.Context, owner uintptr, title, initial string, done func(string, bool)) {
	u.openModalFor(ctx, owner, modalPrompt, title, "", initial, u.label("确定", "OK"), u.label("取消", "Cancel"), done)
}

func (u *UI) Confirm(title, body string, done func(bool)) {
	u.ConfirmFor(nil, 0, title, body, done)
}

func (u *UI) ConfirmFor(ctx context.Context, owner uintptr, title, body string, done func(bool)) {
	u.openModalFor(ctx, owner, modalConfirm, title, body, "", u.label("确认", "Confirm"), u.label("取消", "Cancel"), func(_ string, ok bool) {
		if done != nil {
			done(ok)
		}
	})
}

func (u *UI) Message(title, body string) {
	u.MessageFor(nil, 0, title, body)
}

func (u *UI) MessageFor(ctx context.Context, owner uintptr, title, body string) {
	u.openModalFor(ctx, owner, modalMessage, title, body, "", u.label("知道了", "OK"), "", nil)
}

func (u *UI) layoutPromptRects() {
	p := u.popup
	if p == nil {
		return
	}
	r := clientRect(p.hwnd)
	px := func(v int32) int32 { return scale(v, p.dpi) }
	p.headerRect = rect{0, 0, r.Right, px(44)}
	p.closeRect = rect{r.Right - px(38), px(8), r.Right - px(10), px(36)}
	btnW, btnH := px(88), px(34)
	btnY := r.Bottom - px(50)
	if p.kind == modalMessage {
		p.cancelRect = rect{}
		p.okRect = rect{r.Right - px(18) - btnW, btnY, r.Right - px(18), btnY + btnH}
	} else {
		p.cancelRect = rect{r.Right - px(18) - btnW, btnY, r.Right - px(18), btnY + btnH}
		p.okRect = rect{p.cancelRect.Left - px(10) - btnW, btnY, p.cancelRect.Left - px(10), btnY + btnH}
	}
}

func promptEditProcedure(hwnd, msg, wParam, lParam uintptr) uintptr {
	u := activeUI
	if u == nil || u.popup == nil {
		result, _, _ := defWindowProc.Call(hwnd, msg, wParam, lParam)
		return result
	}
	if msg == wmKeyDown && !imeComposing(hwnd) {
		switch wParam {
		case vkReturn:
			u.closePrompt(true)
			return 0
		case vkEscape:
			u.closePrompt(false)
			return 0
		}
	}
	if msg == wmChar && (wParam == vkReturn || wParam == vkEscape) && !imeComposing(hwnd) {
		return 0
	}
	result, _, _ := callWindowProc.Call(u.popup.editProc, hwnd, msg, wParam, lParam)
	return result
}

func (u *UI) promptEditBrush(dc uintptr) uintptr {
	p := u.popup
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

func (u *UI) promptHit(pt point) int {
	p := u.popup
	if p == nil {
		return 0
	}
	switch {
	case p.okRect.contains(pt):
		return promptOK
	case p.cancelRect.width() > 0 && p.cancelRect.contains(pt):
		return promptCancel
	case p.closeRect.contains(pt):
		return promptClose
	default:
		return 0
	}
}

func (u *UI) promptProcedure(msg, wParam, lParam uintptr) (uintptr, bool) {
	p := u.popup
	if p == nil {
		return 0, false
	}
	switch msg {
	case wmEraseBkgnd:
		return 1, true
	case wmCtlColorEdit:
		return u.promptEditBrush(wParam), true
	case wmWindowPosChanged:
		u.syncPromptChrome()
		return 0, false
	case wmPaint:
		u.paintPrompt()
	case wmClose:
		u.closePrompt(false)
	case wmKeyDown:
		switch wParam {
		case vkEscape:
			u.closePrompt(false)
		case vkReturn:
			u.closePrompt(true)
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
		hit := u.promptHit(mousePoint(lParam))
		if hit != p.hover {
			p.hover = hit
			repaint(p.hwnd)
		}
	case wmMouseLeave:
		p.trackingLeave = false
		if p.hover != 0 {
			p.hover = 0
			repaint(p.hwnd)
		}
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
		switch u.promptHit(mousePoint(lParam)) {
		case promptOK:
			u.closePrompt(true)
		case promptCancel, promptClose:
			u.closePrompt(false)
		}
	case wmCommand:
		switch low(wParam) {
		case promptEdit:
			switch high(wParam) {
			case 0x0100: // EN_SETFOCUS
				if !p.editFocused {
					p.editFocused = true
					repaint(p.hwnd)
				}
			case 0x0200: // EN_KILLFOCUS
				if p.editFocused {
					p.editFocused = false
					repaint(p.hwnd)
				}
			}
		case promptOK:
			u.closePrompt(true)
		case promptCancel:
			u.closePrompt(false)
		default:
			return 0, false
		}
	default:
		return 0, false
	}
	return 0, true
}

func (u *UI) paintPrompt() {
	p := u.popup
	if p == nil {
		return
	}
	u.layoutPromptRects()
	p.frame.paint(p.hwnd, u.drawPrompt)
}

func (u *UI) drawPrompt(hdc uintptr, r, _ rect) {
	p := u.popup
	pal, _ := u.promptPalette()
	px := func(v int32) int32 { return scale(v, p.dpi) }
	border := max(int32(1), px(1))
	rad := u.popupCornerRadius()
	hasChrome := p.chrome.hasChrome()
	pillDiam := scaleCornerDiam(rad, 2, p.dpi)

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

	titleFont := font(p.dpi, 14, true)
	bodyFont := font(p.dpi, 13, false)
	btnFont := font(p.dpi, 13, true)
	monoFont := fontFace(p.dpi, 11, false, "Cascadia Mono")
	promptGlyph := fontFace(p.dpi, 15, true, "Cascadia Mono")
	defer deleteObject.Call(titleFont)
	defer deleteObject.Call(bodyFont)
	defer deleteObject.Call(btnFont)
	defer deleteObject.Call(monoFont)
	defer deleteObject.Call(promptGlyph)

	text(hdc, rect{titleX, border, p.closeRect.Left - px(8), headerH}, p.title, titleFont, pal.text, 0)

	closeInk := pal.secondary
	if p.hover == promptClose {
		settingsRoundFill(hdc, p.closeRect, scaleCornerDiam(rad, 6, p.dpi), pal.selected)
		closeInk = pal.warning
	}
	text(hdc, p.closeRect, "×", promptGlyph, closeInk, 1)

	if p.kind == modalPrompt {
		dock := rect{px(18), px(58), r.Right - px(18), px(100)}
		dockDiam := scaleCornerDiam(rad, 8, p.dpi)
		dockInnerDiam := max(int32(0), dockDiam-2*border)
		dockBorder := pal.border
		if p.editFocused {
			dockBorder = pal.accent
		}
		settingsRoundFill(hdc, dock, dockDiam, dockBorder)
		settingsRoundFill(hdc, settingsInset(dock, border), dockInnerDiam, pal.background)
		text(hdc, rect{dock.Left + px(10), dock.Top, dock.Left + px(24), dock.Bottom}, ">", promptGlyph, pal.accent, 1)
	} else {
		bodyRect := rect{px(20), headerH + px(14), r.Right - px(20), p.okRect.Top - px(14)}
		settingsParagraph(hdc, bodyRect, p.body, bodyFont, pal.text)
	}

	hint := u.label("↵ 确定  ·  ESC 取消", "↵ OK  ·  ESC Cancel")
	if p.kind == modalMessage {
		hint = u.label("↵ 关闭", "↵ Dismiss")
	}
	text(hdc, rect{px(18), p.okRect.Top, p.okRect.Left - px(10), p.okRect.Bottom}, hint, monoFont, pal.secondary, 0)

	// Primary OK button
	btnDiam := scaleCornerDiam(rad, 6, p.dpi)
	btnInnerDiam := max(int32(0), btnDiam-2*border)
	if p.hover == promptOK {
		settingsRoundFill(hdc, p.okRect, btnDiam, pal.accent)
		settingsRoundFill(hdc, settingsInset(p.okRect, border), btnInnerDiam, pal.selected)
		text(hdc, p.okRect, p.okLabel, btnFont, pal.accent, 1)
	} else {
		settingsRoundFill(hdc, p.okRect, btnDiam, pal.accent)
		text(hdc, p.okRect, p.okLabel, btnFont, pal.white, 1)
	}

	if p.cancelRect.width() > 0 {
		cancelBorder, cancelBg, cancelInk := pal.border, pal.background, pal.text
		if p.hover == promptCancel {
			cancelBorder, cancelBg, cancelInk = pal.accent, pal.selected, pal.accent
		}
		settingsRoundFill(hdc, p.cancelRect, btnDiam, cancelBorder)
		settingsRoundFill(hdc, settingsInset(p.cancelRect, border), btnInnerDiam, cancelBg)
		text(hdc, p.cancelRect, p.cancelLabel, btnFont, cancelInk, 1)
	}
}

func (u *UI) closePrompt(accept bool) {
	p := u.popup
	if p == nil {
		return
	}
	var value string
	if p.edit != 0 {
		value = currentText(p.edit)
	}
	if p.ctx != nil && p.ctx.Err() != nil {
		accept = false
	}
	if p.closed != nil {
		p.closeOnce.Do(func() { close(p.closed) })
	}
	ownerAlive := validWindow(p.owner)
	if ownerAlive {
		enableWindow.Call(p.owner, 1)
	}
	p.chrome.release()
	destroyWindow.Call(p.hwnd)
	p.frame.release()
	u.popup = nil
	if p.font != 0 {
		deleteObject.Call(p.font)
	}
	if p.brush != 0 {
		deleteObject.Call(p.brush)
	}
	if ownerAlive && p.owner != u.host && (p.ctx == nil || p.ctx.Err() == nil) {
		setForegroundWindow.Call(p.owner)
	}
	if p.done != nil {
		p.done(value, accept)
	}
	p.finish()
}

var (
	coCreateInstance = ole32.NewProc("CoCreateInstance")
	coTaskMemFree    = ole32.NewProc("CoTaskMemFree")
	clsidFileOpen    = guid{0xdc1c5a9c, 0xe88a, 0x4dde, [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xf7}}
	iidFileOpen      = guid{0xd57c7288, 0xd4ad, 0x4768, [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}
)

type fileType struct{ Name, Pattern *uint16 }

func (u *UI) PickPaths(title string, extensions []string, allowFolders, directoriesOnly, multiple bool, done func([]string, bool)) {
	u.PickPathsFor(nil, 0, title, extensions, allowFolders, directoriesOnly, multiple, done)
}

func (u *UI) PickPathsFor(ctx context.Context, requestedOwner uintptr, title string, extensions []string, allowFolders, directoriesOnly, multiple bool, done func([]string, bool)) {
	if ctx != nil && ctx.Err() != nil {
		if done != nil {
			done(nil, false)
		}
		return
	}
	u.postDialog(func(finish func()) {
		if ctx != nil && ctx.Err() != nil {
			if done != nil {
				done(nil, false)
			}
			finish()
			return
		}
		owner := u.resolveDialogOwner(requestedOwner)
		runPicker := func(folders bool) {
			defer finish()
			if ctx != nil && ctx.Err() != nil {
				if done != nil {
					done(nil, false)
				}
				return
			}
			paths, err := pickShellPaths(owner, title, u.label("支持的文件", "Supported files"), extensions, folders, multiple)
			if ctx != nil && ctx.Err() != nil {
				if done != nil {
					done(nil, false)
				}
				return
			}
			if err != nil {
				log.Printf("open file dialog: %v", err)
				u.MessageFor(ctx, owner, u.label("选择文件失败", "File selection failed"), err.Error())
			}
			if done != nil {
				done(paths, len(paths) > 0 && err == nil)
			}
		}
		if directoriesOnly {
			runPicker(true)
			return
		}
		if !allowFolders {
			runPicker(false)
			return
		}
		// Legacy allowDirectories without directoriesOnly: use our themed modal instead of Win95 MessageBox.
		finish()
		u.openModalFor(ctx, owner, modalConfirm, title,
			u.label("请选择要添加的项目类型，或直接拖入桌面分组中。", "Choose whether to pick folders or files/shortcuts."),
			"", u.label("选文件夹", "Folders"), u.label("选文件", "Files"),
			func(_ string, pickFolders bool) {
				if ctx != nil && ctx.Err() != nil {
					if done != nil {
						done(nil, false)
					}
					return
				}
				u.postDialog(func(f2 func()) {
					defer f2()
					if ctx != nil && ctx.Err() != nil {
						if done != nil {
							done(nil, false)
						}
						return
					}
					curOwner := u.resolveDialogOwner(owner)
					paths, err := pickShellPaths(curOwner, title, u.label("支持的文件", "Supported files"), extensions, pickFolders, multiple)
					if ctx != nil && ctx.Err() != nil {
						if done != nil {
							done(nil, false)
						}
						return
					}
					if err != nil {
						log.Printf("open file dialog: %v", err)
						u.MessageFor(ctx, curOwner, u.label("选择文件失败", "File selection failed"), err.Error())
					}
					if done != nil {
						done(paths, len(paths) > 0 && err == nil)
					}
				})
			})
	})
}

func pickShellPaths(owner uintptr, title, filterLabel string, extensions []string, folders, multiple bool) ([]string, error) {
	var dialog uintptr
	status, _, _ := coCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpen)), 0, 1, uintptr(unsafe.Pointer(&iidFileOpen)), uintptr(unsafe.Pointer(&dialog)))
	if int32(status) < 0 {
		return nil, fmt.Errorf("CoCreateInstance: %#08x", uint32(status))
	}
	defer comCall(dialog, 2)
	var options uint32
	comCall(dialog, 10, uintptr(unsafe.Pointer(&options)))
	options |= 0x40 | 0x800 // FORCEFILESYSTEM | PATHMUSTEXIST
	if multiple {
		options |= 0x200
	}
	if folders {
		options |= 0x20
	} else {
		options |= 0x1000 | 0x00100000 // FILEMUSTEXIST | NODEREFERENCELINKS
	}
	if hr := comCall(dialog, 9, uintptr(options)); int32(hr) < 0 {
		return nil, fmt.Errorf("IFileDialog.SetOptions: %#08x", uint32(hr))
	}
	comCall(dialog, 17, uintptr(unsafe.Pointer(wstr(title))))
	if len(extensions) > 0 && !folders {
		patterns := make([]string, 0, len(extensions))
		for _, ext := range extensions {
			ext = strings.TrimPrefix(ext, ".")
			if ext != "" && !strings.ContainsAny(ext, "\\/;:*?") {
				patterns = append(patterns, "*."+ext)
			}
		}
		if len(patterns) > 0 {
			types := fileType{Name: wstr(filterLabel), Pattern: wstr(strings.Join(patterns, ";"))}
			comCall(dialog, 4, 1, uintptr(unsafe.Pointer(&types)))
		}
	}
	hr := comCall(dialog, 3, owner)
	if uint32(hr) == 0x800704c7 {
		return nil, nil
	}
	if int32(hr) < 0 {
		return nil, fmt.Errorf("IFileDialog.Show: %#08x", uint32(hr))
	}
	if !multiple {
		var item uintptr
		if hr := comCall(dialog, 20, uintptr(unsafe.Pointer(&item))); int32(hr) < 0 {
			return nil, fmt.Errorf("IFileDialog.GetResult: %#08x", uint32(hr))
		}
		defer comCall(item, 2)
		if path := shellItemPath(item); path != "" {
			return []string{path}, nil
		}
		return nil, nil
	}
	var array uintptr
	if hr := comCall(dialog, 27, uintptr(unsafe.Pointer(&array))); int32(hr) < 0 {
		return nil, fmt.Errorf("IFileOpenDialog.GetResults: %#08x", uint32(hr))
	}
	defer comCall(array, 2)
	var count uint32
	if hr := comCall(array, 7, uintptr(unsafe.Pointer(&count))); int32(hr) < 0 {
		return nil, fmt.Errorf("IShellItemArray.GetCount: %#08x", uint32(hr))
	}
	paths := make([]string, 0, min(count, 1024))
	for i := uint32(0); i < count && i < 1024; i++ {
		var item uintptr
		if hr := comCall(array, 8, uintptr(i), uintptr(unsafe.Pointer(&item))); int32(hr) < 0 {
			continue
		}
		path := shellItemPath(item)
		comCall(item, 2)
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func shellItemPath(item uintptr) string {
	var output *uint16
	if hr := comCall(item, 5, 0x80058000, uintptr(unsafe.Pointer(&output))); int32(hr) < 0 || output == nil {
		return ""
	}
	defer coTaskMemFree.Call(uintptr(unsafe.Pointer(output)))
	return syscall.UTF16ToString(unsafe.Slice(output, syscallUTF16Len(output)))
}

func syscallUTF16Len(p *uint16) int {
	n := 0
	for n < 32768 && *(*uint16)(unsafe.Add(unsafe.Pointer(p), n*2)) != 0 {
		n++
	}
	return n
}
