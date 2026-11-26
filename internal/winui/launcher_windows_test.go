package winui

import (
	"runtime"
	"testing"

	v2 "quailcrate/pluginapi/v2"
)

func testLauncherShell() LauncherShell {
	return LauncherShell{PluginID: "test.launcher", Name: "Test", View: &v2.Launcher{UIVersion: 2, Title: "Test", Root: &v2.Node{
		Type: "column", ID: "root", Props: v2.Props{Width: 620}, Children: []v2.Node{
			{Type: "searchInput", ID: "input", Props: v2.Props{Height: 58, Label: "Find"}, Events: map[string]string{"change": "search.query", "submit": "search.open"}},
			{Type: "virtualList", ID: "list", Props: v2.Props{VisibleRows: 5, Source: "results"}, Events: map[string]string{"activate": "search.open"}, Children: []v2.Node{
				{Type: "row", ID: "row", Props: v2.Props{Height: 46}, Children: []v2.Node{{Type: "text", ID: "title", Props: v2.Props{Field: "title"}}}},
			}},
		},
	}}}
}

func TestLauncherWheelSelection(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	shell := testLauncherShell()
	hwnd, err := window("STATIC", "", wsExToolWindow, wsPopup, rect{Right: int32(shell.View.Root.Props.Width), Bottom: 324}, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer destroyWindow.Call(hwnd)

	u := &UI{launcher: hwnd, launcherDPI: 96, results: make([]Result, 8)}
	u.launcherLayout.prepare(shell.View.Root, 620, 324, 96, len(u.results))
	for _, step := range []struct {
		delta         int16
		index, scroll int
	}{
		{120, 0, 0}, // Already at the first result.
		{-60, 0, 0}, // Half a wheel detent.
		{30, 0, 0},  // Partial reversal.
		{-90, 1, 0},
		{-600, 6, 2}, // Keep the selection visible across a page boundary.
		{-240, 7, 3},
		{-120, 7, 3}, // Already at the last result.
		{840, 0, 0},
	} {
		u.launcherProcedure(wmMouseWheel, uintptr(uint16(step.delta))<<16, 0)
		if u.resultIndex != step.index || u.resultScroll != step.scroll {
			t.Fatalf("wheel %d: selected %d, offset %d; want %d, %d", step.delta, u.resultIndex, u.resultScroll, step.index, step.scroll)
		}
	}
}

func TestLauncherDropsStaleResultsAcrossSessions(t *testing.T) {
	u := New(Callbacks{})
	u.launcherOpen = true
	u.launcherRevision = 3
	u.launcherQuery = "app"
	current := []Result{{ID: "fresh", Generation: 1}}
	u.SetResults(3, "app", current, "")
	u.SetResults(2, "app", []Result{{ID: "stale"}}, "")
	u.SetResults(4, "other", nil, "empty")
	u.hostProcedure(wmDispatch, 0, 0)
	if len(u.results) != 1 || u.results[0].ID != "fresh" {
		t.Fatal("late or mismatched query replaced current results")
	}
	u.launcherOpen = false
	u.results = nil
	u.SetResults(5, "app", current, "")
	u.hostProcedure(wmDispatch, 0, 0)
	if len(u.results) != 0 {
		t.Fatal("closed shell accepted results")
	}
}
