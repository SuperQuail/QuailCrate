package gioui

import (
	"image"
	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
	"testing"
)

func TestNodeLayoutFollowsBindingAndPlacement(t *testing.T) {
	view := v1.View{UIVersion: 2, Root: &v2.Node{Type: "row", ID: "bar", Props: v2.Props{Gap: 5}, Children: []v2.Node{
		{Type: "button", ID: "renamed", Props: v2.Props{Text: "Back", Width: 40}, Events: map[string]string{"activate": "month.previous"}},
		{Type: "column", ID: "right", Props: v2.Props{Grow: 1}, Children: []v2.Node{
			{Type: "button", ID: "jump", Props: v2.Props{Text: "Today"}, Events: map[string]string{"activate": "month.reset"}},
			{Type: "progress", ID: "fraction", Props: v2.Props{Height: 2, Value: 0.5}},
		}},
	}}}
	for _, dpi := range []int{96, 144, 192} {
		opt := WidgetOptions{Size: image.Pt(330*dpi/96, 280*dpi/96), DPI: dpi}
		geometry := LayoutWidget(view, opt, nil)
		if len(geometry.Hits) != 2 {
			t.Fatalf("hits %v", geometry.Hits)
		}
		left, right := geometry.Hits[0], geometry.Hits[1]
		if left.ControlID != "renamed" || left.Area.Dx() != 40*dpi/96 || right.Area.Min.X-left.Area.Max.X != 5*dpi/96 || left.Area.Max.Y <= right.Area.Max.Y {
			t.Fatalf("dpi %d, layout %+v", dpi, geometry)
		}
	}
}
