//go:build interactive && windows

package winui

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
	"unsafe"

	"quailcrate/internal/pluginapps"
	v1 "quailcrate/pluginapi/v1"
)

func TestAppOptionPickerLifecycle(t *testing.T) {
	var ui *UI
	var result error
	ui = New(Callbacks{Ready: func() {
		go func() { result = exerciseAppOptionPicker(ui); ui.Close() }()
	}})
	if err := ui.Run(); err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatal(result)
	}
}

func exerciseAppOptionPicker(ui *UI) error {
	w := &pluginAppWindow{ui: ui}
	w.hwnd.Store(ui.host)
	active, cancelActive := context.WithCancel(context.Background())
	defer cancelActive()
	queued, cancelQueued := context.WithCancel(context.Background())
	defer cancelQueued()
	action := v1.Action{Type: "ui.pickOptions", Title: "Choose application"}
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("app-%02d", i)
		action.Options = append(action.Options, v1.Option{ID: id, Text: id, Subtitle: "Installed application"})
	}
	open := func(ctx context.Context) <-chan pluginapps.DialogResult {
		results := make(chan pluginapps.DialogResult, 2)
		w.handleDialog(ctx, action, func(r pluginapps.DialogResult) { results <- r })
		return results
	}
	first, second := open(active), open(queued)
	if err := onUI(ui, func() error {
		p := ui.picker
		if p == nil || p.owner != ui.host || len(ui.dialogQueue) != 1 {
			return fmt.Errorf("picker or queued request unavailable")
		}
		update := user32.NewProc("UpdateWindow")
		update.Call(p.hwnd)
		frame := p.frame.dibBitmap
		pt := point{p.listRect.Left + scale(30, p.dpi), p.listRect.Top + scale(18, p.dpi)}
		user32.NewProc("ClientToScreen").Call(p.hwnd, uintptr(unsafe.Pointer(&pt)))
		coord := uintptr(uint16(pt.X)) | uintptr(uint16(pt.Y))<<16
		for _, delta := range []int16{-60, -60, -360, 120, 120} {
			sendMessage.Call(p.hwnd, wmMouseWheel, uintptr(uint16(delta))<<16, coord)
			update.Call(p.hwnd)
		}
		if p.scroll != 2 || frame == 0 || frame != p.frame.dibBitmap {
			return fmt.Errorf("unexpected scroll %d or replaced picker frame", p.scroll)
		}
		return nil
	}); err != nil {
		return err
	}
	read := func(ch <-chan pluginapps.DialogResult, canceled bool) error {
		select {
		case r := <-ch:
			if r.Canceled != canceled || !canceled && !slices.Equal(r.Items, []string{"app-00"}) {
				return fmt.Errorf("unexpected selection result %+v", r)
			}
			return nil
		case <-time.After(3 * time.Second):
			return fmt.Errorf("picker completion timed out")
		}
	}
	cancelQueued()
	if err := read(second, true); err != nil {
		return err
	}
	if err := onUI(ui, func() error {
		if ui.picker == nil || len(ui.dialogQueue) != 0 {
			return fmt.Errorf("queued cancellation disturbed active picker")
		}
		return nil
	}); err != nil {
		return err
	}
	cancelActive()
	if err := read(first, true); err != nil {
		return err
	}
	last := open(context.Background())
	if err := onUI(ui, func() error {
		if ui.picker == nil {
			return fmt.Errorf("subsequent picker unavailable")
		}
		sendMessage.Call(ui.picker.edit, wmKeyDown, vkReturn, 0)
		return nil
	}); err != nil {
		return err
	}
	return read(last, false)
}
