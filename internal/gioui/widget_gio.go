package gioui

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"quailcrate/internal/theme"
)

// GioWidgetCanvas records a public plugin widget directly into a Gio frame.
// Its context may come from a visible Gio window or an offscreen target.
type GioWidgetCanvas struct {
	Context      layout.Context
	Shaper       *text.Shaper
	Typeface     font.Typeface
	MonoTypeface font.Typeface
}

func (c GioWidgetCanvas) DrawSpatialGlass(spec SpatialGlassSpec) {
	DrawSpatialGlassOps(c.Context.Ops, spec)
}

func (c GioWidgetCanvas) Fill(r image.Rectangle, col color.NRGBA, radius int) {
	if r.Empty() || col.A == 0 {
		return
	}
	if radius <= 0 {
		paint.FillShape(c.Context.Ops, col, clip.Rect(r).Op())
		return
	}
	paint.FillShape(c.Context.Ops, col, clip.UniformRRect(r, min(radius, r.Dx()/2, r.Dy()/2)).Op(c.Context.Ops))
}

func (c GioWidgetCanvas) Stroke(r image.Rectangle, col color.NRGBA, radius, width int) {
	if r.Empty() || col.A == 0 || width <= 0 {
		return
	}
	half := max(1, width/2)
	inset := r.Inset(half)
	if inset.Empty() {
		inset = r
	}
	rad := 0
	if radius > 0 {
		rad = min(max(0, radius-half), inset.Dx()/2, inset.Dy()/2)
	}
	path := clip.UniformRRect(inset, rad).Path(c.Context.Ops)
	paint.FillShape(c.Context.Ops, col, clip.Stroke{Path: path, Width: float32(width)}.Op())
}

func (c GioWidgetCanvas) Image(r image.Rectangle, img image.Image) {
	if img == nil || r.Empty() {
		return
	}
	b := img.Bounds()
	if b.Empty() {
		return
	}
	defer clip.Rect(r).Push(c.Context.Ops).Pop()
	defer op.Offset(r.Min).Push(c.Context.Ops).Pop()
	sx := float32(r.Dx()) / float32(b.Dx())
	sy := float32(r.Dy()) / float32(b.Dy())
	scale := sx
	if sy < scale {
		scale = sy
	}
	ox := (float32(r.Dx()) - float32(b.Dx())*scale) / 2
	oy := (float32(r.Dy()) - float32(b.Dy())*scale) / 2
	t := f32.AffineId().Scale(f32.Point{}, f32.Pt(scale, scale)).Offset(f32.Pt(ox, oy))
	defer op.Affine(t).Push(c.Context.Ops).Pop()
	imgOp := paint.NewImageOp(img)
	imgOp.Filter = paint.FilterLinear
	imgOp.Add(c.Context.Ops)
	paint.PaintOp{}.Add(c.Context.Ops)
}

func (c GioWidgetCanvas) Icon(r image.Rectangle, name string, col color.NRGBA) {
	DrawIcon(c.Context, r, name, col)
}

func (c GioWidgetCanvas) Label(r image.Rectangle, value string, style WidgetText) {
	if r.Empty() || value == "" {
		return
	}
	gtx := c.Context
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	defer op.Offset(r.Min).Push(gtx.Ops).Pop()
	gtx.Constraints = layout.Exact(r.Size())
	direction := layout.W
	if style.Align == WidgetCenter {
		direction = layout.Center
	}
	if style.Align == WidgetEnd {
		direction = layout.E
	}
	tf := c.Typeface
	if style.Mono && c.MonoTypeface != "" {
		tf = c.MonoTypeface
	}
	f := font.Font{Typeface: tf}
	if style.Bold {
		f.Weight = font.SemiBold
	}
	drawText := func(gtx layout.Context, col color.NRGBA) layout.Dimensions {
		ink := op.Record(gtx.Ops)
		paint.ColorOp{Color: col}.Add(gtx.Ops)
		return (widget.Label{MaxLines: 1}).Layout(gtx, c.Shaper, f, unit.Sp(style.Size), value, ink.Stop())
	}
	direction.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		// Clear minimums so the alignment widget gets the measured label height.
		gtx.Constraints.Min = image.Point{}
		if style.Shadow {
			shadowCol := color.NRGBA{R: 0, G: 0, B: 0, A: 185}
			if theme.Luminance(style.Color) < 0.35 {
				shadowCol = color.NRGBA{R: 255, G: 255, B: 255, A: 145}
			}
			off := op.Offset(image.Pt(0, max(1, gtx.Dp(1)))).Push(gtx.Ops)
			drawText(gtx, shadowCol)
			off.Pop()
		}
		return drawText(gtx, style.Color)
	})
}
