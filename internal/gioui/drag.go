package gioui

import (
	"image"
	"image/color"

	"quailcrate/internal/theme"
	v1 "quailcrate/pluginapi/v1"
)

// DragGhostOptions configures the floating translucent card that follows the
// pointer while dragging a desktop widget item across groups or tabs.
type DragGhostOptions struct {
	DPI         int
	Locale      string
	Colors      WidgetColors
	Item        v1.Item
	Icon        image.Image
	TargetBadge string
}

const dragGhostShadowPadDp = 8

func dragGhostTextUnits(s string) int {
	units := 0
	for _, r := range s {
		if r > 127 {
			units += 2
		} else {
			units++
		}
	}
	return units
}

func dragGhostDimensionsDp(opt DragGhostOptions) (cardW, cardH, iconDp int) {
	iconDp = max(28, min(52, ComputeIconMetrics(opt.Colors.IconSize).IconDp))
	cardW = max(94, iconDp+46)
	cardH = 10 + iconDp + 6 + 18 + 8
	if opt.TargetBadge != "" {
		badgeText := "→ " + opt.TargetBadge
		badgeNeed := min(216, max(cardW, 34+dragGhostTextUnits(badgeText)*6))
		if badgeNeed > cardW {
			cardW = badgeNeed
		}
		cardH += 26
	}
	return cardW, cardH, iconDp
}

// MeasureDragGhost returns the physical pixel dimensions for a floating drag preview.
func MeasureDragGhost(opt DragGhostOptions) image.Point {
	dpi := max(opt.DPI, 96)
	px := func(n int) int { return n * dpi / 96 }
	cardW, cardH, _ := dragGhostDimensionsDp(opt)
	return image.Pt(px(cardW+dragGhostShadowPadDp*2), px(cardH+dragGhostShadowPadDp*2))
}

// LayoutDragGhost draws a translucent lifted icon card (and optional target badge)
// onto canvas so users get immediate visual feedback when moving items between groups.
func LayoutDragGhost(opt DragGhostOptions, canvas WidgetCanvas) image.Rectangle {
	dpi := max(opt.DPI, 96)
	size := MeasureDragGhost(opt)
	cardW, cardH, iconDp := dragGhostDimensionsDp(opt)
	var icons map[string]image.Image
	if opt.Icon != nil && opt.Item.Icon != "" {
		icons = map[string]image.Image{opt.Item.Icon: opt.Icon}
	}
	w := widgetLayout{
		WidgetOptions: WidgetOptions{
			DPI:    dpi,
			Size:   size,
			Colors: opt.Colors,
			Icons:  icons,
		},
		canvas:       canvas,
		surfaceAlpha: 232,
		glassStyle:   theme.NormalizeGlassStyle(opt.Colors.GlassStyle),
		isDark:       theme.Luminance(opt.Colors.Surface) < 0.45,
	}
	px := w.px
	line := w.line()
	pad := px(dragGhostShadowPadDp)
	card := image.Rect(pad, pad, pad+px(cardW), pad+px(cardH))
	if canvas == nil {
		return card
	}

	// Soft elevation shadow beneath the floating card.
	w.fill(card.Add(image.Pt(0, px(5))), color.NRGBA{R: 0, G: 0, B: 0, A: 26}, 12)
	w.fill(card.Add(image.Pt(0, px(2))), color.NRGBA{R: 0, G: 0, B: 0, A: 42}, 12)

	// Frosted lifted card surface with glowing Accent border.
	c := opt.Colors
	w.fill(card, w.alphaOf(c.Surface, 234), 12)
	if w.isDark {
		sheenInset := max(line, px(w.rad(12)))
		if card.Dx() > sheenInset*2 {
			w.fill(image.Rect(card.Min.X+sheenInset, card.Min.Y+line, card.Max.X-sheenInset, card.Min.Y+line*2),
				color.NRGBA{R: 255, G: 255, B: 255, A: 42}, 0)
		}
	}
	borderAlpha := uint8(215)
	if opt.TargetBadge != "" {
		borderAlpha = 250
	}
	w.stroke(card, w.alphaOf(c.Accent, borderAlpha), 12, 1)

	// Centered item icon.
	icSize := px(iconDp)
	icX := (card.Min.X + card.Max.X - icSize) / 2
	icY := card.Min.Y + px(10)
	icRect := image.Rect(icX, icY, icX+icSize, icY+icSize)
	w.drawItemIcon(icRect, opt.Item)

	// Item title below icon.
	labelTop := icRect.Max.Y + px(5)
	labelRect := image.Rect(card.Min.X+px(8), labelTop, card.Max.X-px(8), labelTop+px(18))
	w.label(labelRect, opt.Item.Text, 11, true, false, c.Ink, WidgetCenter)

	// Destination pill badge when hovering another group or tab.
	if opt.TargetBadge != "" {
		pillH := px(20)
		pillRect := image.Rect(card.Min.X+px(8), card.Max.Y-px(8)-pillH, card.Max.X-px(8), card.Max.Y-px(8))
		onAccent := c.OnAccent
		if onAccent.A == 0 {
			onAccent = theme.ContrastInk(c.Accent)
		}
		w.fill(pillRect, w.alphaOf(c.Accent, 240), 6)
		w.label(pillRect.Inset(px(3)), "→ "+opt.TargetBadge, 10, true, false, onAccent, WidgetCenter)
	}
	return card
}
