package gioui

import (
	"image"
	v1 "quailcrate/pluginapi/v1"
)

// Each surface uses the same DP measurements and leaf rectangles. Extra space is
// distributed by grow; if a container is too small, its fixed children shrink.
func treeHeight(c v1.Control) int {
	if c.Height > 0 {
		return c.Height
	}
	switch c.Type {
	case "column":
		h := max(0, len(c.Children)-1) * c.Gap
		for _, child := range c.Children {
			h += treeHeight(child)
		}
		return h
	case "row":
		h := 0
		for _, child := range c.Children {
			h = max(h, treeHeight(child))
		}
		return h
	case "grid":
		rows := (max(len(c.Items), len(c.Children)) + max(1, c.Columns) - 1) / max(1, c.Columns)
		return rows * 32
	case "list":
		return 96
	case "text", "icon":
		return 22
	case "slider":
		return 22
	case "progress":
		return 3
	default:
		return 26
	}
}

func treeLeaves(c v1.Control, r image.Rectangle, dp func(int) int, leaf func(v1.Control, image.Rectangle)) {
	if r.Empty() {
		return
	}
	if c.Type == "grid" && len(c.Children) > 0 {
		cols := max(1, c.Columns)
		rows := (len(c.Children) + cols - 1) / cols
		gap := min(dp(c.Gap), r.Dx()/cols, r.Dy()/rows)
		w, h := r.Dx()-(cols-1)*gap, r.Dy()-(rows-1)*gap
		for i, child := range c.Children {
			col, row := i%cols, i/cols
			x, y := r.Min.X+col*gap, r.Min.Y+row*gap
			treeLeaves(child, image.Rect(x+col*w/cols, y+row*h/rows, x+(col+1)*w/cols, y+(row+1)*h/rows), dp, leaf)
		}
		return
	}
	if c.Type != "column" && c.Type != "row" {
		leaf(c, r)
		return
	}
	if len(c.Children) == 0 {
		return
	}
	horizontal := c.Type == "row"
	axis := r.Dy()
	if horizontal {
		axis = r.Dx()
	}
	gap := min(dp(c.Gap), axis/max(1, len(c.Children)-1))
	avail := axis - gap*(len(c.Children)-1)
	basis := func(child v1.Control) (fixed, grow int) {
		if child.Grow > 0 {
			return 0, child.Grow
		}
		if horizontal {
			if child.Width > 0 {
				return dp(child.Width), 0
			}
			return 0, 1
		}
		return dp(treeHeight(child)), 0
	}
	fixed, grow := 0, 0
	for _, child := range c.Children {
		f, g := basis(child)
		fixed += f
		grow += g
	}
	sumF, sumG, pos := 0, 0, 0
	for _, child := range c.Children {
		f, g := basis(child)
		before := sumF*min(avail, fixed)/max(1, fixed) + sumG*max(0, avail-fixed)/max(1, grow)
		sumF += f
		sumG += g
		after := sumF*min(avail, fixed)/max(1, fixed) + sumG*max(0, avail-fixed)/max(1, grow)
		part := r
		if horizontal {
			part.Min.X += pos
			part.Max.X = part.Min.X + after - before
		} else {
			part.Min.Y += pos
			part.Max.Y = part.Min.Y + after - before
		}
		treeLeaves(child, part, dp, leaf)
		pos += after - before + gap
	}
}

func (w *widgetLayout) drawLeaf(c v1.Control, r image.Rectangle) {
	switch c.Type {
	case "button":
		if c.Icon != "" {
			w.headerButton(c, r)
		} else {
			w.button(c, r)
		}
	case "text":
		if c.Variant == "caption" {
			w.label(r, c.Text, 11, false, true, w.Colors.Secondary, WidgetCenter)
			return
		}
		ink := w.Colors.Ink
		switch c.Variant {
		case "accent":
			ink = w.Colors.Accent
		case "muted":
			ink = w.Colors.Secondary
		case "warning":
			ink = w.Colors.Warning
		}
		w.label(r, c.Text, 12, false, false, ink, WidgetStart)
	case "icon":
		w.icon(r, c.Icon, w.Colors.Ink)
	case "progress":
		w.fill(r, w.alphaOf(w.Colors.Border, 100), 1)
		r.Max.X = r.Min.X + int(float64(r.Dx())*max(0, min(1, c.Value)))
		w.fill(r, w.Colors.Accent, 1)
	case "slider":
		val := max(0.0, min(1.0, c.Value))
		dragging := w.SliderDragControl == c.ID && c.ID != ""
		if dragging {
			val = max(0.0, min(1.0, w.SliderDragValue))
		}
		hitIdx := len(w.geometry.Hits)
		w.geometry.Hits = append(w.geometry.Hits, WidgetHit{
			Area:      r,
			ControlID: c.ID,
			Slider:    true,
			Value:     val,
		})
		px := w.px
		midY := (r.Min.Y + r.Max.Y) / 2
		trackH := max(3, px(4))
		knobR := max(4, px(5))
		if dragging || w.isHovered(hitIdx) || (w.KeyboardFocus && w.HasFocus && w.Focused == hitIdx) {
			knobR = max(5, px(6))
		}
		padX := knobR + px(1)
		trackL := r.Min.X + padX
		trackR := max(trackL+1, r.Max.X-padX)
		track := image.Rect(trackL, midY-trackH/2, trackR, midY-trackH/2+trackH)
		w.fill(track, w.alphaOf(w.Colors.Border, max(125, w.surfaceAlpha)), trackH/2)
		fillX := trackL + int(float64(trackR-trackL)*val+0.5)
		if fillX > trackL {
			w.fill(image.Rect(trackL, track.Min.Y, fillX, track.Max.Y), w.Colors.Accent, trackH/2)
		}
		knob := image.Rect(fillX-knobR, midY-knobR, fillX+knobR, midY+knobR)
		w.fill(knob, w.Colors.Ink, knobR)
		w.stroke(knob, w.Colors.Accent, knobR, max(1, px(2)))
		if w.KeyboardFocus && w.HasFocus && w.Focused == hitIdx {
			w.stroke(r.Inset(px(1)), w.Colors.Accent, 6, 1)
		}
	case "list":
		w.list(c, r)
	case "grid":
		cols := max(1, c.Columns)
		rows := (len(c.Items) + cols - 1) / cols
		if rows == 0 {
			return
		}
		for i, item := range c.Items {
			x, y := i%cols, i/cols
			cell := image.Rect(r.Min.X+x*r.Dx()/cols, r.Min.Y+y*r.Dy()/rows, r.Min.X+(x+1)*r.Dx()/cols, r.Min.Y+(y+1)*r.Dy()/rows)
			if !cell.Empty() {
				w.cell(c.ID, item, i, cell)
			}
		}
	}
}
