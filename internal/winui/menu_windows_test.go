//go:build interactive && windows

package winui

import (
	"fmt"
	"runtime"
	"testing"

	"quailcrate/internal/gioui"
	apptheme "quailcrate/internal/theme"
)

func TestOverflowContextMenuActions(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	const dpi, workHeight = 120, 360
	items := []gioui.MenuItem{
		{ID: "item:open", Label: "打开", Icon: "rocket"},
		{Separator: true},
		{Disabled: true, Label: "Disabled"},
	}
	for i := 0; i < 20; i++ {
		items = append(items, gioui.MenuItem{ID: fmt.Sprintf("item:%d", i), Label: fmt.Sprintf("Command %d", i)})
	}
	items = append(items, gioui.MenuItem{ID: "sys:appearance", Label: "自定义外观与透明度…", Icon: "palette"})
	view := gioui.NewMenuViewport(items, dpi, workHeight)
	if view.MaxScroll <= 0 || view.Size.Y > workHeight {
		t.Fatalf("overflow viewport size=%v maxScroll=%d", view.Size, view.MaxScroll)
	}
	// A hidden layered window exercises the same bitmap presentation as the
	// desktop popup without taking focus or interacting with the user's windows.
	hwnd, err := window("STATIC", "", wsExLayered|wsExToolWindow, wsPopup, rect{Right: int32(view.Size.X), Bottom: int32(view.Size.Y)}, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var selected string
	m := &popupMenu{
		hwnd: hwnd, items: items, viewport: view, dpi: dpi, palette: apptheme.DefaultPalette(),
		hovered: -1, onSelect: func(id string) { selected = id },
	}
	u := &UI{menu: m, locale: "zh-CN"}
	defer u.widgetRenderer.release()
	defer u.closeContextMenu("")
	if !u.presentContextMenu() {
		t.Fatal("failed to present the themed menu on the layered window")
	}
	if got := m.frame.image.Bounds().Size(); got != view.Size {
		t.Fatalf("rendered menu size %v, want %v", got, view.Size)
	}

	u.menuProcedure(wmKeyDown, vkUp, 0)
	if !m.hasHover || m.hovered != len(items)-1 || m.scroll != view.MaxScroll {
		t.Fatalf("Up from no selection: index=%d scroll=%d", m.hovered, m.scroll)
	}
	u.menuProcedure(wmKeyDown, vkDown, 0)
	if m.hovered != 0 || m.scroll != 0 {
		t.Fatalf("keyboard wrap: index=%d scroll=%d", m.hovered, m.scroll)
	}
	u.menuProcedure(wmKeyDown, vkDown, 0)
	if m.hovered != 3 {
		t.Fatalf("Down past separator and disabled row: index=%d", m.hovered)
	}

	wheel := func(delta int16) { u.menuProcedure(wmMouseWheel, uintptr(uint16(delta))<<16, 0) }
	wheel(-60)
	if m.scroll != 0 {
		t.Fatalf("partial wheel detent scrolled %d pixels", m.scroll)
	}
	wheel(-60)
	if m.scroll != view.RowHeight {
		t.Fatalf("one wheel detent scrolled %d pixels, want %d", m.scroll, view.RowHeight)
	}
	clickAt := func(y int) {
		u.menuProcedure(wmLButtonUp, 0, uintptr(uint16(view.Size.X/2))|uintptr(uint16(y))<<16)
	}
	clickAt((view.Up.Min.Y + view.Up.Max.Y) / 2)
	if m.scroll != 0 || u.menu != m {
		t.Fatal("clicking the up arrow did not scroll back to the first row")
	}
	clickAt((view.Down.Min.Y + view.Down.Max.Y) / 2)
	if m.scroll != view.RowHeight || u.menu != m {
		t.Fatal("clicking the down arrow did not scroll one row")
	}
	wheel(-12000)
	if m.scroll != view.MaxScroll {
		t.Fatalf("wheel at bottom: scroll=%d, want %d", m.scroll, view.MaxScroll)
	}
	clickAt(view.Down.Min.Y - view.RowHeight/2)
	if u.menu != nil || selected != "sys:appearance" {
		t.Fatalf("bottom-row click: selected=%q, menu still open=%t", selected, u.menu != nil)
	}
}
