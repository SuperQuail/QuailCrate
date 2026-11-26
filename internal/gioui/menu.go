package gioui

import (
	"image"
	"image/color"
	"strings"
	"unicode/utf8"

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

// MenuItem represents a single entry or separator inside a geek glass popup menu.
type MenuItem struct {
	ID        string
	Label     string
	Shortcut  string
	Icon      string
	Separator bool
	Checked   bool
	Danger    bool
	Disabled  bool
}

// MenuOptions configures rendering of a custom floating context/tray menu.
type MenuOptions struct {
	DPI             int
	Locale          string
	Palette         theme.Palette
	HasHover        bool
	Hovered         int
	CornerRadius    int
	HasCornerRadius bool
	Viewport        MenuViewport
	Scroll          int
}

const (
	menuShadowPadDp = 10
	menuPadYDp      = 6
	menuItemHDp     = 32
	menuSepHDp      = 9
	menuScrollHDp   = 20
	menuMinWidthDp  = 212
	menuMaxWidthDp  = 292
)

func menuPx(dp, dpi int) int {
	return dp * max(dpi, 1) / 96
}

// MeasureMenu returns the physical pixel dimensions (including soft shadow margin)
// required to draw items at dpi.
func MeasureMenu(items []MenuItem, dpi int) image.Point {
	pad := menuPx(menuShadowPadDp, dpi)
	innerH := menuPx(menuPadYDp*2, dpi)
	maxChars := 12
	for _, it := range items {
		if it.Separator {
			innerH += menuPx(menuSepHDp, dpi)
			continue
		}
		innerH += menuPx(menuItemHDp, dpi)
		// Approximate CJK runes as ~1.7x width of ASCII.
		units := 0
		for _, r := range it.Label {
			if r > 127 {
				units += 2
			} else {
				units++
			}
		}
		if it.Shortcut != "" {
			units += utf8.RuneCountInString(it.Shortcut) + 4
		}
		if units > maxChars {
			maxChars = units
		}
	}
	wDp := max(menuMinWidthDp, min(menuMaxWidthDp, 78+maxChars*8))
	return image.Pt(menuPx(wDp, dpi)+pad*2, innerH+pad*2)
}

// MenuViewport holds the fixed popup geometry and the positions of its unscrolled rows.
// All dimensions and scroll offsets are in physical pixels.
type MenuViewport struct {
	Size      image.Point
	MaxScroll int
	RowHeight int
	Up, Down  image.Rectangle

	card    image.Rectangle
	content image.Rectangle
	rows    []menuRow
}

type menuRow struct {
	bounds     image.Rectangle
	selectable bool
}

// NewMenuViewport reserves scroll controls only when the natural menu exceeds
// maxHeight. A nonpositive height leaves the menu unconstrained.
func NewMenuViewport(items []MenuItem, dpi, maxHeight int) MenuViewport {
	dpi = max(dpi, 96)
	px := func(dp int) int { return menuPx(dp, dpi) }
	size := MeasureMenu(items, dpi)
	overflow := maxHeight > 0 && size.Y > maxHeight
	if overflow {
		size.Y = maxHeight
	}
	pad := px(menuShadowPadDp)
	padY := min(pad, (size.Y-1)/2)
	v := MenuViewport{
		Size: size, RowHeight: px(menuItemHDp),
		card: image.Rect(pad, padY, size.X-pad, size.Y-padY),
		rows: make([]menuRow, len(items)),
	}
	marginY := min(px(menuPadYDp), (v.card.Dy()-1)/2)
	v.content = image.Rect(v.card.Min.X+px(6), v.card.Min.Y+marginY, v.card.Max.X-px(6), v.card.Max.Y-marginY)
	if overflow {
		arrowH := min(px(menuScrollHDp), v.content.Dy()/3)
		v.Up = v.content
		v.Up.Max.Y = v.Up.Min.Y + arrowH
		v.Down = v.content
		v.Down.Min.Y = v.Down.Max.Y - arrowH
		v.content.Min.Y += arrowH
		v.content.Max.Y -= arrowH
	}
	y := v.content.Min.Y
	for i, it := range items {
		height := v.RowHeight
		if it.Separator {
			height = px(menuSepHDp)
		}
		v.rows[i] = menuRow{
			bounds:     image.Rect(v.content.Min.X, y, v.content.Max.X, y+height),
			selectable: !it.Separator && !it.Disabled,
		}
		y += height
	}
	v.MaxScroll = max(0, y-v.content.Max.Y)
	return v
}

// Reveal returns the nearest scroll offset that brings the given row into view.
func (v MenuViewport) Reveal(index, scroll int) int {
	scroll = min(max(scroll, 0), v.MaxScroll)
	if index < 0 || index >= len(v.rows) {
		return scroll
	}
	row := v.rows[index].bounds
	return min(max(scroll, row.Max.Y-v.content.Max.Y), row.Min.Y-v.content.Min.Y)
}

// HitIndex returns the original index of a visible, selectable item, or -1.
func (v MenuViewport) HitIndex(pt image.Point, scroll int) int {
	if !pt.In(v.content) {
		return -1
	}
	pt.Y += min(max(scroll, 0), v.MaxScroll)
	for i, row := range v.rows {
		if row.selectable && pt.In(row.bounds) {
			return i
		}
	}
	return -1
}

// LayoutMenu renders a glassmorphic floating menu into gtx (which may be nil to
// compute hit rectangles only) and returns the hit rectangle for each MenuItem.
func LayoutMenu(gtx *layout.Context, shaper *text.Shaper, items []MenuItem, opt MenuOptions) []image.Rectangle {
	dpi := max(opt.DPI, 96)
	px := func(dp int) int { return menuPx(dp, dpi) }
	v := opt.Viewport
	if v.Size == (image.Point{}) {
		v = NewMenuViewport(items, dpi, 0)
	}
	card := v.card
	hits := make([]image.Rectangle, len(items))

	p := opt.Palette
	baseR := theme.DefaultCornerRadius
	if opt.HasCornerRadius {
		baseR = opt.CornerRadius
	}
	cardR := px(theme.ScaleRadius(baseR, 10))
	rowR := px(theme.ScaleRadius(baseR, 6))
	badgeR := px(theme.ScaleRadius(baseR, 4))
	barR := px(theme.ScaleRadius(baseR, 2))
	if gtx != nil {
		// Multi-pass feathered outer drop shadow for genuine floating elevation.
		drawSoftShadow(gtx.Ops, card, cardR, dpi, p.IsDark())

		// Smoked translucent glass body.
		surfaceAlpha := uint8(236)
		if !p.IsDark() {
			surfaceAlpha = 244
		}
		paint.FillShape(gtx.Ops, theme.WithAlpha(p.Surface, surfaceAlpha), clip.UniformRRect(card, cardR).Op(gtx.Ops))

		// Subtle 1px specular top glass highlight rim.
		if p.IsDark() {
			rimInset := max(1, cardR)
			rim := image.Rect(card.Min.X+rimInset, card.Min.Y+max(1, px(1)), card.Max.X-rimInset, card.Min.Y+max(2, px(2)))
			paint.FillShape(gtx.Ops, color.NRGBA{R: 255, G: 255, B: 255, A: 30}, clip.Rect(rim).Op())
		}

		// Crisp outer 1px border.
		borderPath := clip.UniformRRect(card.Inset(max(1, px(1)/2)), cardR).Path(gtx.Ops)
		paint.FillShape(gtx.Ops, theme.WithAlpha(p.Border, 235), clip.Stroke{Path: borderPath, Width: float32(max(1, px(1)))}.Op())
	}

	uiFace := font.Typeface("Segoe UI Variable Text, Segoe UI, Microsoft YaHei UI, sans-serif")
	if strings.HasPrefix(opt.Locale, "zh") {
		uiFace = "Microsoft YaHei UI, Segoe UI Variable Text, Segoe UI, sans-serif"
	}
	monoFace := font.Typeface("Cascadia Mono, JetBrains Mono, Consolas, Microsoft YaHei UI, monospace")

	line := max(1, px(1))
	scroll := min(max(opt.Scroll, 0), v.MaxScroll)
	if gtx != nil && v.MaxScroll > 0 {
		drawArrow := func(area image.Rectangle, icon string, enabled bool) {
			d := min(px(18), area.Dy())
			x, y := (area.Min.X+area.Max.X-d)/2, (area.Min.Y+area.Max.Y-d)/2
			ink := p.Secondary
			if !enabled {
				ink = theme.WithAlpha(ink, 75)
			}
			DrawIcon(*gtx, image.Rect(x, y, x+d, y+d), icon, ink)
		}
		drawArrow(v.Up, "chevron-up", scroll > 0)
		drawArrow(v.Down, "chevron-down", scroll < v.MaxScroll)
		defer clip.Rect(v.content).Push(gtx.Ops).Pop()
	}

	for i, it := range items {
		row := v.rows[i].bounds.Sub(image.Pt(0, scroll))
		visible := row.Intersect(v.content)
		if visible.Empty() {
			continue
		}
		if it.Separator {
			if gtx != nil {
				sy := row.Min.Y + row.Dy()/2
				paint.FillShape(gtx.Ops, theme.WithAlpha(p.Border, 150), clip.Rect(image.Rect(card.Min.X+px(12), sy, card.Max.X-px(12), sy+line)).Op())
			}
			continue
		}
		if !it.Disabled {
			hits[i] = visible
		}
		if gtx != nil && shaper != nil {
			hovered := opt.HasHover && opt.Hovered == i && !it.Disabled
			ink := p.Text
			iconInk := p.Secondary
			shortInk := p.Secondary
			if it.Checked {
				iconInk = p.Accent
			}
			if it.Danger {
				ink = p.Warning
				iconInk = p.Warning
			}
			if it.Disabled {
				ink = theme.WithAlpha(p.Secondary, 130)
				iconInk = ink
			}
			if hovered {
				pillBg := theme.WithAlpha(p.Selected, 220)
				barCol := p.Accent
				if it.Danger {
					pillBg = theme.WithAlpha(p.WarningBg, 235)
					barCol = p.Warning
					ink = p.Warning
					iconInk = p.Warning
				} else {
					ink = p.Text
					iconInk = p.Accent
				}
				paint.FillShape(gtx.Ops, pillBg, clip.UniformRRect(row, rowR).Op(gtx.Ops))
				bar := image.Rect(row.Min.X+px(2), row.Min.Y+px(6), row.Min.X+px(2)+max(2, px(2)), row.Max.Y-px(6))
				paint.FillShape(gtx.Ops, barCol, clip.UniformRRect(bar, barR).Op(gtx.Ops))
			}

			// Left vector icon
			icSize := px(15)
			icX := row.Min.X + px(11)
			icY := (row.Min.Y + row.Max.Y - icSize) / 2
			iconName := it.Icon
			if it.Checked && iconName == "" {
				iconName = "check"
			}
			if iconName != "" {
				DrawIcon(*gtx, image.Rect(icX, icY, icX+icSize, icY+icSize), iconName, iconInk)
			}

			textLeft := icX + icSize + px(10)
			textRight := row.Max.X - px(10)
			if it.Shortcut != "" {
				shortW := px(72)
				shortRect := image.Rect(max(textLeft, textRight-shortW), row.Min.Y, textRight, row.Max.Y)
				drawMenuLabel(*gtx, shaper, shortRect, it.Shortcut, monoFace, 10.5, font.Medium, shortInk, layout.E)
				textRight = shortRect.Min.X - px(6)
			} else if it.Checked {
				badge := image.Rect(textRight-px(28), row.Min.Y+px(7), textRight, row.Max.Y-px(7))
				paint.FillShape(gtx.Ops, theme.WithAlpha(p.Accent, 42), clip.UniformRRect(badge, badgeR).Op(gtx.Ops))
				drawMenuLabel(*gtx, shaper, badge, "ON", monoFace, 9.5, font.Bold, p.Accent, layout.Center)
				textRight = badge.Min.X - px(6)
			}

			weight := font.Normal
			if hovered {
				weight = font.Medium
			}
			drawMenuLabel(*gtx, shaper, image.Rect(textLeft, row.Min.Y, max(textLeft, textRight), row.Max.Y), it.Label, uiFace, 12.5, weight, ink, layout.W)
		}
	}
	return hits
}

func drawMenuLabel(gtx layout.Context, shaper *text.Shaper, r image.Rectangle, value string, tf font.Typeface, size unit.Sp, weight font.Weight, col color.NRGBA, dir layout.Direction) {
	if r.Empty() || value == "" {
		return
	}
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	defer op.Offset(r.Min).Push(gtx.Ops).Pop()
	gtx.Constraints = layout.Exact(r.Size())
	dir.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		ink := op.Record(gtx.Ops)
		paint.ColorOp{Color: col}.Add(gtx.Ops)
		f := font.Font{Typeface: tf, Weight: weight}
		return (widget.Label{MaxLines: 1}).Layout(gtx, shaper, f, size, value, ink.Stop())
	})
}

// drawSoftShadow renders a lightweight multi-pass feathered elevation shadow
// into a transparent per-pixel RGBA canvas.
func drawSoftShadow(ops *op.Ops, card image.Rectangle, radius, dpi int, dark bool) {
	px := func(dp int) int { return dp * max(dpi, 1) / 96 }
	baseAlpha := 34
	if !dark {
		baseAlpha = 22
	}
	layers := [4]struct {
		expand, offsetY, alpha int
	}{
		{8, 4, baseAlpha / 4},
		{5, 3, baseAlpha / 3},
		{3, 2, baseAlpha / 2},
		{1, 1, baseAlpha},
	}
	for _, l := range layers {
		e := px(l.expand)
		dy := px(l.offsetY)
		sr := image.Rect(card.Min.X-e, card.Min.Y-e/2+dy, card.Max.X+e, card.Max.Y+e+dy)
		paint.FillShape(ops, color.NRGBA{R: 0, G: 0, B: 0, A: uint8(l.alpha)}, clip.UniformRRect(sr, radius+e/2).Op(ops))
	}
}
