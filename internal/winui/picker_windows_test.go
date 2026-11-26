package winui

import (
	"runtime"
	"testing"
	"unsafe"

	v1 "quailcrate/pluginapi/v1"
)

func TestPickerHoverAndWheel(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hwnd, err := window("STATIC", "", wsExToolWindow, wsPopup, rect{100, 80, 576, 524}, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer destroyWindow.Call(hwnd)

	p := &optionPicker{
		hwnd: hwnd, dpi: 96, listRect: rect{18, 102, 458, 382},
		options: make([]v1.Option, 10), hoverRow: -1,
	}
	p.refilter()
	u := &UI{picker: p}
	pack := func(pt point) uintptr { return uintptr(uint16(pt.X)) | uintptr(uint16(pt.Y))<<16 }
	first := point{40, 120}
	second := point{40, 160}
	screen := second
	if ok, _, err := user32.NewProc("ClientToScreen").Call(hwnd, uintptr(unsafe.Pointer(&screen))); ok == 0 {
		t.Fatal(err)
	}
	wheel := func(delta int16) uintptr { return uintptr(uint16(delta)) << 16 }
	for _, step := range []struct {
		name                string
		msg, wParam, lParam uintptr
		scroll, hover       int
	}{
		{"enter first row", wmMouseMove, 0, pack(first), 0, 0},
		{"move to next row", wmMouseMove, 0, pack(second), 0, 1},
		{"scroll down", wmMouseWheel, wheel(-120), pack(screen), 1, 2},
		{"reach bottom", wmMouseWheel, wheel(-360), pack(screen), 3, 4},
		{"at bottom", wmMouseWheel, wheel(-120), pack(screen), 3, 4},
		{"reach top", wmMouseWheel, wheel(360), pack(screen), 0, 1},
		{"at top", wmMouseWheel, wheel(120), pack(screen), 0, 1},
		{"partial notch", wmMouseWheel, wheel(-60), pack(screen), 0, 1},
		{"reverse partial notch", wmMouseWheel, wheel(30), pack(screen), 0, 1},
		{"complete notch with remainder", wmMouseWheel, wheel(-150), pack(screen), 1, 2},
		{"complete next notch", wmMouseWheel, wheel(-60), pack(screen), 2, 3},
		{"leave", wmMouseLeave, 0, 0, 2, -1},
	} {
		u.pickerProcedure(step.msg, step.wParam, step.lParam)
		if p.scroll != step.scroll || p.hoverRow != step.hover {
			t.Errorf("%s: scroll=%d hover=%d, want %d and %d", step.name, p.scroll, p.hoverRow, step.scroll, step.hover)
		}
	}
}
