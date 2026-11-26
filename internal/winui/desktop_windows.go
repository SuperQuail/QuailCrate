package winui

import (
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"slices"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"quailcrate/internal/gioui"
	apptheme "quailcrate/internal/theme"
	v1 "quailcrate/pluginapi/v1"
)

const (
	widgetTitleHeight   = gioui.WidgetTitleHeight
	widgetGap           = 10
	widgetSnapDistance  = 8 // DIP
	iconSizeWheelStep   = 4 // DIP per Ctrl+Wheel notch
	getPrevious         = 3
	getNext             = 2
	wmWindowPosChanging = 0x0046
)

var (
	shcore                   = windows.NewLazySystemDLL("shcore.dll")
	getDpiForMonitor         = shcore.NewProc("GetDpiForMonitor")
	enumDisplayDevices       = user32.NewProc("EnumDisplayDevicesW")
	getWindowLong            = user32.NewProc("GetWindowLongPtrW")
	getWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	desktopMonitorCallback   = syscall.NewCallback(eachMonitor)
)

type widgetWindow struct {
	model              Widget
	frame              widgetFrame
	hwnd               uintptr
	layeredOK          bool
	drop               *dropTarget
	dropControl        string
	dropIndex          int
	draggingItemID     string
	dropZones          []widgetHit
	listScroll         map[string]int
	scrollAreas        []gioui.WidgetScrollArea
	hits               []widgetHit
	focused            int
	keyboardFocus      bool
	hasHover           bool
	hovered            int
	mouseInside        bool
	trackingLeave      bool
	collapsed          bool
	expandedHeight     int
	locked             bool
	iconSize           int
	pressed            *widgetHit
	pressedAt          point
	mouseStart         point
	dragStart          rect
	dragDPI            int32
	snapNeighbors      []rect
	wheelRemainder     int
	zoomWheelRemainder int
	dragging           bool
	resizing           bool
	sliderDragging     bool
	sliderControl      string
	sliderValue        float64
	lastDragPresent    time.Time
}

type widgetHit struct {
	area              rect
	controlID, itemID string
	menu              string
	index, total      int
	reorder, drop     bool
	slider            bool
	value             float64
}

type displayDevice struct {
	Size        uint32
	Name        [32]uint16
	Description [128]uint16
	Flags       uint32
	ID, Key     [128]uint16
}

type desktopMonitor struct {
	info monitorInfo
	id   string
	dpi  int32
}

type windowPosition struct {
	Window, InsertAfter uintptr
	X, Y, Width, Height int32
	Flags               uint32
}

func widgetKey(pluginID, instanceID string) string { return pluginID + "\n" + instanceID }

func (u *UI) SetWidgets(models []Widget) {
	u.post(func() {
		u.widgetModels = slices.Clone(models)
		u.syncWidgets()
	})
}

func (u *UI) syncWidgets() {
	active := make(map[string]bool, len(u.widgetModels))
	for i, model := range u.widgetModels {
		key := widgetKey(model.PluginID, model.ID)
		active[key] = true
		w := u.widgets[key]
		if w != nil {
			if w.model.Generation == model.Generation && w.model.Layout.Revision > model.Layout.Revision {
				model.Layout = w.model.Layout
				u.widgetModels[i].Layout = model.Layout
			}
			if !reflect.DeepEqual(w.model, model) {
				oldLayout := w.model.Layout
				changedLayout := oldLayout != model.Layout
				w.model = model
				if oldLayout.AlwaysOnTop != model.Layout.AlwaysOnTop {
					u.desktopZDirty = true
				}
				if changedLayout && !w.dragging && !w.resizing {
					w.collapsed = model.Layout.Collapsed
					w.locked = model.Layout.Locked
					w.iconSize = model.Layout.IconSize
					if model.Layout.ExpandedHeight > 0 {
						w.expandedHeight = model.Layout.ExpandedHeight
					}
					oldLayout.Revision = model.Layout.Revision
					oldLayout.IconSize = model.Layout.IconSize
					oldLayout.AlwaysOnTop = model.Layout.AlwaysOnTop
					if oldLayout != model.Layout {
						u.desktopNeedsPlacement = true
					}
				}
				u.buildWidgetHits(w)
				u.presentWidget(w)
			}
			continue
		}
		h, err := window(widgetClass, model.View.Title, wsExToolWindow|wsExLayered, wsPopup, rect{Right: 100, Bottom: 100}, 0, 0, u.module)
		if err != nil {
			log.Printf("widget %s/%s: %v", model.PluginID, model.ID, err)
			continue
		}
		w = &widgetWindow{
			model: model, hwnd: h, hovered: -1, listScroll: make(map[string]int),
			collapsed: model.Layout.Collapsed, locked: model.Layout.Locked,
			expandedHeight: model.Layout.ExpandedHeight,
			iconSize:       model.Layout.IconSize,
		}
		u.widgets[key], u.widgetByWindow[h] = w, w
		w.drop = registerDropTarget(u, w)
		u.desktopNeedsPlacement = true
		u.desktopZDirty = true
	}
	for key, w := range u.widgets {
		if !active[key] {
			u.destroyWidget(w)
			u.desktopNeedsPlacement = true
		}
	}
	hasWidgets := len(u.widgets) > 0
	if hasWidgets != u.desktopTimer {
		if hasWidgets {
			setTimer.Call(u.host, mainTimer, 250, 0)
		} else {
			killTimer.Call(u.host, mainTimer)
		}
		u.desktopTimer = hasWidgets
	}
	if u.desktopNeedsPlacement {
		u.repositionWidgets()
		u.desktopNeedsPlacement = false
	}
	if u.needsSpatialBackdrop() && u.backdrop.version == 0 {
		u.refreshWallpaperBackdrop(false)
	}
	for _, w := range u.widgets {
		u.presentWidget(w)
	}
	u.updateDesktop()
}

func (u *UI) destroyWidget(w *widgetWindow) {
	if u.itemDrag != nil && (u.itemDrag.source == w || u.itemDrag.targetWidget == w) {
		u.endItemDrag()
	}
	if u.popup != nil && u.popup.owner == w.hwnd {
		u.closePrompt(false)
	}
	if u.picker != nil && u.picker.owner == w.hwnd {
		u.closePicker(false)
	}
	if w.drop != nil {
		revokeDropTarget(w.drop)
	}
	w.frame.release()
	delete(u.widgets, widgetKey(w.model.PluginID, w.model.ID))
	delete(u.widgetByWindow, w.hwnd)
	destroyWindow.Call(w.hwnd)
	if len(u.widgets) == 0 {
		u.widgetRenderer.release()
	}
}

func (u *UI) rebuildWidgets() {
	for _, w := range u.widgets {
		u.destroyWidget(w)
	}
	u.shell, u.desktopHost = 0, 0
	u.desktopZDirty = true
	u.syncWidgets()
}

func eachMonitor(handle, _, _, _ uintptr) uintptr {
	u := activeUI
	if u != nil {
		u.monitors = append(u.monitors, describeMonitor(handle))
	}
	return 1
}

func describeMonitor(handle uintptr) desktopMonitor {
	m := desktopMonitor{info: monitorDetails(handle), dpi: dpiForMonitor(handle)}
	m.id = syscall.UTF16ToString(m.info.Device[:])
	device := displayDevice{Size: uint32(unsafe.Sizeof(displayDevice{}))}
	if ok, _, _ := enumDisplayDevices.Call(uintptr(unsafe.Pointer(&m.info.Device[0])), 0, uintptr(unsafe.Pointer(&device)), 1); ok != 0 {
		if id := syscall.UTF16ToString(device.ID[:]); id != "" {
			m.id = id
		}
	}
	return m
}

func dpiForMonitor(handle uintptr) int32 {
	var dx, dy uint32
	if hr, _, _ := getDpiForMonitor.Call(handle, 0, uintptr(unsafe.Pointer(&dx)), uintptr(unsafe.Pointer(&dy))); int32(hr) == 0 && dx != 0 {
		return int32(dx)
	}
	return 96
}

func desktopMonitorAt(p point) desktopMonitor {
	return describeMonitor(monitorHandleAt(p))
}

func (u *UI) refreshMonitors() {
	u.monitors = u.monitors[:0]
	enumDisplayMonitors.Call(0, 0, desktopMonitorCallback, 0)
}

func (u *UI) monitorFor(id string) desktopMonitor {
	var primary desktopMonitor
	for _, m := range u.monitors {
		if m.id == id && id != "" {
			return m
		}
		if m.info.Flags&1 != 0 {
			primary = m
		}
	}
	if primary.info.Work.width() == 0 && len(u.monitors) != 0 {
		primary = u.monitors[0]
	}
	return primary
}

func clampWidget(r, work rect) rect {
	width, height := min(r.width(), work.width()), min(r.height(), work.height())
	x := max(work.Left, min(r.Left, work.Right-width))
	y := max(work.Top, min(r.Top, work.Bottom-height))
	return rect{x, y, x + width, y + height}
}

func (u *UI) repositionWidgets() {
	if len(u.widgets) == 0 {
		return
	}
	u.refreshMonitors()
	var leftY, leftX, leftWidth, rightY int32
	for _, model := range u.widgetModels {
		w := u.widgets[widgetKey(model.PluginID, model.ID)]
		if w == nil || w.dragging || w.resizing {
			continue
		}
		m := u.monitorFor(model.Layout.Monitor)
		if m.info.Work.width() <= 0 {
			continue
		}
		l := model.Layout
		width, height := scale(int32(l.Width), m.dpi), scale(int32(l.Height), m.dpi)
		if width < scale(160, m.dpi) {
			width = scale(312, m.dpi)
		}
		if height < scale(96, m.dpi) {
			height = scale(280, m.dpi)
		}
		if w.collapsed {
			height = scale(widgetTitleHeight+4, m.dpi)
		}
		gap := scale(widgetGap, m.dpi)
		work := m.info.Work
		x, y := work.Left+scale(int32(l.X), m.dpi), work.Top+scale(int32(l.Y), m.dpi)
		if !l.Placed {
			if l.Anchor == "right" {
				x, y = work.Right-width-gap, work.Top+gap+rightY
				rightY += height + gap
			} else {
				if leftY != 0 && leftY+height+gap > work.height() {
					leftX += leftWidth + gap
					leftY, leftWidth = 0, 0
				}
				x, y = work.Left+gap+leftX, work.Top+gap+leftY
				leftY += height + gap
				leftWidth = max(leftWidth, width)
			}
		}
		target := clampWidget(rect{x, y, x + width, y + height}, work)
		if windowRect(w.hwnd) != target {
			move(w.hwnd, target)
		}
		u.buildWidgetHits(w)
		u.presentWidget(w)
	}
}

func (u *UI) setEditing(enabled bool) {
	u.editing = enabled
	for _, w := range u.widgets {
		if !enabled {
			u.cancelWidgetInput(w)
		}
		u.presentWidget(w)
	}
	u.refreshSettings()
}

func processOf(window uintptr) uint32 {
	var id uint32
	getWindowThreadProcessID.Call(window, uintptr(unsafe.Pointer(&id)))
	return id
}

func desktopIconsHost(shell uintptr) uintptr {
	if shell == 0 {
		return 0
	}
	iconView := wstr("SHELLDLL_DefView")
	if view, _, _ := findWindowEx.Call(shell, 0, uintptr(unsafe.Pointer(iconView)), 0); view != 0 {
		return shell
	}
	process := processOf(shell)
	workerClass := wstr("WorkerW")
	worker := uintptr(0)
	for {
		worker, _, _ = findWindowEx.Call(0, worker, uintptr(unsafe.Pointer(workerClass)), 0)
		if worker == 0 {
			return shell
		}
		if processOf(worker) != process {
			continue
		}
		if visible, _, _ := isWindowVisible.Call(worker); visible == 0 {
			continue
		}
		if view, _, _ := findWindowEx.Call(worker, 0, uintptr(unsafe.Pointer(iconView)), 0); view != 0 {
			return worker
		}
	}
}

func (u *UI) updateDesktop() {
	if len(u.widgets) == 0 {
		return
	}
	if u.desktopMarker == 0 {
		marker, err := window(hostClass, "QuailCrateDesktopMarker", wsExToolWindow, wsPopup|0x08000000, rect{}, 0, 0, u.module)
		if err != nil {
			log.Printf("desktop marker: %v", err)
		} else {
			u.desktopMarker = marker
			setWindowPos.Call(marker, hwndBottom, 0, 0, 0, 0, zFlags)
			u.desktopZDirty = true
		}
	}
	shell, _, _ := getShellWindow.Call()
	// Explorer can move the icon view between WorkerW and Progman while both HWNDs are valid.
	host := desktopIconsHost(shell)
	if u.desktopMarker != 0 && u.shell != shell {
		setWindowPos.Call(u.desktopMarker, hwndBottom, 0, 0, 0, 0, zFlags)
	}
	shown := u.desktopMarker != 0 && visibleWindowAbove(host, u.desktopMarker)
	if u.shell != shell || u.desktopHost != host || u.desktopShown != shown {
		log.Printf("desktop: shell=%#x host=%#x shown=%v widgets=%d", shell, host, shown, len(u.widgets))
	}
	needsOrder := u.desktopZDirty || u.shell != shell || u.desktopHost != host || u.desktopShown != shown
	u.shell, u.desktopHost, u.desktopShown = shell, host, shown
	for _, w := range u.widgets {
		style, _, _ := getWindowLong.Call(w.hwnd, gwlStyle)
		if style&wsVisible == 0 || style&wsMinimized != 0 {
			showWindow.Call(w.hwnd, swShowNormalNoActivate)
			needsOrder = true
		}
		if isTopmost(w.hwnd) != (shown || w.model.Layout.AlwaysOnTop) {
			needsOrder = true
		}
	}
	if shown || needsOrder {
		orderAfter, inOrder := u.widgetTopmostOrder(host)
		if needsOrder || !inOrder {
			u.desktopZDirty = !u.applyDesktopOrder(orderAfter)
		}
	}
}

func visibleWindowAbove(hwnd, below uintptr) bool {
	if visible, _, _ := isWindowVisible.Call(hwnd); visible == 0 {
		return false
	}
	for h, n := hwnd, 0; h != 0 && n < 8192; n++ {
		h, _, _ = getWindow.Call(h, getNext)
		if h == below {
			return true
		}
	}
	return false
}

func isTopmost(hwnd uintptr) bool {
	style, _, _ := getWindowLong.Call(hwnd, gwlExStyle)
	return style&wsExTopmost != 0
}

func (u *UI) widgetTopmostOrder(host uintptr) (orderAfter uintptr, inOrder bool) {
	// Find the lowest foreign topmost window above the desktop. On Show Desktop
	// all widgets belong below this anchor, with pinned widgets above unpinned ones.
	widgetsAbove := 0
	sawPinned, pinsAboveOthers := false, true
	for h, n := host, 0; h != 0 && n < 8192; n++ {
		h, _, _ = getWindow.Call(h, getPrevious)
		if h == 0 {
			break
		}
		if w := u.widgetByWindow[h]; w != nil {
			widgetsAbove++
			if w.model.Layout.AlwaysOnTop {
				sawPinned = true
			} else if sawPinned {
				pinsAboveOthers = false
			}
		} else if isTopmost(h) {
			return h, pinsAboveOthers && widgetsAbove == len(u.widgets)
		}
	}
	return hwndTopmost, pinsAboveOthers && widgetsAbove == len(u.widgets)
}

func (u *UI) applyDesktopOrder(orderAfter uintptr) bool {
	u.adjustingDesktopZ = true
	applied := true
	// Inserting after the same anchor places the last group highest.
	for _, pinned := range [...]bool{false, true} {
		after := orderAfter
		if !u.desktopShown && !pinned {
			after = hwndBottom
		}
		for _, w := range u.widgets {
			if w.model.Layout.AlwaysOnTop != pinned {
				continue
			}
			if pinned && !u.desktopShown && isTopmost(w.hwnd) {
				continue
			}
			if after != hwndBottom && after != hwndTopmost && !isTopmost(w.hwnd) {
				setWindowPos.Call(w.hwnd, hwndTopmost, 0, 0, 0, 0, zFlags)
			}
			if ok, _, _ := setWindowPos.Call(w.hwnd, after, 0, 0, 0, 0, zFlags); ok == 0 {
				applied = false
			}
		}
	}
	u.adjustingDesktopZ = false
	return applied
}

func isResizeGrip(w *widgetWindow, p point) bool {
	if w.collapsed || w.locked {
		return false
	}
	r := clientRect(w.hwnd)
	grip := scale(16, dpiForWindow(w.hwnd))
	return p.X >= r.Right-grip && p.Y >= r.Bottom-grip
}

func (u *UI) applyWidgetVisualHeight(w *widgetWindow, expanded bool) {
	dpi := dpiForWindow(w.hwnd)
	r := windowRect(w.hwnd)
	if !expanded {
		r.Bottom = r.Top + scale(widgetTitleHeight+4, dpi)
	} else {
		hDip := w.expandedHeight
		if hDip < 120 {
			hDip = max(240, w.model.Layout.Height)
		}
		r.Bottom = r.Top + scale(int32(hDip), dpi)
		m := desktopMonitorAt(point{r.Left, r.Top})
		r = clampWidget(r, m.info.Work)
	}
	move(w.hwnd, r)
	u.buildWidgetHits(w)
	u.presentWidget(w)
}

func (u *UI) toggleWidgetCollapsed(w *widgetWindow) {
	dpi := dpiForWindow(w.hwnd)
	r := windowRect(w.hwnd)
	if !w.collapsed {
		w.expandedHeight = int(unscale(r.height(), dpi))
		if w.expandedHeight < 120 {
			w.expandedHeight = max(240, w.model.Layout.Height)
		}
		w.collapsed = true
		u.applyWidgetVisualHeight(w, false)
	} else {
		w.collapsed = false
		u.applyWidgetVisualHeight(w, true)
	}
	u.commitWidgetBounds(w, windowRect(w.hwnd))
}

func (u *UI) widgetProcedure(w *widgetWindow, msg, wParam, lParam uintptr) (uintptr, bool) {
	switch msg {
	case wmEraseBkgnd:
		return 1, true
	case wmPaint:
		u.paintWidget(w)
	case wmSize:
		u.buildWidgetHits(w)
		u.presentWidget(w)
	case wmWindowPosChanging:
		if !u.adjustingDesktopZ {
			change := nativePointer[windowPosition](lParam)
			change.Flags |= swpNoZOrder
		}
	case wmDpiChanged:
		if !w.dragging && !w.resizing {
			suggested := *nativePointer[rect](lParam)
			move(w.hwnd, suggested)
		}
		u.buildWidgetHits(w)
		u.presentWidget(w)
	case wmSetFocus:
		if !w.mouseInside || w.keyboardFocus {
			u.presentWidget(w)
		}
	case wmKillFocus:
		redraw := !w.mouseInside || w.keyboardFocus
		w.keyboardFocus = false
		if redraw {
			u.presentWidget(w)
		}
	case wmSetCursor:
		p := cursorPosition()
		screenToClient.Call(w.hwnd, uintptr(unsafe.Pointer(&p)))
		if isResizeGrip(w, p) {
			c, _, _ := loadCursor.Call(0, 32642) // IDC_SIZENWSE
			setCursor.Call(c)
			return 1, true
		}
		return 0, false
	case wmClose:
		return 0, true
	case wmLButtonDown:
		u.widgetMouseDown(w, mousePoint(lParam))
	case wmLButtonDblClk:
		p := mousePoint(lParam)
		if unscale(p.Y, dpiForWindow(w.hwnd)) < widgetTitleHeight {
			if _, idx := hitAt(w, p); idx < 0 {
				u.toggleWidgetCollapsed(w)
			}
		}
	case wmMouseMove:
		u.widgetMouseMove(w, mousePoint(lParam))
	case wmMouseLeave:
		w.trackingLeave = false
		if w.dragging || w.resizing {
			return 0, true
		}
		changed := w.hasHover || w.mouseInside
		w.hasHover, w.hovered, w.mouseInside = false, -1, false
		if changed {
			u.buildWidgetHits(w)
			u.presentWidget(w)
		}
	case wmLButtonUp:
		u.widgetMouseUp(w, mousePoint(lParam))
	case wmRButtonUp:
		u.openWidgetMenu(w, mousePoint(lParam))
	case wmCaptureChanged:
		u.cancelWidgetInput(w)
	case wmKeyDown:
		u.widgetKeyDown(w, wParam)
	case wmMouseWheel:
		u.handleWidgetWheel(w, wParam)
	default:
		return 0, false
	}
	return 0, true
}

func (u *UI) openWidgetNamedMenu(w *widgetWindow, name string) {
	type menuAction struct{ controlID string }
	actions := make(map[string]menuAction)
	var items []gioui.MenuItem
	for _, entry := range w.model.View.Menu {
		if entry.Placement != name {
			continue
		}
		if entry.Type == "separator" {
			items = append(items, gioui.MenuItem{Separator: true})
			continue
		}
		key := "add:" + entry.ID
		actions[key] = menuAction{controlID: entry.ID}
		items = append(items, gioui.MenuItem{
			ID:      key,
			Label:   entry.Text,
			Icon:    entry.Icon,
			Checked: entry.Checked,
			Danger:  entry.Danger,
		})
	}
	if len(items) == 0 {
		if w.model.View.Root == nil && name == "add" {
			u.emitWidget(w, v1.Event{Type: "widget.action", ControlID: "add_files"})
		}
		return
	}
	pal := u.appearance.Resolve(w.model.PluginID).Palette
	u.showContextMenu(cursorPosition(), pal, items, func(id string) {
		if act, ok := actions[id]; ok {
			u.emitWidget(w, v1.Event{Type: "widget.action", ControlID: act.controlID})
		}
	})
}

func (u *UI) openWidgetMenu(w *widgetWindow, p point) {
	h, _ := hitAt(w, p)
	clickedItemID := ""
	clickedTabID := ""
	if h.controlID == "__tab__" {
		clickedTabID = h.itemID
	} else {
		clickedItemID = h.itemID
	}

	var itemEntries, tabEntries, widgetEntries []v1.Control
	for _, entry := range w.model.View.Menu {
		if entry.Placement != "" && entry.Placement != "tab" {
			continue
		}
		if entry.Placement == "tab" {
			if clickedTabID != "" {
				tabEntries = append(tabEntries, entry)
			}
			continue
		}
		if entry.ItemOnly {
			if clickedItemID != "" {
				itemEntries = append(itemEntries, entry)
			}
		} else {
			widgetEntries = append(widgetEntries, entry)
		}
	}

	type menuAction struct {
		controlID string
		itemID    string
		tabID     string
	}
	actions := make(map[string]menuAction)
	var items []gioui.MenuItem

	addEntry := func(prefix string, entry v1.Control, itemID, tabID string) {
		if entry.Type == "separator" {
			items = append(items, gioui.MenuItem{Separator: true})
			return
		}
		key := prefix + ":" + entry.ID
		actions[key] = menuAction{controlID: entry.ID, itemID: itemID, tabID: tabID}
		items = append(items, gioui.MenuItem{
			ID:      key,
			Label:   entry.Text,
			Icon:    entry.Icon,
			Checked: entry.Checked,
			Danger:  entry.Danger,
		})
	}

	for _, entry := range tabEntries {
		addEntry("tab", entry, "", clickedTabID)
	}
	if len(tabEntries) > 0 && (len(itemEntries) > 0 || len(widgetEntries) > 0) {
		items = append(items, gioui.MenuItem{Separator: true})
	}
	for _, entry := range itemEntries {
		addEntry("item", entry, clickedItemID, "")
	}
	if len(itemEntries) > 0 && len(widgetEntries) > 0 {
		items = append(items, gioui.MenuItem{Separator: true})
	}
	for _, entry := range widgetEntries {
		addEntry("widget", entry, "", "")
	}
	if len(items) > 0 {
		items = append(items, gioui.MenuItem{Separator: true})
	}

	collapseLabel := u.label("卷起面板", "Roll up panel")
	collapseIcon := "collapse"
	if w.collapsed {
		collapseLabel = u.label("展开面板", "Expand panel")
		collapseIcon = "expand"
	}
	items = append(items, gioui.MenuItem{ID: "sys:collapse", Label: collapseLabel, Icon: collapseIcon})
	items = append(items, gioui.MenuItem{
		ID:      "sys:always_on_top",
		Label:   u.label("置顶组件", "Keep widget on top"),
		Icon:    "pin",
		Checked: w.model.Layout.AlwaysOnTop,
	})

	lockLabel := u.label("锁定位置与尺寸", "Lock position & size")
	lockIcon := "lock"
	if w.locked {
		lockIcon = "unlock"
	}
	items = append(items, gioui.MenuItem{ID: "sys:lock", Label: lockLabel, Icon: lockIcon, Checked: w.locked})
	if widgetSupportsIconScaling(w.model.View) && w.iconSize > 0 {
		items = append(items, gioui.MenuItem{
			ID:    "sys:reset_icon_size",
			Label: fmt.Sprintf(u.label("恢复默认图标尺寸（当前 %dpx）", "Reset icon size (%dpx)"), w.iconSize),
			Icon:  "grid",
		})
	}
	items = append(items, gioui.MenuItem{ID: "sys:appearance", Label: u.label("自定义外观与透明度…", "Appearance & opacity…"), Icon: "palette"})

	pal := u.appearance.Resolve(w.model.PluginID).Palette
	generation := w.model.Generation
	u.showContextMenu(cursorPosition(), pal, items, func(id string) {
		if u.widgets[widgetKey(w.model.PluginID, w.model.ID)] != w || w.model.Generation != generation {
			return
		}
		if act, ok := actions[id]; ok {
			u.emitWidget(w, v1.Event{Type: "widget.action", ControlID: act.controlID, ItemID: act.itemID, TabID: act.tabID})
			return
		}
		switch id {
		case "sys:collapse":
			u.toggleWidgetCollapsed(w)
		case "sys:always_on_top":
			w.model.Layout.AlwaysOnTop = !w.model.Layout.AlwaysOnTop
			u.persistWidgetLayout(w)
			u.updateDesktop()
		case "sys:lock":
			w.locked = !w.locked
			u.commitWidgetBounds(w, windowRect(w.hwnd))
			u.presentWidget(w)
		case "sys:reset_icon_size":
			w.iconSize = 0
			u.persistWidgetIconSize(w)
			u.buildWidgetHits(w)
			u.presentWidget(w)
		case "sys:appearance":
			u.showAppearanceSettings()
		}
	})
}

func (u *UI) emitWidget(w *widgetWindow, event v1.Event) {
	if u.cb.WidgetEvent != nil {
		event.WidgetID = w.model.ID
		event.Generation, event.ViewRevision = w.model.Generation, w.model.Revision
		u.cb.WidgetEvent(w.model.PluginID, event)
	}
}

func hitAt(w *widgetWindow, p point) (widgetHit, int) {
	for i, h := range w.hits {
		if h.area.contains(p) {
			return h, i
		}
	}
	return widgetHit{}, -1
}

func (u *UI) widgetMouseDown(w *widgetWindow, p point) {
	hadKeyboardFocus := w.keyboardFocus
	w.keyboardFocus = false
	w.pressedAt = p

	dpi := dpiForWindow(w.hwnd)
	h, i := hitAt(w, p)
	w.resizing = isResizeGrip(w, p)
	w.dragging = !w.resizing && i < 0 && unscale(p.Y, dpi) < widgetTitleHeight && (u.editing || !w.locked)
	if w.resizing || w.dragging {
		w.dragStart = windowRect(w.hwnd)
		// Widgets are borderless. Anchor the gesture to the actual button-down event.
		w.mouseStart = point{w.dragStart.Left + p.X, w.dragStart.Top + p.Y}
		w.dragDPI = dpi
		w.snapNeighbors = w.snapNeighbors[:0]
		for _, other := range u.widgets {
			if other != w && other.hwnd != 0 {
				w.snapNeighbors = append(w.snapNeighbors, windowRect(other.hwnd))
			}
		}
		setCapture.Call(w.hwnd)
		setFocus.Call(w.hwnd)
		if hadKeyboardFocus || (w.dragging && u.needsSpatialBackdrop()) {
			w.lastDragPresent = time.Now()
			u.presentWidget(w)
		}
		return
	}
	setFocus.Call(w.hwnd)
	if i < 0 {
		u.presentWidget(w)
		return
	}
	w.focused = i
	w.pressed = &h
	if h.slider {
		w.sliderDragging = true
		w.sliderControl = h.controlID
		w.sliderValue = sliderRatioAt(h.area, p.X, dpi)
	}
	setCapture.Call(w.hwnd)
	u.presentWidget(w)
}

func sliderRatioAt(area rect, x, dpi int32) float64 {
	pad := scale(6, dpi)
	lo := area.Left + pad
	hi := area.Right - pad
	if hi <= lo {
		lo, hi = area.Left, area.Right
	}
	if hi <= lo {
		return 0
	}
	r := float64(x-lo) / float64(hi-lo)
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

func (u *UI) dragMonitorAt(p point) desktopMonitor {
	for _, m := range u.monitors {
		if m.info.Monitor.contains(p) {
			return m
		}
	}
	handle := monitorHandleAt(p)
	return desktopMonitor{info: monitorDetails(handle), dpi: dpiForMonitor(handle)}
}

func widgetSnapEnabled() bool {
	alt, _, _ := getKeyState.Call(vkMenu)
	return int16(alt) >= 0
}

func (u *UI) snapWidgetRect(w *widgetWindow, r rect, dpi int32, resizing bool) rect {
	work := u.dragMonitorAt(point{(r.Left + r.Right) / 2, (r.Top + r.Bottom) / 2}).info.Work
	thresh, gap := scale(widgetSnapDistance, dpi), scale(widgetGap, dpi)
	// Peer window bounds stay fixed during mouse capture. Compare the cached
	// edges in place instead of crossing into user32 for every mouse move.
	bestDelta := func(value, low, high int32, horizontal bool) (int32, bool) {
		best, delta := thresh+1, int32(0)
		consider := func(edge int32) {
			d := edge - value
			if dist := abs32(d); dist < best {
				best, delta = dist, d
			}
		}
		consider(low)
		consider(low + gap)
		consider(high)
		consider(high - gap)
		for _, n := range w.snapNeighbors {
			a, b := n.Top, n.Bottom
			if horizontal {
				a, b = n.Left, n.Right
			}
			consider(a)
			consider(b)
			consider(a - gap)
			consider(b + gap)
		}
		return delta, best <= thresh
	}

	if resizing {
		if dx, ok := bestDelta(r.Right, work.Left, work.Right, true); ok {
			r.Right += dx
		}
		if dy, ok := bestDelta(r.Bottom, work.Top, work.Bottom, false); ok {
			r.Bottom += dy
		}
		return r
	}

	dxL, okL := bestDelta(r.Left, work.Left, work.Right, true)
	dxR, okR := bestDelta(r.Right, work.Left, work.Right, true)
	switch {
	case okL && (!okR || abs32(dxL) <= abs32(dxR)):
		r.Left += dxL
		r.Right += dxL
	case okR:
		r.Left += dxR
		r.Right += dxR
	}

	dyT, okT := bestDelta(r.Top, work.Top, work.Bottom, false)
	dyB, okB := bestDelta(r.Bottom, work.Top, work.Bottom, false)
	switch {
	case okT && (!okB || abs32(dyT) <= abs32(dyB)):
		r.Top += dyT
		r.Bottom += dyT
	case okB:
		r.Top += dyB
		r.Bottom += dyB
	}
	return r
}

func abs32(n int32) int32 {
	if n < 0 {
		return -n
	}
	return n
}

func (w *widgetWindow) dragBounds(cursor point, dpi int32) rect {
	fromStart := func(v int32) int32 { return int32(int64(v) * int64(dpi) / int64(w.dragDPI)) }
	x := cursor.X - fromStart(w.mouseStart.X-w.dragStart.Left)
	y := cursor.Y - fromStart(w.mouseStart.Y-w.dragStart.Top)
	return rect{x, y, x + fromStart(w.dragStart.width()), y + fromStart(w.dragStart.height())}
}

func (u *UI) widgetDragTarget(w *widgetWindow, cursor point, snapOnDrop bool) rect {
	if cursor == w.mouseStart {
		return w.dragStart
	}
	dpi := u.dragMonitorAt(cursor).dpi
	r := w.dragBounds(cursor, dpi)
	if !snapOnDrop {
		return r
	}
	// Preserve fine adjustments independently on each axis, even when the
	// other axis travels far enough to align with a neighbor.
	tolerance := scale(widgetSnapDistance, dpi)
	snapX := abs32(cursor.X-w.mouseStart.X) > tolerance
	snapY := abs32(cursor.Y-w.mouseStart.Y) > tolerance
	if snapX || snapY {
		aligned := u.snapWidgetRect(w, r, dpi, false)
		if snapX {
			r.Left, r.Right = aligned.Left, aligned.Right
		}
		if snapY {
			r.Top, r.Bottom = aligned.Top, aligned.Bottom
		}
	}
	return r
}

func setWidgetBounds(hwnd uintptr, current, target rect) {
	if current == target {
		return
	}
	flags := uintptr(swpNoZOrder | swpNoOwnerZOrder | swpNoActivate)
	if current.width() == target.width() && current.height() == target.height() {
		flags |= swpNoSize
	}
	setWindowPos.Call(hwnd, 0, coord(target.Left), coord(target.Top), coord(target.width()), coord(target.height()), flags)
}

func (w *widgetWindow) trackMouseLeave() {
	if w.trackingLeave {
		return
	}
	tme := trackMouseEventInfo{Size: uint32(unsafe.Sizeof(trackMouseEventInfo{})), Flags: tmeLeave, HWnd: w.hwnd}
	ok, _, _ := trackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
	w.trackingLeave = ok != 0
}

func (u *UI) widgetMouseMove(w *widgetWindow, p point) {
	if w.resizing {
		desktop := cursorPosition()
		dx, dy := desktop.X-w.mouseStart.X, desktop.Y-w.mouseStart.Y
		dpi := w.dragDPI
		minW, maxW := scale(220, dpi), scale(780, dpi)
		minH, maxH := scale(150, dpi), scale(760, dpi)
		newW := max(minW, min(maxW, w.dragStart.width()+dx))
		newH := max(minH, min(maxH, w.dragStart.height()+dy))
		target := rect{w.dragStart.Left, w.dragStart.Top, w.dragStart.Left + newW, w.dragStart.Top + newH}
		if widgetSnapEnabled() {
			target = u.snapWidgetRect(w, target, dpi, true)
		}
		target.Right = max(target.Left+minW, min(target.Left+maxW, target.Right))
		target.Bottom = max(target.Top+minH, min(target.Top+maxH, target.Bottom))
		setWidgetBounds(w.hwnd, windowRect(w.hwnd), target) // WM_SIZE updates layout and paints resized content.
		return
	}
	if w.dragging {
		cur := windowRect(w.hwnd)
		target := u.widgetDragTarget(w, cursorPosition(), false)
		setWidgetBounds(w.hwnd, cur, target)
		if target != cur && u.needsSpatialBackdrop() && time.Since(w.lastDragPresent) >= 16*time.Millisecond {
			w.lastDragPresent = time.Now()
			u.presentWidget(w)
		}
		return
	}
	if w.sliderDragging && w.pressed != nil {
		next := sliderRatioAt(w.pressed.area, p.X, dpiForWindow(w.hwnd))
		if next != w.sliderValue {
			w.sliderValue = next
			u.presentWidget(w)
		}
		return
	}
	w.trackMouseLeave()
	entered := !w.mouseInside
	if entered {
		w.mouseInside = true
		u.buildWidgetHits(w)
	}
	_, index := hitAt(w, p)
	hoverChanged := entered || (index >= 0) != w.hasHover || (index >= 0 && w.hovered != index)
	if hoverChanged {
		w.hasHover = index >= 0
		w.hovered = index
	}
	if w.pressed != nil && w.pressed.reorder && w.pressed.itemID != "" {
		if u.updateItemDrag(w, p) {
			if index >= 0 && w.focused != index {
				w.focused = index
				hoverChanged = true
			}
			if hoverChanged {
				u.presentWidget(w)
			}
			return
		}
	}
	if hoverChanged {
		u.presentWidget(w)
	}
}

func (u *UI) commitWidgetBounds(w *widgetWindow, r rect) {
	m := desktopMonitorAt(point{(r.Left + r.Right) / 2, (r.Top + r.Bottom) / 2})
	clamped := clampWidget(r, m.info.Work)
	setWidgetBounds(w.hwnd, r, clamped)
	r = clamped
	heightDip := int(unscale(r.height(), m.dpi))
	if w.collapsed && w.expandedHeight > 0 {
		heightDip = w.expandedHeight
	} else if !w.collapsed {
		w.expandedHeight = heightDip
	}
	layout := &w.model.Layout
	layout.Monitor = m.id
	layout.X = int(unscale(r.Left-m.info.Work.Left, m.dpi))
	layout.Y = int(unscale(r.Top-m.info.Work.Top, m.dpi))
	layout.Width = int(unscale(r.width(), m.dpi))
	layout.Height = heightDip
	layout.Placed = true
	layout.Collapsed = w.collapsed
	layout.Locked = w.locked
	layout.ExpandedHeight = w.expandedHeight
	layout.IconSize = w.iconSize
	u.persistWidgetLayout(w)
}

func (u *UI) persistWidgetLayout(w *widgetWindow) {
	w.model.Layout.Revision++
	for i, model := range u.widgetModels {
		if model.ID == w.model.ID && model.PluginID == w.model.PluginID {
			u.widgetModels[i].Layout = w.model.Layout
			break
		}
	}
	if u.cb.MoveWidget != nil {
		u.cb.MoveWidget(w.model.PluginID, w.model.ID, w.model.Generation, w.model.Layout)
	}
}

func (u *UI) widgetMouseUp(w *widgetWindow, p point) {
	if w.resizing || w.dragging {
		r := windowRect(w.hwnd)
		if w.dragging {
			// A button-up can arrive before a final WM_MOUSEMOVE. Use the
			// release event's client coordinates on our borderless window.
			cursor := point{r.Left + p.X, r.Top + p.Y}
			target := u.widgetDragTarget(w, cursor, widgetSnapEnabled())
			setWidgetBounds(w.hwnd, r, target)
			r = windowRect(w.hwnd)
		}
		w.resizing, w.dragging = false, false
		releaseCapture.Call()
		if r != w.dragStart {
			u.commitWidgetBounds(w, r)
		}
		if u.needsSpatialBackdrop() {
			u.presentWidget(w)
		}
		if w.mouseInside {
			w.trackMouseLeave()
		}
		return
	}
	pressed := w.pressed
	wasSlider := w.sliderDragging
	w.pressed = nil
	w.sliderDragging = false
	w.sliderControl = ""
	wasDraggingItem := u.itemDrag != nil && u.itemDrag.source == w && u.itemDrag.active
	u.endItemDrag()
	if pressed == nil {
		return
	}
	releaseCapture.Call()
	if wasSlider && pressed.slider {
		val := sliderRatioAt(pressed.area, p.X, dpiForWindow(w.hwnd))
		w.sliderValue = val
		u.submitWidgetSlider(w, pressed.controlID, val)
		u.presentWidget(w)
		return
	}
	h, i := hitAt(w, p)

	// Cross-tab or cross-panel transfer when dragging a reorderable item.
	if pressed.reorder && pressed.itemID != "" {
		screenPt := cursorPosition()
		if w.hwnd != 0 {
			wr := windowRect(w.hwnd)
			screenPt = point{wr.Left + p.X, wr.Top + p.Y}
		}
		target := u.resolveItemDropTarget(w, *pressed, p, screenPt)
		if target.crossCard && target.widget != nil {
			u.emitWidget(w, v1.Event{
				Type:           "widget.transfer",
				ControlID:      pressed.controlID,
				ItemID:         pressed.itemID,
				TargetWidgetID: target.widget.model.ID,
				TargetTabID:    target.tabID,
			})
			u.presentWidget(w)
			return
		}
		if target.crossTab && target.tabID != "" {
			u.emitWidget(w, v1.Event{
				Type:           "widget.transfer",
				ControlID:      pressed.controlID,
				ItemID:         pressed.itemID,
				TargetWidgetID: w.model.ID,
				TargetTabID:    target.tabID,
			})
			u.presentWidget(w)
			return
		}
		if target.reorderTo >= 0 && target.reorderTo != pressed.index {
			u.emitWidget(w, v1.Event{Type: "widget.reorder", ControlID: pressed.controlID, ItemID: pressed.itemID, From: pressed.index, To: target.reorderTo})
			u.presentWidget(w)
			return
		}
		if wasDraggingItem {
			// User dragged the item and released back on itself or empty desktop: cancel without launching.
			u.presentWidget(w)
			return
		}
	}

	if i < 0 || h.controlID != pressed.controlID {
		u.presentWidget(w)
		return
	}
	if u.activateWidgetMenu(w, h, p) {
		return
	}
	if pressed.reorder && h.index != pressed.index {
		u.emitWidget(w, v1.Event{Type: "widget.reorder", ControlID: h.controlID, ItemID: pressed.itemID, From: pressed.index, To: h.index})
	} else if h.itemID == pressed.itemID {
		u.activateWidgetControl(w, h)
	}
	u.presentWidget(w)
}

func (u *UI) activateWidgetControl(w *widgetWindow, h widgetHit) {
	for _, c := range w.model.View.DisplayControls() {
		if c.ID == h.controlID {
			u.emitWidget(w, v1.Event{Type: "widget.action", ControlID: c.ActionID(), ItemID: h.itemID})
			return
		}
	}
}

func (u *UI) submitWidgetSlider(w *widgetWindow, controlID string, val float64) {
	if val < 0 {
		val = 0
	} else if val > 1 {
		val = 1
	}
	raw := json.RawMessage(strconv.FormatFloat(val, 'f', 4, 64))
	for _, c := range w.model.View.DisplayControls() {
		if c.ID == controlID && c.Type == "slider" {
			u.emitWidget(w, v1.Event{Type: "widget.action", ControlID: c.ActionID(), InputValue: raw})
			return
		}
	}
}

func (u *UI) activateWidgetMenu(w *widgetWindow, h widgetHit, p point) bool {
	if h.controlID == "__tab__" {
		if h.itemID != "" && h.itemID != w.model.View.ActiveTab {
			u.emitWidget(w, v1.Event{Type: "widget.tabSelect", TabID: h.itemID})
		}
		u.presentWidget(w)
		return true
	}
	if h.menu == "" {
		return false
	}
	if h.menu == "context" {
		u.openWidgetMenu(w, p)
	} else {
		u.openWidgetNamedMenu(w, h.menu)
	}
	return true
}

func (u *UI) cancelWidgetInput(w *widgetWindow) {
	dragging, resizing, pressed := w.dragging, w.resizing, w.pressed
	w.dragging, w.resizing, w.pressed = false, false, nil
	w.sliderDragging, w.sliderControl = false, ""
	if u.itemDrag != nil && u.itemDrag.source == w {
		u.endItemDrag()
	}
	if dragging || resizing {
		setWidgetBounds(w.hwnd, windowRect(w.hwnd), w.dragStart)
		if dragging && u.needsSpatialBackdrop() {
			u.presentWidget(w)
		}
	}
	if dragging || resizing || pressed != nil {
		if capture, _, _ := getCapture.Call(); capture == w.hwnd {
			releaseCapture.Call()
		}
	}
	if (dragging || resizing) && w.mouseInside {
		w.trackMouseLeave()
	}
}

func (u *UI) widgetKeyDown(w *widgetWindow, key uintptr) {
	switch key {
	case vkEscape:
		if w.dragging || w.resizing || w.pressed != nil {
			u.cancelWidgetInput(w)
		} else if u.editing {
			u.setEditing(false)
		}
	case vkTab, vkUp, vkDown, vkLeft, vkRight:
		if len(w.hits) == 0 {
			return
		}
		direction := 1
		shift, _, _ := getKeyState.Call(vkShift)
		if (key == vkTab && int16(shift) < 0) || key == vkUp || key == vkLeft {
			direction = -1
		}
		if (key == vkLeft || key == vkRight) && w.focused >= 0 && w.focused < len(w.hits) && w.hits[w.focused].slider {
			h := w.hits[w.focused]
			next := h.value + float64(direction)*0.05
			if next < 0 {
				next = 0
			} else if next > 1 {
				next = 1
			}
			w.hits[w.focused].value = next
			w.keyboardFocus = true
			u.submitWidgetSlider(w, h.controlID, next)
			u.presentWidget(w)
			return
		}
		if int16(shift) < 0 && (key == vkUp || key == vkDown) {
			h := w.hits[w.focused]
			if h.reorder && h.index+direction >= 0 && h.index+direction < h.total {
				u.emitWidget(w, v1.Event{Type: "widget.reorder", ControlID: h.controlID, ItemID: h.itemID, From: h.index, To: h.index + direction})
				return
			}
		}
		w.keyboardFocus = true
		w.focused = nextWidgetFocus(w, key, direction)
		u.presentWidget(w)
	case vkReturn, vkSpace:
		if w.focused < len(w.hits) {
			h := w.hits[w.focused]
			if u.activateWidgetMenu(w, h, point{X: (h.area.Left + h.area.Right) / 2, Y: (h.area.Top + h.area.Bottom) / 2}) {
				return
			}
			if h.itemID != "" || !h.drop {
				u.activateWidgetControl(w, h)
			}
		}
	}
}

func nextWidgetFocus(w *widgetWindow, key uintptr, direction int) int {
	old := w.focused
	h := w.hits[old]
	if h.itemID != "" {
		for _, c := range w.model.View.DisplayControls() {
			if c.ID != h.controlID || c.Type != "grid" {
				continue
			}
			first, last := old, old
			for first > 0 && w.hits[first-1].controlID == c.ID {
				first--
			}
			for last+1 < len(w.hits) && w.hits[last+1].controlID == c.ID {
				last++
			}
			switch key {
			case vkTab:
				if direction > 0 {
					old = last
				} else {
					old = first
				}
			case vkUp, vkDown:
				return max(first, min(last, old+direction*max(1, c.Columns)))
			case vkLeft, vkRight:
				return max(first, min(last, old+direction))
			}
			break
		}
	}
	return (old + direction + len(w.hits)) % len(w.hits)
}

func widgetSupportsIconScaling(view v1.View) bool {
	for _, c := range view.DisplayControls() {
		if c.Type == "list" {
			return true
		}
	}
	return false
}

func (u *UI) handleWidgetWheel(w *widgetWindow, wParam uintptr) {
	delta := int(int16(wParam >> 16))
	if delta == 0 {
		return
	}
	if wParam&mkControl != 0 && widgetSupportsIconScaling(w.model.View) && !w.collapsed {
		u.scaleWidgetIcons(w, delta)
		return
	}
	u.scrollWidget(w, delta)
}

func (u *UI) effectiveWidgetIconSize(w *widgetWindow) int {
	if w != nil && w.iconSize > 0 {
		return max(apptheme.MinIconSize, min(apptheme.MaxIconSize, w.iconSize))
	}
	return u.appearance.EffectiveIconSize()
}

func (u *UI) persistWidgetIconSize(w *widgetWindow) {
	w.model.Layout.IconSize = w.iconSize
	u.persistWidgetLayout(w)
}

func (u *UI) scaleWidgetIcons(w *widgetWindow, delta int) {
	w.zoomWheelRemainder += delta
	steps := w.zoomWheelRemainder / 120
	w.zoomWheelRemainder %= 120
	if steps == 0 {
		return
	}
	cur := u.effectiveWidgetIconSize(w)
	next := max(apptheme.MinIconSize, min(apptheme.MaxIconSize, cur+steps*iconSizeWheelStep))
	if next == cur {
		return
	}
	w.iconSize = next
	u.persistWidgetIconSize(w)
	u.buildWidgetHits(w)
	if w.hwnd != 0 && w.mouseInside {
		p := cursorPosition()
		screenToClient.Call(w.hwnd, uintptr(unsafe.Pointer(&p)))
		_, idx := hitAt(w, p)
		w.hasHover = idx >= 0
		w.hovered = idx
	}
	u.presentWidget(w)
}

func (u *UI) scrollWidget(w *widgetWindow, delta int) {
	if delta == 0 {
		return
	}
	w.wheelRemainder += delta
	steps := w.wheelRemainder / 120
	w.wheelRemainder %= 120
	if steps == 0 {
		return
	}
	p := cursorPosition()
	screenToClient.Call(w.hwnd, uintptr(unsafe.Pointer(&p)))
	// Wheel over the header cycles inline tabs when a panel has multiple tabs.
	if unscale(p.Y, dpiForWindow(w.hwnd)) < widgetTitleHeight && len(w.model.View.Tabs) > 1 {
		tabs := w.model.View.Tabs
		cur := 0
		for idx, t := range tabs {
			if t.ID == w.model.View.ActiveTab {
				cur = idx
				break
			}
		}
		step := 1
		if delta > 0 {
			step = -1
		}
		next := (cur + step + len(tabs)) % len(tabs)
		u.emitWidget(w, v1.Event{Type: "widget.tabSelect", TabID: tabs[next].ID})
		return
	}
	if w.collapsed {
		return
	}
	for _, area := range w.scrollAreas {
		if !widgetRect(area.Area).contains(p) {
			continue
		}
		old := w.listScroll[area.ControlID]
		next := max(0, min(old-steps, area.Max))
		if next == old {
			return
		}
		w.listScroll[area.ControlID] = next
		u.buildWidgetHits(w)
		u.presentWidget(w)
		return
	}
	layout := w.model.View.LayoutTree()
	action := layout.WheelDown
	if delta > 0 {
		action = layout.WheelUp
	}
	if action != "" {
		u.emitWidget(w, v1.Event{Type: "widget.action", ControlID: action})
	}
}
