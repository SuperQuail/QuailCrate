package winui

import (
	"runtime"
	"testing"
)

func TestWidgetDragReleaseAndCancel(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	monitor := desktopMonitorAt(point{})
	work := monitor.info.Work
	left, top := work.Left+40, work.Top+40
	hwnd, err := window("STATIC", "", wsExToolWindow|wsExNoActivate, wsPopup, rect{left, top, left + 240, top + 180}, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer destroyWindow.Call(hwnd)

	var saved []Layout
	u := UI{monitors: []desktopMonitor{monitor}}
	u.cb.MoveWidget = func(_, _ string, _ uint64, l Layout) { saved = append(saved, l) }
	w := widgetWindow{hwnd: hwnd, dragDPI: monitor.dpi}
	grab := point{18, 8}
	beginDrag := func() rect {
		w.dragging = true
		w.dragStart = windowRect(hwnd)
		w.mouseStart = point{w.dragStart.Left + grab.X, w.dragStart.Top + grab.Y}
		return w.dragStart
	}

	start := beginDrag()
	u.widgetMouseUp(&w, grab)
	if w.dragging || windowRect(hwnd) != start || len(saved) != 0 {
		t.Fatalf("stationary release: dragging=%t, rect=%v, saved=%v", w.dragging, windowRect(hwnd), saved)
	}

	beginDrag()
	// The only message carrying the new pointer position is the button-up.
	u.widgetMouseUp(&w, point{grab.X + 1, grab.Y + 2})
	want := rect{start.Left + 1, start.Top + 2, start.Right + 1, start.Bottom + 2}
	if got := windowRect(hwnd); w.dragging || got != want {
		t.Fatalf("release moved window to %v, want %v; dragging=%t", got, want, w.dragging)
	}
	if len(saved) != 1 || !saved[0].Placed || saved[0].X != int(unscale(want.Left-work.Left, monitor.dpi)) || saved[0].Y != int(unscale(want.Top-work.Top, monitor.dpi)) {
		t.Fatalf("unexpected saved position after release: %+v", saved)
	}

	beginDrag()
	setWidgetBounds(hwnd, want, start)
	u.widgetKeyDown(&w, vkEscape)
	if got := windowRect(hwnd); w.dragging || got != want || len(saved) != 1 {
		t.Fatalf("cancel: dragging=%t, rect=%v, want %v, saved=%v", w.dragging, got, want, saved)
	}
}
