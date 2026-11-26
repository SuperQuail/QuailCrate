//go:build interactive && windows

package winui

import (
	"image"
	"testing"

	"quailcrate/internal/gioui"
	v1 "quailcrate/pluginapi/v1"
)

// Rendering with and without a title also catches accidentally recording a
// text material into a different Gio op list or failing to load system fonts.
func TestGioWidgetRendersTitle(t *testing.T) {
	r := widgetRenderer{}
	defer r.release()
	opt := gioui.WidgetOptions{DPI: 120, Size: image.Pt(375, 220), Colors: widgetColors()}
	for _, tc := range []struct{ locale, title string }{{"en-US", "Archive"}, {"zh-CN", "桌面分组"}} {
		var with, without widgetFrame
		if err := r.render(v1.View{}, tc.locale, opt, &without); err != nil {
			t.Fatal(err)
		}
		if err := r.render(v1.View{Title: tc.title}, tc.locale, opt, &with); err != nil {
			t.Fatal(err)
		}
		var difference int
		for y := 1; y < 45; y++ {
			for x := 10; x < 200; x++ {
				if without.image.RGBAAt(x, y) != with.image.RGBAAt(x, y) {
					difference++
				}
			}
		}
		if difference < 40 {
			t.Errorf("%s: only %d changed title pixels", tc.locale, difference)
		}
	}
}
