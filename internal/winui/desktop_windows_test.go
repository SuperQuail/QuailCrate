package winui

import (
	"runtime"
	"testing"

	apptheme "quailcrate/internal/theme"
	v1 "quailcrate/pluginapi/v1"
)

func TestWidgetAlwaysOnTopOrder(t *testing.T) {
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)

	newWindow := func() uintptr {
		t.Helper()
		h, err := window("STATIC", "", wsExToolWindow|wsExNoActivate, wsPopup|wsVisible,
			rect{-32000, -32000, -31999, -31999}, 0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { destroyWindow.Call(h) })
		return h
	}
	u := New(Callbacks{})
	first, second := &widgetWindow{hwnd: newWindow()}, &widgetWindow{hwnd: newWindow()}
	u.widgets["first"], u.widgets["second"] = first, second
	u.widgetByWindow[first.hwnd], u.widgetByWindow[second.hwnd] = first, second
	app, menu := newWindow(), newWindow()
	if ok, _, err := setWindowPos.Call(menu, hwndTopmost, 0, 0, 0, 0, zFlags); ok == 0 {
		t.Fatal(err)
	}

	checkOrder := func(above, below uintptr) {
		t.Helper()
		if !visibleWindowAbove(above, below) {
			t.Fatalf("window %#x should be above %#x", above, below)
		}
	}
	apply := func(shown bool) {
		t.Helper()
		u.desktopShown = shown
		if !u.applyDesktopOrder(menu) {
			t.Fatal("could not set widget Z order")
		}
		for _, w := range u.widgets {
			if isTopmost(w.hwnd) != (shown || w.model.Layout.AlwaysOnTop) {
				t.Fatalf("incorrect Z band for widget %#x (pinned=%t, desktop shown=%t)", w.hwnd, w.model.Layout.AlwaysOnTop, shown)
			}
		}
	}
	first.model.Layout.AlwaysOnTop = true
	apply(false)
	checkOrder(menu, first.hwnd)
	checkOrder(first.hwnd, app)
	checkOrder(app, second.hwnd)

	// Raise the simulated desktop directly below the menu, as Explorer does on Win+D.
	for _, h := range []uintptr{app, menu} {
		if ok, _, err := setWindowPos.Call(h, hwndTopmost, 0, 0, 0, 0, zFlags); ok == 0 {
			t.Fatal(err)
		}
	}
	apply(true)
	checkOrder(menu, first.hwnd)
	checkOrder(first.hwnd, second.hwnd)
	checkOrder(second.hwnd, app)
	if anchor, inOrder := u.widgetTopmostOrder(app); anchor != menu || !inOrder {
		t.Fatalf("expected settled order below menu, got anchor %#x, inOrder %t", anchor, inOrder)
	}

	first.model.Layout.AlwaysOnTop, second.model.Layout.AlwaysOnTop = false, true
	if _, inOrder := u.widgetTopmostOrder(app); inOrder {
		t.Fatal("swapping pins on the raised desktop should require reordering")
	}
	apply(true)
	checkOrder(second.hwnd, first.hwnd)
	if ok, _, err := setWindowPos.Call(app, hwndNotTopmost, 0, 0, 0, 0, zFlags); ok == 0 {
		t.Fatal(err)
	}
	apply(false)
	checkOrder(second.hwnd, app)
	checkOrder(app, first.hwnd)
}

func TestWidgetDragBoundsPreserveGrabPoint(t *testing.T) {
	w := widgetWindow{
		dragStart:  rect{-200, 50, 181, 309},
		mouseStart: point{-187, 59},
		dragDPI:    120,
	}
	cursor := point{400, 251}
	for _, tc := range []struct {
		dpi  int32
		want rect
	}{
		{120, rect{387, 242, 768, 501}},
		{144, rect{385, 241, 842, 551}},
	} {
		if got := w.dragBounds(cursor, tc.dpi); got != tc.want {
			t.Errorf("drag from 120 to %d DPI: bounds = %+v, want %+v", tc.dpi, got, tc.want)
		}
	}
}

func TestWidgetDragTracksPixelsAndSnapsOnDrop(t *testing.T) {
	work := rect{-1200, -300, 0, 640}
	u := UI{monitors: []desktopMonitor{{dpi: 120, info: monitorInfo{Monitor: work, Work: work}}}}
	// Start at the usual 10 DIP inset from the work area's left and top edges.
	w := widgetWindow{
		dragStart:     rect{-1188, -288, -888, -88},
		mouseStart:    point{-1174, -280},
		dragDPI:       120,
		snapNeighbors: []rect{{-620, -288, -300, -88}, {-1188, 100, -888, 320}},
	}
	check := func(delta point, snapOnDrop bool, wantDelta point) {
		t.Helper()
		cursor := point{w.mouseStart.X + delta.X, w.mouseStart.Y + delta.Y}
		start := w.dragStart
		want := rect{start.Left + wantDelta.X, start.Top + wantDelta.Y, start.Right + wantDelta.X, start.Bottom + wantDelta.Y}
		if got := u.widgetDragTarget(&w, cursor, snapOnDrop); got != want {
			t.Errorf("delta %v, snap on drop %t: got %+v, want %+v", delta, snapOnDrop, got, want)
		}
	}
	for _, distance := range []int32{0, 1, 2, 3, 2, 1, 0, -1, -2} {
		delta := point{distance, -distance}
		check(delta, false, delta)
		check(delta, true, delta)
	}
	// A longer drop aligns with the neighbor's 12 px gap on the traveled
	// axis while preserving the two-pixel nudge on the other axis.
	check(point{252, 2}, true, point{256, 2})
	check(point{2, 179}, true, point{2, 176})
	check(point{252, 2}, false, point{252, 2})
}

func TestWidgetCtrlWheelScalesOnlyCurrentWidgetAndPersists(t *testing.T) {
	type savedMove struct {
		widgetID string
		iconSize int
	}
	var moved []savedMove
	globalSaved := 0
	u := New(Callbacks{
		SaveAppearance: func(apptheme.Config) { globalSaved++ },
		MoveWidget: func(_, widgetID string, _ uint64, layout Layout) {
			moved = append(moved, savedMove{widgetID: widgetID, iconSize: layout.IconSize})
		},
	})
	groupA := &widgetWindow{
		model: Widget{
			PluginID: apptheme.ComponentGroups,
			ID:       "group-1",
			Layout:   Layout{X: 10, Y: 20, Width: 396, Height: 276, Placed: true},
			View: v1.View{
				Title: "工作",
				Controls: []v1.Control{{
					ID: "items", Type: "list", Columns: 4,
					Items: []v1.Item{{ID: "a", Text: "Alpha"}, {ID: "b", Text: "Beta"}},
				}},
			},
		},
		listScroll: map[string]int{"items": 1},
	}
	groupB := &widgetWindow{
		model: Widget{
			PluginID: apptheme.ComponentGroups,
			ID:       "group-2",
			View: v1.View{
				Title: "快捷启动",
				Controls: []v1.Control{{
					ID: "items", Type: "list", Columns: 1,
					Items: []v1.Item{{ID: "c", Text: "Baidu"}},
				}},
			},
		},
		listScroll: make(map[string]int),
	}
	calendar := &widgetWindow{
		model: Widget{
			PluginID: apptheme.ComponentCalendar,
			ID:       "month",
			View: v1.View{
				Title:    "Calendar",
				Controls: []v1.Control{{ID: "days", Type: "grid", Columns: 7}},
			},
		},
	}
	u.widgetModels = []Widget{groupA.model, groupB.model, calendar.model}

	wheelWParam := func(delta int16, ctrl bool) uintptr {
		wp := uintptr(uint16(delta)) << 16
		if ctrl {
			wp |= mkControl
		}
		return wp
	}

	// Ctrl+Wheel up on groupA grows ONLY groupA (40 -> 44); global & groupB remain 40.
	u.handleWidgetWheel(groupA, wheelWParam(120, true))
	if got := u.widgetColorsForWidget(groupA).IconSize; got != 44 {
		t.Fatalf("after Ctrl+Wheel up: groupA iconSize = %d, want 44", got)
	}
	if got := u.widgetColorsForWidget(groupB).IconSize; got != apptheme.DefaultIconSize {
		t.Fatalf("groupB should remain at global default %d, got %d", apptheme.DefaultIconSize, got)
	}
	if u.appearance.EffectiveIconSize() != apptheme.DefaultIconSize || globalSaved != 0 {
		t.Fatalf("Ctrl+Wheel on one widget must not mutate global appearance: size=%d saves=%d", u.appearance.EffectiveIconSize(), globalSaved)
	}
	if groupA.listScroll["items"] != 1 {
		t.Fatalf("Ctrl+Wheel should not scroll list: scroll=%d", groupA.listScroll["items"])
	}

	// Fractional precision-wheel deltas accumulate to one notch (+4dp -> 48).
	u.handleWidgetWheel(groupA, wheelWParam(60, true))
	if got := u.widgetColorsForWidget(groupA).IconSize; got != 44 {
		t.Fatalf("partial delta should not step yet: iconSize = %d", got)
	}
	u.handleWidgetWheel(groupA, wheelWParam(60, true))
	if got := u.widgetColorsForWidget(groupA).IconSize; got != 48 {
		t.Fatalf("accumulated delta should step to 48, got %d", got)
	}

	// Global icon size change updates uncustomized groupB, while groupA keeps its independent size.
	cfg := u.appearance.Clone()
	cfg.SetIconSize(56)
	u.applyAppearance(cfg)
	if got := u.widgetColorsForWidget(groupB).IconSize; got != 56 {
		t.Fatalf("uncustomized groupB should follow global 56, got %d", got)
	}
	if got := u.widgetColorsForWidget(groupA).IconSize; got != 48 {
		t.Fatalf("customized groupA should keep 48, got %d", got)
	}

	// Ctrl+Wheel down clamps cleanly at MinIconSize without redundant saves.
	u.handleWidgetWheel(groupA, wheelWParam(-120*20, true))
	if got := u.widgetColorsForWidget(groupA).IconSize; got != apptheme.MinIconSize {
		t.Fatalf("clamped min iconSize = %d, want %d", got, apptheme.MinIconSize)
	}
	countAtMin := len(moved)
	u.handleWidgetWheel(groupA, wheelWParam(-120, true))
	if len(moved) != countAtMin {
		t.Fatalf("already-at-min wheel triggered redundant save: %+v", moved)
	}

	// Ctrl+Wheel up clamps cleanly at MaxIconSize.
	u.handleWidgetWheel(groupA, wheelWParam(120*20, true))
	if got := u.widgetColorsForWidget(groupA).IconSize; got != apptheme.MaxIconSize {
		t.Fatalf("clamped max iconSize = %d, want %d", got, apptheme.MaxIconSize)
	}

	// Non-list widgets (e.g. Calendar) ignore Ctrl+Wheel icon scaling.
	beforeCal := len(moved)
	u.handleWidgetWheel(calendar, wheelWParam(-120, true))
	if len(moved) != beforeCal {
		t.Fatalf("calendar Ctrl+Wheel should not trigger MoveWidget: %+v", moved)
	}

	// Resetting groupA iconSize back to 0 makes it follow the global default (56) again.
	groupA.iconSize = 0
	u.persistWidgetIconSize(groupA)
	if got := u.widgetColorsForWidget(groupA).IconSize; got != 56 {
		t.Fatalf("reset groupA should inherit global 56, got %d", got)
	}

	wantMoved := []savedMove{
		{widgetID: "group-1", iconSize: 44},
		{widgetID: "group-1", iconSize: 48},
		{widgetID: "group-1", iconSize: apptheme.MinIconSize},
		{widgetID: "group-1", iconSize: apptheme.MaxIconSize},
		{widgetID: "group-1", iconSize: 0},
	}
	if len(moved) != len(wantMoved) {
		t.Fatalf("saved widget icon moves = %+v, want %+v", moved, wantMoved)
	}
	for i := range wantMoved {
		if moved[i] != wantMoved[i] {
			t.Fatalf("moved[%d] = %+v, want %+v (all=%+v)", i, moved[i], wantMoved[i], moved)
		}
	}
	if u.widgetModels[0].Layout.IconSize != 0 || !u.widgetModels[0].Layout.Placed || u.widgetModels[0].Layout.X != 10 {
		t.Fatalf("widgetModels layout corrupted: %+v", u.widgetModels[0].Layout)
	}
}

func TestItemDragCrossGroupHighlightsAndCleanup(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	mon := desktopMonitorAt(point{})
	work := mon.info.Work
	hwndA, err := window("STATIC", "", wsExToolWindow|wsExNoActivate, wsPopup, rect{work.Left + 20, work.Top + 20, work.Left + 340, work.Top + 260}, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer destroyWindow.Call(hwndA)
	hwndB, err := window("STATIC", "", wsExToolWindow|wsExNoActivate, wsPopup, rect{work.Left + 380, work.Top + 20, work.Left + 700, work.Top + 260}, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer destroyWindow.Call(hwndB)

	var events []v1.Event
	u := New(Callbacks{
		WidgetEvent: func(_ string, ev v1.Event) { events = append(events, ev) },
	})
	groupA := &widgetWindow{
		hwnd: hwndA,
		model: Widget{
			PluginID: apptheme.ComponentGroups,
			ID:       "group-a",
			View: v1.View{
				Title: "分组A",
				Controls: []v1.Control{{
					ID: "items", Type: "list", Columns: 4, Drop: true, Reorder: true,
					Items: []v1.Item{
						{ID: "i1", Text: "Adobe"},
						{ID: "i2", Text: "Blender"},
					},
				}},
			},
		},
		listScroll: make(map[string]int),
	}
	groupB := &widgetWindow{
		hwnd: hwndB,
		model: Widget{
			PluginID: apptheme.ComponentGroups,
			ID:       "group-b",
			View: v1.View{
				Title: "分组B",
				Controls: []v1.Control{{
					ID: "items", Type: "list", Columns: 4, Drop: true, Reorder: true,
					Items: []v1.Item{{ID: "j1", Text: "VS Code"}},
				}},
			},
		},
		listScroll: make(map[string]int),
	}
	u.widgets[widgetKey(groupA.model.PluginID, groupA.model.ID)] = groupA
	u.widgets[widgetKey(groupB.model.PluginID, groupB.model.ID)] = groupB
	u.buildWidgetHits(groupA)
	u.buildWidgetHits(groupB)
	if len(groupA.hits) < 2 {
		t.Fatalf("expected >= 2 hits on groupA, got %d", len(groupA.hits))
	}

	pressed := groupA.hits[0]
	drag := &itemDragState{
		source:    groupA,
		controlID: pressed.controlID,
		itemID:    pressed.itemID,
		item:      groupA.model.View.Controls[0].Items[0],
		fromIndex: pressed.index,
		active:    true,
	}
	u.itemDrag = drag

	// 1) Dragging over sibling item inside groupA sets source placeholder + reorder insertion bar.
	rectA := windowRect(hwndA)
	sibling := groupA.hits[1].area
	sibClient := point{(sibling.Left + sibling.Right) / 2, (sibling.Top + sibling.Bottom) / 2}
	u.applyItemDragAt(drag, pressed, sibClient, point{rectA.Left + sibClient.X, rectA.Top + sibClient.Y})
	if groupA.draggingItemID != "i1" || groupA.dropControl != "items" || groupA.dropIndex != 2 {
		t.Fatalf("in-group drag state = dragging:%q drop:%q/%d", groupA.draggingItemID, groupA.dropControl, groupA.dropIndex)
	}
	if groupB.dropControl != "" {
		t.Fatalf("groupB should not be highlighted while dragging inside groupA: %q", groupB.dropControl)
	}

	// 2) Dragging over groupB clears groupA's insertion bar and highlights groupB as drop target.
	rectB := windowRect(hwndB)
	overB := point{(rectB.Left + rectB.Right) / 2, (rectB.Top + rectB.Bottom) / 2}
	u.applyItemDragAt(drag, pressed, point{overB.X - rectA.Left, overB.Y - rectA.Top}, overB)
	if groupA.dropControl != "" || groupA.draggingItemID != "i1" {
		t.Fatalf("source state while over groupB = dragging:%q drop:%q", groupA.draggingItemID, groupA.dropControl)
	}
	if groupB.dropControl != "items" || groupB.dropIndex != 1 {
		t.Fatalf("groupB should highlight drop zone while hovered: drop=%q/%d", groupB.dropControl, groupB.dropIndex)
	}

	// 3) Releasing over groupB cleans up all visual drag highlights and emits widget.transfer.
	groupA.pressed = &pressed
	u.widgetMouseUp(groupA, point{overB.X - rectA.Left, overB.Y - rectA.Top})
	if u.itemDrag != nil || groupA.draggingItemID != "" || groupA.dropControl != "" || groupB.dropControl != "" {
		t.Fatalf("drag visual state not cleaned up: itemDrag=%v A=(%q,%q) B=%q", u.itemDrag, groupA.draggingItemID, groupA.dropControl, groupB.dropControl)
	}
	if len(events) != 1 || events[0].Type != "widget.transfer" || events[0].ItemID != "i1" || events[0].TargetWidgetID != "group-b" {
		t.Fatalf("unexpected emitted events = %+v", events)
	}
}
