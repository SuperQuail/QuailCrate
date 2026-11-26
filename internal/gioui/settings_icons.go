package gioui

import (
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

func settingsIcon(gtx layout.Context, r image.Rectangle, name string, ink color.NRGBA) {
	DrawIcon(gtx, r, name, ink)
}

// DrawIcon renders a crisp 24-unit vector stroke icon sharp at any DPI scale.
func DrawIcon(gtx layout.Context, r image.Rectangle, name string, ink color.NRGBA) {
	if r.Empty() || ink.A == 0 {
		return
	}
	scale := float32(min(r.Dx(), r.Dy())) / 24
	ox := float32(r.Min.X) + (float32(r.Dx())-24*scale)/2
	oy := float32(r.Min.Y) + (float32(r.Dy())-24*scale)/2
	pos := func(x, y float32) f32.Point { return f32.Pt(ox+x*scale, oy+y*scale) }
	var path clip.Path
	path.Begin(gtx.Ops)
	line := func(coords ...float32) {
		path.MoveTo(pos(coords[0], coords[1]))
		for i := 2; i < len(coords); i += 2 {
			path.LineTo(pos(coords[i], coords[i+1]))
		}
	}
	round := func(x, y, radius float32) {
		// Four cubic arcs approximate a circle.
		k := radius * 0.552285
		path.MoveTo(pos(x+radius, y))
		path.CubeTo(pos(x+radius, y+k), pos(x+k, y+radius), pos(x, y+radius))
		path.CubeTo(pos(x-k, y+radius), pos(x-radius, y+k), pos(x-radius, y))
		path.CubeTo(pos(x-radius, y-k), pos(x-k, y-radius), pos(x, y-radius))
		path.CubeTo(pos(x+k, y-radius), pos(x+radius, y-k), pos(x+radius, y))
		path.Close()
	}
	switch name {
	case "search":
		round(14, 9, 6.5)
		line(9.5, 14, 2, 22)
	case "folder", "open-folder":
		line(2.5, 6, 2.5, 19, 21.5, 19, 21.5, 7.5, 11, 7.5, 9, 4.5, 2.5, 4.5, 2.5, 6)
		line(3, 9, 20.5, 9)
	case "folder-plus":
		line(2.5, 6, 2.5, 19, 21.5, 19, 21.5, 7.5, 11, 7.5, 9, 4.5, 2.5, 4.5, 2.5, 6)
		line(12, 11, 12, 16.5)
		line(9.2, 13.8, 14.8, 13.8)
	case "plus":
		line(12, 5, 12, 19)
		line(5, 12, 19, 12)
	case "edit":
		line(4, 20, 8.5, 19, 19.5, 8, 16, 4.5, 5, 15.5, 4, 20)
		line(13.8, 6.8, 17.2, 10.2)
	case "trash":
		line(4.5, 6.5, 19.5, 6.5)
		line(9, 6.5, 9, 4, 15, 4, 15, 6.5)
		line(6.5, 6.5, 7.5, 20, 16.5, 20, 17.5, 6.5)
		line(10, 10.5, 10, 16.5)
		line(14, 10.5, 14, 16.5)
	case "lock":
		line(5.5, 11, 18.5, 11, 18.5, 20, 5.5, 20, 5.5, 11)
		line(8.5, 11, 8.5, 7.5, 9.5, 5.2, 12, 4.5, 14.5, 5.2, 15.5, 7.5, 15.5, 11)
	case "unlock":
		line(5.5, 11, 18.5, 11, 18.5, 20, 5.5, 20, 5.5, 11)
		line(8.5, 11, 8.5, 7.5, 9.5, 5.2, 12, 4.5, 14.5, 5.2, 15.5, 7)
	case "arrow-up":
		line(12, 19, 12, 5)
		line(6.5, 10.5, 12, 5, 17.5, 10.5)
	case "arrow-down":
		line(12, 5, 12, 19)
		line(6.5, 13.5, 12, 19, 17.5, 13.5)
	case "chevron-left":
		line(14.5, 6, 8.5, 12, 14.5, 18)
	case "chevron-right":
		line(9.5, 6, 15.5, 12, 9.5, 18)
	case "chevron-down":
		line(6, 9.5, 12, 15.5, 18, 9.5)
	case "chevron-up":
		line(6, 14.5, 12, 8.5, 18, 14.5)
	case "command":
		line(4.5, 7, 10.5, 12, 4.5, 17)
		line(12.5, 17, 19.5, 17)
	case "apps":
		line(4, 4, 10.5, 4, 10.5, 10.5, 4, 10.5, 4, 4)
		line(13.5, 4, 20, 4, 20, 10.5, 13.5, 10.5, 13.5, 4)
		line(4, 13.5, 10.5, 13.5, 10.5, 20, 4, 20, 4, 13.5)
		line(13.5, 13.5, 20, 13.5, 20, 20, 13.5, 20, 13.5, 13.5)
	case "retry":
		round(12, 12, 7.5)
		line(16.5, 5.5, 19.5, 9, 15.2, 10.2)
	case "today":
		round(12, 12, 4.2)
	case "more":
		round(6, 12, 1.3)
		round(12, 12, 1.3)
		round(18, 12, 1.3)
	case "collapse":
		line(4.5, 6, 19.5, 6)
		line(7.5, 15.5, 12, 11, 16.5, 15.5)
	case "expand":
		line(4.5, 6, 19.5, 6)
		line(7.5, 11.5, 12, 16, 16.5, 11.5)
	case "sliders":
		line(4, 7, 20, 7)
		line(4, 17, 20, 17)
		round(9, 7, 2.2)
		round(15, 17, 2.2)
	case "layout":
		line(3.5, 4.5, 20.5, 4.5, 20.5, 19.5, 3.5, 19.5, 3.5, 4.5)
		line(10, 4.5, 10, 19.5)
		line(10, 12, 20.5, 12)
	case "rocket":
		line(12, 3.5, 16.5, 8.5, 16.5, 15, 12, 19.5, 7.5, 15, 7.5, 8.5, 12, 3.5)
		round(12, 10.5, 1.6)
	case "power":
		line(12, 3.5, 12, 11.5)
		line(7.2, 6.8, 5, 10, 5.2, 14.5, 8, 18.2, 12, 19.5, 16, 18.2, 18.8, 14.5, 19, 10, 16.8, 6.8)
	case "check":
		line(5, 12.5, 10, 17.5, 19.5, 7)
	case "globe":
		round(12, 12, 8.5)
		line(3.5, 12, 20.5, 12)
		line(12, 3.5, 9.2, 8, 9.2, 16, 12, 20.5)
		line(12, 3.5, 14.8, 8, 14.8, 16, 12, 20.5)
	case "terminal":
		line(3.5, 5, 20.5, 5, 20.5, 19, 3.5, 19, 3.5, 5)
		line(6.5, 9.5, 10, 12.5, 6.5, 15.5)
		line(12, 15.5, 17.5, 15.5)
	case "code":
		line(8.5, 7, 3.5, 12, 8.5, 17)
		line(15.5, 7, 20.5, 12, 15.5, 17)
		line(13.5, 5.5, 10.5, 18.5)
	case "pin":
		line(9, 4.5, 15, 4.5, 14, 10.5, 17.5, 13.5, 6.5, 13.5, 10, 10.5, 9, 4.5)
		line(12, 13.5, 12, 20.5)
	case "unpin":
		line(9, 4.5, 15, 4.5, 14, 10.5, 17.5, 13.5, 6.5, 13.5, 10, 10.5, 9, 4.5)
		line(12, 13.5, 12, 20.5)
		line(4.5, 4.5, 19.5, 19.5)
	case "grid":
		line(4, 4, 10, 4, 10, 10, 4, 10, 4, 4)
		line(14, 4, 20, 4, 20, 10, 14, 10, 14, 4)
		line(4, 14, 10, 14, 10, 20, 4, 20, 4, 14)
		line(14, 14, 20, 14, 20, 20, 14, 20, 14, 14)
	case "list":
		line(8.5, 7, 20, 7)
		line(8.5, 12, 20, 12)
		line(8.5, 17, 20, 17)
		round(5, 7, 1)
		round(5, 12, 1)
		round(5, 17, 1)
	case "shield":
		line(12, 3.5, 19, 6.5, 18, 13.5, 12, 20.5, 6, 13.5, 5, 6.5, 12, 3.5)
	case "sort":
		line(7.5, 5, 7.5, 19)
		line(4.5, 8.5, 7.5, 5, 10.5, 8.5)
		line(16.5, 19, 16.5, 5)
		line(13.5, 15.5, 16.5, 19, 19.5, 15.5)
	case "move":
		line(5, 12, 19, 12)
		line(14.5, 7.5, 19, 12, 14.5, 16.5)
	case "tab-plus":
		line(3.5, 6.5, 13, 6.5, 14.5, 9.5, 20.5, 9.5, 20.5, 18.5, 3.5, 18.5, 3.5, 6.5)
		line(12, 12, 12, 16)
		line(10, 14, 14, 14)
	case "refresh":
		round(12, 12, 7.5)
		line(16.5, 5.5, 19.5, 9, 15.2, 10.2)
	case "win-min":
		line(6, 12.5, 18, 12.5)
	case "win-max":
		line(6.5, 6.5, 17.5, 6.5, 17.5, 17.5, 6.5, 17.5, 6.5, 6.5)
	case "win-restore":
		line(6, 9, 15, 9, 15, 18, 6, 18, 6, 9)
		line(9, 9, 9, 6, 18, 6, 18, 15, 15, 15)
	case "win-close":
		line(6.5, 6.5, 17.5, 17.5)
		line(17.5, 6.5, 6.5, 17.5)
	case "calendar":
		line(2.5, 5.5, 21.5, 5.5, 21.5, 21, 2.5, 21, 2.5, 5.5)
		line(3, 9, 21, 9)
		line(7, 2.5, 7, 7)
		line(17, 2.5, 17, 7)
		for y := float32(12); y <= 18; y += 3 {
			for x := float32(6); x <= 17; x += 5 {
				line(x, y, x+1.5, y)
			}
		}
	case "image":
		line(2, 5, 22, 5, 22, 20, 2, 20, 2, 5)
		line(5, 8, 19, 8, 19, 17, 5, 17, 5, 8)
	case "desktop":
		line(2.5, 4.5, 21.5, 4.5, 21.5, 16.5, 2.5, 16.5, 2.5, 4.5)
		line(12, 17, 12, 20)
		line(7.5, 20, 16.5, 20)
	case "music":
		line(9.5, 17, 9.5, 5.5, 20, 3.8, 20, 15.2)
		line(9.5, 9, 20, 7.3)
		round(7, 17.5, 2.5)
		round(17.5, 15.8, 2.5)
	case "play":
		line(7, 5, 19, 12, 7, 19, 7, 5)
	case "pause":
		line(8, 5.5, 8, 18.5)
		line(16, 5.5, 16, 18.5)
	case "skip-prev":
		line(6.5, 6, 6.5, 18)
		line(18, 6.5, 10, 12, 18, 17.5, 18, 6.5)
	case "skip-next":
		line(17.5, 6, 17.5, 18)
		line(6, 6.5, 14, 12, 6, 17.5, 6, 6.5)
	case "repeat":
		line(4.5, 9, 18, 9, 15.5, 6.5)
		line(18, 9, 15.5, 11.5)
		line(19.5, 15, 6, 15, 8.5, 12.5)
		line(6, 15, 8.5, 17.5)
	case "shuffle":
		line(4.5, 7, 9, 7, 15, 17, 19.5, 17)
		line(17, 14.5, 19.5, 17, 17, 19.5)
		line(4.5, 17, 9, 17, 15, 7, 19.5, 7)
		line(17, 4.5, 19.5, 7, 17, 9.5)
	case "volume":
		line(4.5, 9.5, 8.5, 9.5, 13.5, 5.5, 13.5, 18.5, 8.5, 14.5, 4.5, 14.5, 4.5, 9.5)
		line(16.8, 9.2, 18.4, 12, 16.8, 14.8)
	case "palette":
		round(12, 12, 9)
		round(8.8, 9.8, 1.3)
		round(13.8, 8.2, 1.3)
		round(16.6, 12.2, 1.3)
		round(11.6, 15.6, 1.8)
	case "settings":
		const teeth = 8
		for i := 0; i < 4*teeth; i++ {
			radius := float32(8)
			if i%4 == 1 || i%4 == 2 {
				radius = 10.4
			}
			a := float64(i) * 2 * math.Pi / (4 * teeth)
			p := pos(12+radius*float32(math.Cos(a)), 12+radius*float32(math.Sin(a)))
			if i == 0 {
				path.MoveTo(p)
			} else {
				path.LineTo(p)
			}
		}
		path.Close()
		round(12, 12, 3.2)
	default:
		path.MoveTo(pos(9, 2.5))
		path.QuadTo(pos(5.5, 2.5), pos(6.2, 7))
		path.QuadTo(pos(7, 11), pos(3.5, 12))
		path.QuadTo(pos(7, 13), pos(6.2, 17))
		path.QuadTo(pos(5.5, 21.5), pos(9, 21.5))
		path.MoveTo(pos(15, 2.5))
		path.QuadTo(pos(18.5, 2.5), pos(17.8, 7))
		path.QuadTo(pos(17, 11), pos(20.5, 12))
		path.QuadTo(pos(17, 13), pos(17.8, 17))
		path.QuadTo(pos(18.5, 21.5), pos(15, 21.5))
	}
	paint.FillShape(gtx.Ops, ink, clip.Stroke{Path: path.End(), Width: 1.55 * scale}.Op())
}
