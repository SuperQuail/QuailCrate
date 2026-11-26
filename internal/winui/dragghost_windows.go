package winui

import (
	"fmt"
	"image"
	"strings"
	"unsafe"

	giofont "gioui.org/font"
	"gioui.org/gpu/headless"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/unit"
	"quailcrate/internal/gioui"
	v1 "quailcrate/pluginapi/v1"
)

type itemDragState struct {
	source       *widgetWindow
	controlID    string
	itemID       string
	item         v1.Item
	fromIndex    int
	active       bool
	targetWidget *widgetWindow
	ghostHwnd    uintptr
	ghostFrame   widgetFrame
	ghostSize    image.Point
	ghostDPI     int
	ghostKey     string
	ghostVisible bool
}

type itemDropTarget struct {
	widget    *widgetWindow
	controlID string
	dropIndex int
	reorderTo int
	tabID     string
	crossTab  bool
	crossCard bool
	badge     string
}

func findWidgetItem(view v1.View, controlID, itemID string) (v1.Item, bool) {
	for _, c := range view.DisplayControls() {
		if c.ID != controlID {
			continue
		}
		for _, it := range c.Items {
			if it.ID == itemID {
				return it, true
			}
		}
	}
	return v1.Item{}, false
}

func primaryWidgetDropControl(view v1.View) (string, int) {
	for _, c := range view.DisplayControls() {
		if c.Type == "list" && c.Drop {
			return c.ID, len(c.Items)
		}
	}
	return "__widget__", -1
}

func widgetTabTitle(view v1.View, tabID string) string {
	for _, t := range view.Tabs {
		if t.ID == tabID {
			if strings.TrimSpace(t.Title) != "" {
				return t.Title
			}
			break
		}
	}
	return view.Title
}

func widgetTargetLabel(view v1.View) string {
	if len(view.Tabs) > 1 && view.ActiveTab != "" {
		if title := widgetTabTitle(view, view.ActiveTab); title != "" {
			return title
		}
	}
	return view.Title
}

func (u *UI) resolveItemDropTarget(w *widgetWindow, pressed widgetHit, clientPt, screenPt point) itemDropTarget {
	if w == nil {
		return itemDropTarget{reorderTo: -1}
	}
	h, idx := hitAt(w, clientPt)
	insideSource := w.hwnd != 0 && windowRect(w.hwnd).contains(screenPt)

	// Cross-group target when pointer leaves the source widget (or lands on a peer panel).
	if !insideSource || idx < 0 {
		for _, other := range u.widgets {
			if other == nil || other == w || other.model.PluginID != w.model.PluginID || other.hwnd == 0 {
				continue
			}
			if !windowRect(other.hwnd).contains(screenPt) {
				continue
			}
			cp := screenPt
			screenToClient.Call(other.hwnd, uintptr(unsafe.Pointer(&cp)))
			if oh, oIdx := hitAt(other, cp); oIdx >= 0 && oh.controlID == "__tab__" && oh.itemID != "" {
				tabName := widgetTabTitle(other.model.View, oh.itemID)
				dest := tabName
				if other.model.View.Title != "" && other.model.View.Title != tabName {
					dest = other.model.View.Title + " · " + tabName
				}
				return itemDropTarget{
					widget:    other,
					controlID: "__tab__",
					dropIndex: oh.index,
					reorderTo: -1,
					tabID:     oh.itemID,
					crossCard: true,
					badge:     fmt.Sprintf(u.label("移到 %s", "Move to %s"), dest),
				}
			}
			ctrlID, endIdx := primaryWidgetDropControl(other.model.View)
			if other.collapsed {
				ctrlID, endIdx = "__widget__", -1
			}
			dest := widgetTargetLabel(other.model.View)
			return itemDropTarget{
				widget:    other,
				controlID: ctrlID,
				dropIndex: endIdx,
				reorderTo: -1,
				crossCard: true,
				badge:     fmt.Sprintf(u.label("移到 %s", "Move to %s"), dest),
			}
		}
	}

	// Cross-tab transfer within the same multi-tab widget.
	if idx >= 0 && h.controlID == "__tab__" && h.itemID != "" && h.itemID != w.model.View.ActiveTab {
		tabName := widgetTabTitle(w.model.View, h.itemID)
		return itemDropTarget{
			widget:    w,
			controlID: "__tab__",
			dropIndex: h.index,
			reorderTo: -1,
			tabID:     h.itemID,
			crossTab:  true,
			badge:     fmt.Sprintf(u.label("移到 %s", "Move to %s"), tabName),
		}
	}

	// In-place reorder within the same list/grid control.
	if idx >= 0 && h.controlID == pressed.controlID && h.index != pressed.index {
		dropIdx := h.index
		if h.index > pressed.index {
			dropIdx = h.index + 1
		}
		return itemDropTarget{
			widget:    w,
			controlID: pressed.controlID,
			dropIndex: dropIdx,
			reorderTo: h.index,
		}
	}

	return itemDropTarget{reorderTo: -1}
}

func (u *UI) updateItemDrag(w *widgetWindow, clientPt point) bool {
	if w == nil || w.pressed == nil || !w.pressed.reorder || w.pressed.itemID == "" {
		return false
	}
	dpi := dpiForWindow(w.hwnd)
	d := u.itemDrag
	if d == nil || d.source != w || d.itemID != w.pressed.itemID {
		thresh := scale(5, dpi)
		if abs32(clientPt.X-w.pressedAt.X) < thresh && abs32(clientPt.Y-w.pressedAt.Y) < thresh {
			return false
		}
		item, ok := findWidgetItem(w.model.View, w.pressed.controlID, w.pressed.itemID)
		if !ok {
			item = v1.Item{ID: w.pressed.itemID}
		}
		u.endItemDrag()
		d = &itemDragState{
			source:    w,
			controlID: w.pressed.controlID,
			itemID:    w.pressed.itemID,
			item:      item,
			fromIndex: w.pressed.index,
			active:    true,
		}
		u.itemDrag = d
	}

	screenPt := cursorPosition()
	if w.hwnd != 0 {
		wr := windowRect(w.hwnd)
		screenPt = point{wr.Left + clientPt.X, wr.Top + clientPt.Y}
	}
	u.applyItemDragAt(d, *w.pressed, clientPt, screenPt)
	return true
}

func (u *UI) applyItemDragAt(d *itemDragState, pressed widgetHit, clientPt, screenPt point) {
	w := d.source
	sourceDirty := false
	if w.draggingItemID != d.itemID {
		w.draggingItemID = d.itemID
		sourceDirty = true
	}

	target := u.resolveItemDropTarget(w, pressed, clientPt, screenPt)

	// Clear previous target widget highlight if pointer moved off it.
	if d.targetWidget != nil && d.targetWidget != target.widget {
		prev := d.targetWidget
		if prev.dropControl != "" || prev.dropIndex != 0 {
			prev.dropControl, prev.dropIndex = "", 0
			if prev == w {
				sourceDirty = true
			} else {
				u.presentWidget(prev)
			}
		}
		d.targetWidget = nil
	}

	// Apply new drop highlight to target widget (source or peer).
	if target.widget != nil && target.controlID != "" {
		tw := target.widget
		if tw.dropControl != target.controlID || tw.dropIndex != target.dropIndex {
			tw.dropControl, tw.dropIndex = target.controlID, target.dropIndex
			if tw == w {
				sourceDirty = true
			} else {
				u.presentWidget(tw)
			}
		}
		d.targetWidget = tw
	} else if w.dropControl != "" || w.dropIndex != 0 {
		w.dropControl, w.dropIndex = "", 0
		sourceDirty = true
	}

	if sourceDirty {
		u.presentWidget(w)
	}

	if w.hwnd != 0 {
		u.presentDragGhost(d, screenPt, target.badge)
	}
}

func (u *UI) presentDragGhost(d *itemDragState, screenPt point, badge string) {
	if d == nil || d.source == nil || d.source.hwnd == 0 {
		return
	}
	mon := desktopMonitorAt(screenPt)
	dpi := int(mon.dpi)
	if dpi < 96 {
		dpi = 96
	}
	colors := u.widgetColorsForWidget(d.source)
	var iconImg image.Image
	if d.item.Icon != "" && !v1.IsIconName(d.item.Icon) {
		if img := u.launcherIcon(d.item.Icon); img != nil {
			iconImg = img
		}
	}
	opt := gioui.DragGhostOptions{
		DPI:         dpi,
		Locale:      u.locale,
		Colors:      colors,
		Item:        d.item,
		Icon:        iconImg,
		TargetBadge: badge,
	}
	size := gioui.MeasureDragGhost(opt)
	if size.X < 1 || size.Y < 1 {
		return
	}
	key := fmt.Sprintf("%d|%s|%s|%t|%d|%d", dpi, u.locale, badge, iconImg != nil, colors.IconSize, colors.CornerRadius)
	if d.ghostHwnd == 0 {
		ex := uintptr(wsExToolWindow | wsExTopmost | wsExLayered | wsExTransparent | wsExNoActivate)
		h, err := window(chromeClass, "", ex, wsPopup, rect{Right: int32(size.X), Bottom: int32(size.Y)}, 0, 0, u.module)
		if err != nil {
			return
		}
		d.ghostHwnd = h
	}

	// Offset slightly below-right of the pointer tip so the user can aim cleanly at tabs/slots.
	pad := int32(8 * dpi / 96)
	offX := int32(12*dpi/96) - pad
	offY := int32(14*dpi/96) - pad
	x := screenPt.X + offX
	y := screenPt.Y + offY
	work := mon.info.Monitor
	if work.width() > 0 && work.height() > 0 {
		if x+int32(size.X) > work.Right {
			x = screenPt.X - int32(size.X) + pad - int32(10*dpi/96)
		}
		if y+int32(size.Y) > work.Bottom {
			y = screenPt.Y - int32(size.Y) + pad - int32(10*dpi/96)
		}
		x = max(work.Left, min(x, work.Right-int32(size.X)))
		y = max(work.Top, min(y, work.Bottom-int32(size.Y)))
	}

	if d.ghostKey != key || d.ghostSize != size {
		if u.renderDragGhost(d, opt, size) {
			d.ghostKey = key
			d.ghostSize = size
			d.ghostDPI = dpi
		}
	}
	if d.ghostKey == "" {
		return
	}

	flags := uintptr(swpNoSize | swpNoActivate | swpNoOwnerZOrder)
	setWindowPos.Call(d.ghostHwnd, hwndTopmost, coord(x), coord(y), 0, 0, flags)
	if !d.ghostVisible {
		showWindow.Call(d.ghostHwnd, swShowNoActivate)
		d.ghostVisible = true
	}
}

func (u *UI) renderDragGhost(d *itemDragState, opt gioui.DragGhostOptions, size image.Point) bool {
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
	scale := float32(opt.DPI) / 96
	gtx := layout.Context{
		Ops:         &r.ops,
		Constraints: layout.Exact(size),
		Metric:      unit.Metric{PxPerDp: scale, PxPerSp: scale},
		Locale:      system.Locale{Language: opt.Locale},
	}
	uiFamily := giofont.Typeface("Segoe UI Variable Text, Segoe UI, Microsoft YaHei UI, sans-serif")
	monoFamily := giofont.Typeface("Cascadia Mono, JetBrains Mono, Consolas, Microsoft YaHei UI, monospace")
	if strings.HasPrefix(opt.Locale, "zh") {
		uiFamily = "Microsoft YaHei UI, Segoe UI Variable Text, Segoe UI, sans-serif"
	}
	gioui.LayoutDragGhost(opt, gioui.GioWidgetCanvas{
		Context:      gtx,
		Shaper:       r.shaper,
		Typeface:     uiFamily,
		MonoTypeface: monoFamily,
	})
	if err := r.window.Frame(&r.ops); err != nil {
		return false
	}
	if d.ghostFrame.image == nil || d.ghostFrame.image.Rect.Size() != size {
		d.ghostFrame.image = image.NewRGBA(image.Rectangle{Max: size})
	}
	if err := r.window.Screenshot(d.ghostFrame.image); err != nil {
		return false
	}
	return presentLayeredBitmap(d.ghostHwnd, &d.ghostFrame)
}

func (u *UI) endItemDrag() {
	d := u.itemDrag
	if d == nil {
		return
	}
	u.itemDrag = nil
	d.ghostFrame.release()
	if d.ghostHwnd != 0 {
		destroyWindow.Call(d.ghostHwnd)
		d.ghostHwnd = 0
	}
	if d.targetWidget != nil && d.targetWidget != d.source {
		tw := d.targetWidget
		if tw.dropControl != "" || tw.dropIndex != 0 {
			tw.dropControl, tw.dropIndex = "", 0
			u.presentWidget(tw)
		}
	}
	if d.source != nil {
		sw := d.source
		changed := sw.draggingItemID != "" || sw.dropControl != "" || sw.dropIndex != 0
		sw.draggingItemID = ""
		sw.dropControl, sw.dropIndex = "", 0
		if changed {
			u.presentWidget(sw)
		}
	}
}
