//go:build interactive && windows

package winui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
	"unsafe"

	"quailcrate/internal/plugins"
	v1 "quailcrate/pluginapi/v1"
)

// Exercise the actual native popup, its child EDIT and the installed Lua row
// template. UpdateWindow and GdiFlush measure submission, not monitor latency.
func TestLauncherNavigationPresentation(t *testing.T) {
	var shell LauncherShell
	for _, pkg := range plugins.Discover(filepath.Join("..", "..", "plugins"), "", nil) {
		if pkg.Manifest.ID != "quailcrate.launcher" {
			continue
		}
		mgr := plugins.NewManager()
		resp, err := mgr.Enable(context.Background(), pkg, pkg.Manifest.Permissions, v1.Payload{Locale: "en-US"})
		if err != nil {
			t.Fatal(err)
		}
		shell = LauncherShell{pkg.Manifest.ID, pkg.Manifest.Name, resp.Data.Launcher}
		if err := mgr.Disable(context.Background(), pkg.Manifest.ID); err != nil {
			t.Fatal(err)
		}
	}
	if shell.View == nil {
		t.Fatal("bundled launcher did not register")
	}
	var ui *UI
	var testErr error
	ui = New(Callbacks{Ready: func() {
		ui.SetLaunchers([]LauncherShell{shell}, shell)
		ui.ShowLauncher()
		results := make([]Result, 25)
		for i := range results {
			key := fmt.Sprintf("app-%02d", i+1)
			results[i] = Result{ID: key, PluginID: "provider", Generation: 1, Revision: 1, Title: key, Subtitle: "Packaged app", Icon: "imgfile:" + key}
		}
		ui.post(func() {
			ui.ensureIconCache()
			for i, r := range results {
				ic := image.NewRGBA(image.Rect(0, 0, 16, 16))
				for j := 0; j < len(ic.Pix); j += 4 {
					ic.Pix[j], ic.Pix[j+1], ic.Pix[j+2], ic.Pix[j+3] = uint8(i*9), 17, 238, 255
				}
				ui.icons.images[r.Icon] = ic
			}
		})
		ui.SetResults(1, "", results, "")
		ui.post(func() {
			defer ui.Close()
			update := user32.NewProc("UpdateWindow")
			getUpdate := user32.NewProc("GetUpdateRect")
			update.Call(ui.launcher)
			gdiFlush.Call()
			pixel := func(row int32) uintptr {
				b := offsetRow(ui.launcherLayout.icon, row*ui.launcherLayout.rowHeight)
				dc, _, _ := user32.NewProc("GetDC").Call(ui.launcher)
				defer user32.NewProc("ReleaseDC").Call(ui.launcher, dc)
				value, _, _ := gdi32.NewProc("GetPixel").Call(dc, coord((b.Left+b.Right)/2), coord((b.Top+b.Bottom)/2))
				return value
			}
			var samples []time.Duration
			// Alternate wheel navigation and keys through the full list in both directions.
			for leg := 0; leg < 4; leg++ {
				for k := 0; k < len(results)-1; k++ {
					index, key, delta := k+1, uintptr(vkDown), int16(-120)
					if leg%2 == 1 {
						index, key, delta = len(results)-k-2, vkUp, 120
					}
					start := time.Now()
					if k%2 == 0 {
						sendMessage.Call(ui.launcher, wmMouseWheel, uintptr(uint16(delta))<<16, 0)
					} else {
						sendMessage.Call(ui.edit, wmKeyDown, key, 0)
					}
					update.Call(ui.launcher)
					gdiFlush.Call()
					samples = append(samples, time.Since(start))
					if got, want := pixel(2), uintptr(0xee1100+(ui.resultScroll+2)*9); got != want {
						testErr = fmt.Errorf("presented row icon %#x, want %#x", got, want)
						return
					}
					if ui.resultIndex != index || index < ui.resultScroll || index >= ui.resultScroll+5 {
						testErr = fmt.Errorf("selection %d, scroll %d; expected %d in view", ui.resultIndex, ui.resultScroll, index)
						return
					}
				}
			}
			slices.Sort(samples)
			t.Logf("25 icons, 5 rows, %d inputs: native input to GDI submission median %s, p95 %s", len(samples), samples[len(samples)/2], samples[len(samples)*95/100])
			// Keep icon lookup waiting while a second thread samples the window, both
			// for a selection change and for a full scrolled viewport repaint.
			tid, _, _ := kernel32.NewProc("GetCurrentThreadId").Call()
			getDesktop := user32.NewProc("GetThreadDesktop")
			desktop, _, _ := getDesktop.Call(tid)
			setDesktop := user32.NewProc("SetThreadDesktop")
			for _, index := range []int{1, 5} {
				ui.selectResult(index - ui.resultIndex)
				ui.icons.mu.Lock()
				sampled := make(chan uintptr, 1)
				go func() {
					runtime.LockOSThread()
					defer runtime.UnlockOSThread()
					tid, _, _ := kernel32.NewProc("GetCurrentThreadId").Call()
					original, _, _ := getDesktop.Call(tid)
					if ok, _, _ := setDesktop.Call(desktop); ok == 0 {
						ui.icons.mu.Unlock()
						sampled <- ^uintptr(0)
						return
					}
					defer setDesktop.Call(original)
					time.Sleep(25 * time.Millisecond)
					sampled <- pixel(2)
					ui.icons.mu.Unlock()
				}()
				update.Call(ui.launcher)
				if mid := <-sampled; mid&0xffff00 != 0xee1100 {
					testErr = fmt.Errorf("intermediate frame lost app icon for selection %d: %#x", index, mid)
					return
				}
			}
			ui.selectResult(-ui.resultIndex)
			update.Call(ui.launcher)
			bitmap := ui.launcherFrame.dibBitmap
			if bitmap == 0 {
				testErr = fmt.Errorf("missing persistent frame")
				return
			}
			// Half detents and boundary keys have no painted region.
			sendMessage.Call(ui.launcher, wmMouseWheel, uintptr(uint16(60))<<16, 0)
			sendMessage.Call(ui.edit, wmKeyDown, vkUp, 0)
			if dirty, _, _ := getUpdate.Call(ui.launcher, 0, 0); dirty != 0 {
				testErr = fmt.Errorf("unchanged selection invalidated window")
				return
			}
			nextIcon := image.NewRGBA(image.Rect(0, 0, 16, 16))
			nextIcon.SetRGBA(0, 0, color.RGBA{G: 255, A: 255})
			ui.icons.images[results[1].Icon] = nextIcon
			ui.repaintLauncherIcons([]string{results[1].Icon})
			var dirty rect
			getUpdate.Call(ui.launcher, uintptr(unsafe.Pointer(&dirty)), 0)
			want := offsetRow(ui.launcherLayout.icon, ui.launcherLayout.rowHeight)
			if dirty != want {
				testErr = fmt.Errorf("icon update dirtied %+v, want %+v", dirty, want)
				return
			}
			update.Call(ui.launcher)
			if ui.launcherFrame.dibBitmap != bitmap {
				testErr = fmt.Errorf("replaced frame while scrolling")
				return
			}
			setText(ui.edit, "1")
			single := results[0]
			single.Title, single.Subtitle = "OneNote for Windows 10", "打包应用"
			ui.SetResults(2, "1", []Result{single}, "")
			ui.hostProcedure(wmDispatch, 0, 0)
			update.Call(ui.launcher)
			for _, delta := range []int16{-120, 120} {
				sendMessage.Call(ui.launcher, wmMouseWheel, uintptr(uint16(delta))<<16, 0)
				sendMessage.Call(ui.edit, wmKeyDown, vkDown, 0)
				if dirty, _, _ := getUpdate.Call(ui.launcher, 0, 0); dirty != 0 ||
					len(ui.results) != 1 || ui.resultIndex != 0 || pixel(0) != 0xee1100 {
					testErr = fmt.Errorf("single-result boundary repainted or lost its app icon")
					return
				}
			}
		})
	}})
	if err := ui.Run(); err != nil {
		t.Fatal(err)
	}
	if testErr != nil {
		t.Fatal(testErr)
	}
}
