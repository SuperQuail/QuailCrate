package gioui

import (
	"hash/fnv"
	"image"
	"image/color"
	"math"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"

	"quailcrate/internal/theme"
	v1 "quailcrate/pluginapi/v1"
)

// The host uses the same physical rectangles for native file drops, mouse
// input and drawing. A nil canvas runs only the hit-testing part of layout.
type WidgetCanvas interface {
	Fill(image.Rectangle, color.NRGBA, int)
	Stroke(image.Rectangle, color.NRGBA, int, int)
	Image(image.Rectangle, image.Image)
	Icon(image.Rectangle, string, color.NRGBA)
	Label(image.Rectangle, string, WidgetText)
}

type WidgetText struct {
	Color  color.NRGBA
	Size   int // sp
	Bold   bool
	Mono   bool
	Shadow bool
	Align  WidgetAlign
}

type WidgetAlign int

const (
	WidgetStart WidgetAlign = iota
	WidgetCenter
	WidgetEnd
)

const WidgetTitleHeight = 40

type WidgetColors struct {
	Background, Surface, Ink, Secondary, Border, Accent, Selection, Hover, Weekend, Warning, OnAccent, OnSelection color.NRGBA
	Opacity                                                                                                        int    // 0..100 when Translucent is true
	Translucent                                                                                                    bool   // enables per-pixel surface alpha for wallpaper visibility
	GlassStyle                                                                                                     string // "smoked" | "wireframe" | "minimal"
	CornerRadius                                                                                                   int    // global outer corner radius in dp (0..20)
	HasCornerRadius                                                                                                bool
	TitleAccentBar                                                                                                 bool // optional decorative vertical bar left of widget title
	IconSize                                                                                                       int  // desktop widget icon size in dp (22..72; 0 uses default 40)
}

// IconMetrics describes the proportional icon-grid and compact-list geometry
// derived from a configured desktop icon size in dp.
type IconMetrics struct {
	IconDp     int
	MinTileW   int
	TileH      int
	PadY       int
	LabelH     int
	FontSize   int
	TileRadius int
	ListIconDp int
	ListRowH   int
}

// ComputeIconMetrics derives responsive grid and compact-list dimensions from iconDp.
func ComputeIconMetrics(iconDp int) IconMetrics {
	if iconDp <= 0 {
		iconDp = theme.DefaultIconSize
	}
	if iconDp < theme.MinIconSize {
		iconDp = theme.MinIconSize
	}
	if iconDp > theme.MaxIconSize {
		iconDp = theme.MaxIconSize
	}
	padX := max(12, int(math.Round(float64(iconDp)*0.48)))
	minTileW := max(iconDp+padX*2, iconDp+30)
	fontSize := 13
	switch {
	case iconDp <= 28:
		fontSize = 10
	case iconDp <= 42:
		fontSize = 11
	case iconDp <= 56:
		fontSize = 12
	}
	labelH := fontSize + 10
	padY := max(7, int(math.Round(float64(iconDp)*0.18)))
	tileH := iconDp + padY*2 + labelH + 4
	tileRadius := max(6, min(14, int(math.Round(float64(iconDp)*0.22))))
	listIconDp := max(18, min(36, int(math.Round(float64(iconDp)*0.60))))
	listRowH := max(36, listIconDp+16)
	return IconMetrics{
		IconDp:     iconDp,
		MinTileW:   minTileW,
		TileH:      tileH,
		PadY:       padY,
		LabelH:     labelH,
		FontSize:   fontSize,
		TileRadius: tileRadius,
		ListIconDp: listIconDp,
		ListRowH:   listRowH,
	}
}

type WidgetOptions struct {
	DPI               int
	Size              image.Point
	Colors            WidgetColors
	MoveHint          string
	HasFocus          bool
	KeyboardFocus     bool
	Focused           int
	HasHover          bool
	Hovered           int
	MouseInside       bool
	Collapsed         bool
	Locked            bool
	DropControl       string
	DropIndex         int
	DraggingItemID    string
	SliderDragControl string
	SliderDragValue   float64
	Scroll            map[string]int
	Icons             map[string]image.Image
	Backdrop          *SpatialBackdrop
	Elevated          bool
}

type WidgetHit struct {
	Area              image.Rectangle
	ControlID, ItemID string
	Menu              string
	Index, Total      int
	Reorder, Drop     bool
	Slider            bool
	Value             float64
}

type WidgetScrollArea struct {
	Area      image.Rectangle
	ControlID string
	Max       int
}
type WidgetGeometry struct {
	Hits, DropZones []WidgetHit
	ScrollAreas     []WidgetScrollArea
}

type widgetLayout struct {
	WidgetOptions
	canvas       WidgetCanvas
	geometry     WidgetGeometry
	surfaceAlpha uint8
	shadowText   bool
	glassStyle   string
	isDark       bool
}

func LayoutWidget(view v1.View, options WidgetOptions, canvas WidgetCanvas) WidgetGeometry {
	alpha := uint8(255)
	shadow := false
	if options.Colors.Translucent {
		op := max(0, min(100, options.Colors.Opacity))
		alpha = uint8(op * 255 / 100)
		shadow = op < 52
	}
	w := widgetLayout{
		WidgetOptions: options,
		canvas:        canvas,
		surfaceAlpha:  alpha,
		shadowText:    shadow,
		glassStyle:    theme.NormalizeGlassStyle(options.Colors.GlassStyle),
		isDark:        theme.Luminance(options.Colors.Surface) < 0.45,
	}
	w.draw(view)
	return w.geometry
}

func (w *widgetLayout) px(n int) int { return n * max(w.DPI, 1) / 96 }
func (w *widgetLayout) line() int    { return max(1, w.px(1)) }
func (w *widgetLayout) rad(baseDp int) int {
	if baseDp <= 0 {
		return 0
	}
	r := theme.DefaultCornerRadius
	if w.Colors.HasCornerRadius {
		r = w.Colors.CornerRadius
	}
	return theme.ScaleRadius(r, baseDp)
}

func (w *widgetLayout) fill(r image.Rectangle, c color.NRGBA, radius int) {
	if w.canvas != nil && !r.Empty() && c.A > 0 {
		w.canvas.Fill(r, c, w.px(w.rad(radius)))
	}
}

func (w *widgetLayout) stroke(r image.Rectangle, c color.NRGBA, radius, width int) {
	if w.canvas != nil && !r.Empty() && c.A > 0 {
		w.canvas.Stroke(r, c, w.px(w.rad(radius)), max(1, w.px(width)))
	}
}

func (w *widgetLayout) icon(r image.Rectangle, name string, c color.NRGBA) {
	if w.canvas != nil && !r.Empty() && name != "" && c.A > 0 {
		w.canvas.Icon(r, name, c)
	}
}

func (w *widgetLayout) label(r image.Rectangle, s string, size int, bold, mono bool, c color.NRGBA, a WidgetAlign) {
	if w.canvas != nil && s != "" && !r.Empty() {
		w.canvas.Label(r, s, WidgetText{
			Color:  c,
			Size:   size,
			Bold:   bold,
			Mono:   mono,
			Shadow: w.shadowText,
			Align:  a,
		})
	}
}

func (w *widgetLayout) alphaOf(c color.NRGBA, a uint8) color.NRGBA {
	return theme.WithAlpha(c, a)
}

func (w *widgetLayout) scaleAlpha(a uint8, pct int, floor uint8) uint8 {
	if !w.Colors.Translucent {
		return a
	}
	v := int(w.surfaceAlpha) * pct / 100
	if v < int(floor) {
		v = int(floor)
	}
	if v > 255 {
		v = 255
	}
	return uint8(v)
}

func (w *widgetLayout) isHovered(hitIdx int) bool {
	return w.HasHover && w.Hovered == hitIdx
}

func (w *widgetLayout) isKeyboardFocused(hitIdx int) bool {
	return w.HasFocus && w.KeyboardFocus && w.Focused == hitIdx
}

func (w *widgetLayout) draw(view v1.View) {
	px := w.px
	line := w.line()
	width, height := w.Size.X, w.Size.Y
	if width < px(32) || height < px(32) {
		return
	}
	c := w.Colors
	isSpatialGlass := w.Colors.Translucent && w.glassStyle == theme.GlassSmoked
	padL, padT, padR, padB := 0, 0, 0, 0
	if isSpatialGlass {
		padL, padT, padR, padB = SpatialGlassPadding(w.Size, w.DPI, w.Colors.Translucent, w.surfaceAlpha)
	}
	frame := image.Rect(padL, padT, width-padR, height-padB)

	if isSpatialGlass {
		const spatialRadDp = 18
		var borderOverride color.NRGBA
		if w.MoveHint != "" || (w.DropControl != "" && w.DraggingItemID == "") {
			borderOverride = w.alphaOf(c.Accent, 240)
		}
		if w.surfaceAlpha == 0 {
			// Preserve alpha=1 click hit-testing at 0% pure-float opacity.
			w.fill(frame, w.alphaOf(c.Surface, 1), spatialRadDp)
			if borderOverride.A > 0 {
				w.stroke(frame, borderOverride, spatialRadDp, 1)
			}
		} else if sgc, ok := w.canvas.(SpatialGlassCanvas); ok {
			sgc.DrawSpatialGlass(SpatialGlassSpec{
				CanvasSize:     w.Size,
				Frame:          frame,
				Radius:         px(w.rad(spatialRadDp)),
				DPI:            w.DPI,
				Surface:        c.Surface,
				BorderOverride: borderOverride,
				Opacity:        w.Colors.Opacity,
				SurfaceAlpha:   w.surfaceAlpha,
				IsDark:         w.isDark,
				Elevated:       w.Elevated,
				Backdrop:       w.Backdrop,
			})
		} else {
			// Fallback for non-GPU canvases (GDI fallback & test spy).
			w.fill(frame, w.alphaOf(c.Surface, w.surfaceAlpha), spatialRadDp)
			borderCol := borderOverride
			if borderCol.A == 0 {
				if w.isDark {
					borderCol = color.NRGBA{R: 255, G: 255, B: 255, A: uint8(max(18, int(w.surfaceAlpha)*34/255))}
				} else {
					borderCol = color.NRGBA{R: 255, G: 255, B: 255, A: uint8(max(120, int(w.surfaceAlpha)*217/255))}
				}
			}
			w.stroke(frame, borderCol, spatialRadDp, 1)
		}
	} else {
		// Unified single-layer surface for Terminal HUD / Minimal Float / opaque fallback.
		shellAlpha := w.surfaceAlpha
		if w.Colors.Translucent && shellAlpha == 0 {
			shellAlpha = 1
		}
		w.fill(frame, w.alphaOf(c.Surface, shellAlpha), 14)

		// Subtle top 1px specular sheen on dark Terminal HUD panels.
		if w.isDark && w.surfaceAlpha >= 18 && w.glassStyle != theme.GlassMinimal {
			sheenA := uint8(min(44, max(14, int(w.surfaceAlpha)/5)))
			sheenInset := max(line, px(w.rad(14)))
			w.fill(image.Rect(frame.Min.X+sheenInset, frame.Min.Y+line, frame.Max.X-sheenInset, frame.Min.Y+line*2), color.NRGBA{R: 255, G: 255, B: 255, A: sheenA}, 0)
		}

		// Outer border according to glass style.
		borderCol := c.Border
		if w.MoveHint != "" || (w.DropControl != "" && w.DraggingItemID == "") {
			borderCol = w.alphaOf(c.Accent, 240)
		} else if w.Colors.Translucent {
			switch w.glassStyle {
			case theme.GlassMinimal:
				borderCol = w.alphaOf(c.Border, 0)
			case theme.GlassWireframe:
				if w.surfaceAlpha == 0 {
					borderCol = w.alphaOf(c.Accent, 130)
				} else {
					borderCol = w.alphaOf(theme.Blend(c.Border, c.Accent, 0.45), uint8(min(255, max(120, int(w.surfaceAlpha)+75))))
				}
			}
		}
		w.stroke(frame, borderCol, 14, 1)

		// Geek HUD corner brackets for Wireframe style.
		if w.glassStyle == theme.GlassWireframe && w.MoveHint == "" {
			w.drawCornerBrackets(frame, w.alphaOf(c.Accent, 210))
		}
	}

	tree := view.LayoutTree()
	headerControls := tree.Header

	top := frame.Min.Y
	headerH := min(px(WidgetTitleHeight), frame.Dy())
	headerBottom := top + headerH
	left, right := frame.Min.X+px(14), frame.Max.X-px(14)

	// Optional left HUD vertical accent indicator.
	titleLeft := left
	if c.TitleAccentBar {
		barW := max(2, px(3))
		w.fill(image.Rect(left, top+px(13), left+barW, headerBottom-px(13)), c.Accent, 2)
		titleLeft = left + barW + px(9)
	}
	titleRight := right

	if w.Collapsed {
		if view.Summary != "" {
			sumW := min(px(108), max(px(58), frame.Dx()/4))
			sumRect := image.Rect(max(titleLeft, right-sumW), top+px(9), right, headerBottom-px(9))
			w.fill(sumRect, w.alphaOf(c.Accent, 28), 6)
			w.label(sumRect, view.Summary, 11, true, true, c.Accent, WidgetCenter)
			titleRight = sumRect.Min.X - px(8)
		}
		if w.Locked {
			lockR := image.Rect(max(titleLeft, titleRight-px(14)), top+(headerH-px(14))/2, titleRight, top+(headerH+px(14))/2)
			w.icon(lockR, "lock", c.Secondary)
			titleRight = lockR.Min.X - px(6)
		}
		if len(view.Tabs) > 1 {
			w.drawInlineTabs(view.Tabs, view.ActiveTab, titleLeft, titleRight, top, headerBottom)
		} else {
			w.label(image.Rect(titleLeft, top, max(titleLeft, titleRight), headerBottom), view.Title, 14, true, false, c.Ink, WidgetStart)
		}
		return
	}

	// Render right-aligned inline header buttons (e.g., Calendar ‹ ● › or Groups hover + / ⋯).
	if len(headerControls) > 0 {
		btnSize := px(24)
		btnGap := px(4)
		cursorX := right
		for idx := len(headerControls) - 1; idx >= 0; idx-- {
			btn := headerControls[idx]
			if btn.HoverOnly && !w.MouseInside && !w.HasFocus {
				continue
			}
			bw := btnSize
			if btn.Icon == "" && len([]rune(btn.Text)) > 2 {
				bw = px(44)
			}
			br := image.Rect(cursorX-bw, top+(headerH-btnSize)/2, cursorX, top+(headerH+btnSize)/2)
			if br.Min.X < titleLeft+px(40) {
				break
			}
			w.headerButton(btn, br)
			cursorX = br.Min.X - btnGap
		}
		titleRight = cursorX - px(4)
	}

	if w.MoveHint != "" {
		badgeW := px(62)
		badge := image.Rect(max(titleLeft, titleRight-badgeW), top+px(10), titleRight, headerBottom-px(10))
		w.fill(badge, w.alphaOf(c.Accent, 36), 5)
		w.stroke(badge, w.alphaOf(c.Accent, 180), 5, 1)
		w.label(badge, w.MoveHint, 10, true, true, c.Accent, WidgetCenter)
		titleRight = badge.Min.X - px(6)
	} else if w.Locked {
		lockR := image.Rect(max(titleLeft, titleRight-px(14)), top+(headerH-px(14))/2, titleRight, top+(headerH+px(14))/2)
		w.icon(lockR, "lock", w.alphaOf(c.Secondary, 180))
		titleRight = lockR.Min.X - px(6)
	}

	if len(view.Tabs) > 1 {
		w.drawInlineTabs(view.Tabs, view.ActiveTab, titleLeft, titleRight, top, headerBottom)
	} else {
		w.label(image.Rect(titleLeft, top, max(titleLeft, titleRight), headerBottom), view.Title, 14, true, false, c.Ink, WidgetStart)
	}

	treeLeaves(tree.Body, image.Rect(left, headerBottom+px(8), right, frame.Max.Y-px(10)), px, w.drawLeaf)

	// Subtle geek resize corner grip cue when hovering an unlocked widget.
	if w.MouseInside && !w.Locked && w.canvas != nil {
		gripCol := w.alphaOf(c.Secondary, 145)
		gx, gy := frame.Max.X-px(5), frame.Max.Y-px(5)
		w.fill(image.Rect(gx-px(5), gy, gx, gy+line), gripCol, 0)
		w.fill(image.Rect(gx, gy-px(5), gx+line, gy+line), gripCol, 0)
	}
}

func (w *widgetLayout) drawCornerBrackets(r image.Rectangle, col color.NRGBA) {
	arm := w.px(10)
	t := max(2, w.px(2))
	// Top-left
	w.fill(image.Rect(r.Min.X, r.Min.Y, r.Min.X+arm, r.Min.Y+t), col, 0)
	w.fill(image.Rect(r.Min.X, r.Min.Y, r.Min.X+t, r.Min.Y+arm), col, 0)
	// Top-right
	w.fill(image.Rect(r.Max.X-arm, r.Min.Y, r.Max.X, r.Min.Y+t), col, 0)
	w.fill(image.Rect(r.Max.X-t, r.Min.Y, r.Max.X, r.Min.Y+arm), col, 0)
	// Bottom-left
	w.fill(image.Rect(r.Min.X, r.Max.Y-t, r.Min.X+arm, r.Max.Y), col, 0)
	w.fill(image.Rect(r.Min.X, r.Max.Y-arm, r.Min.X+t, r.Max.Y), col, 0)
	// Bottom-right
	w.fill(image.Rect(r.Max.X-arm, r.Max.Y-t, r.Max.X, r.Max.Y), col, 0)
	w.fill(image.Rect(r.Max.X-t, r.Max.Y-arm, r.Max.X, r.Max.Y), col, 0)
}

// drawInlineTabs renders the tab strip inside the widget header.
func (w *widgetLayout) drawInlineTabs(tabs []v1.WidgetTab, activeTab string, left, right, top, headerBottom int) {
	if right-left < w.px(44) {
		return
	}
	c := w.Colors
	px := w.px
	tabGap := px(4)
	// Leave at least 24dip on the right as a natural drag handle whenever space allows.
	maxX := right
	if right-left > px(180) {
		maxX = right - px(18)
	}
	curX := left
	for idx, tab := range tabs {
		units := 0
		for _, r := range tab.Title {
			if r > 127 {
				units += 2
			} else {
				units++
			}
		}
		tabW := max(px(44), min(px(108), px(18)+units*px(7)))
		if curX+tabW > maxX {
			if idx == 0 {
				tabW = max(px(36), maxX-curX)
			} else {
				break
			}
		}
		pill := image.Rect(curX, top+px(6), curX+tabW, headerBottom-px(6))
		hitIdx := len(w.geometry.Hits)
		hovered := w.isHovered(hitIdx)
		focused := w.isKeyboardFocused(hitIdx)
		isActive := tab.ID == activeTab || (activeTab == "" && idx == 0)
		isDropTarget := w.DropControl == "__tab__" && w.DropIndex == idx
		w.geometry.Hits = append(w.geometry.Hits, WidgetHit{
			Area:      image.Rect(curX, top, curX+tabW, headerBottom),
			ControlID: "__tab__",
			ItemID:    tab.ID,
			Index:     idx,
			Total:     len(tabs),
			Drop:      true,
		})

		ink := c.Secondary
		bold := false
		if isActive {
			w.fill(pill, w.alphaOf(c.Selection, max(125, w.surfaceAlpha)), 7)
			ink = c.Ink
			bold = true
			// Signature 2px Accent underline bar along the header bottom.
			barH := max(2, px(2))
			w.fill(image.Rect(pill.Min.X+px(6), headerBottom-barH, pill.Max.X-px(6), headerBottom), c.Accent, 1)
		} else if hovered {
			w.fill(pill, w.alphaOf(c.Hover, max(115, w.surfaceAlpha)), 7)
			ink = c.Ink
		}
		if isDropTarget {
			w.fill(pill, w.alphaOf(c.Accent, 46), 7)
			w.stroke(pill, c.Accent, 7, 1)
			ink = c.Accent
			bold = true
		} else if focused {
			w.stroke(pill, c.Accent, 7, 1)
		}
		w.label(pill.Inset(px(4)), tab.Title, 12, bold, false, ink, WidgetCenter)
		curX += tabW + tabGap
	}
}

func (w *widgetLayout) headerButton(btn v1.Control, r image.Rectangle) {
	c := w.Colors
	hitIdx := len(w.geometry.Hits)
	hovered := w.isHovered(hitIdx)
	focused := w.isKeyboardFocused(hitIdx)
	w.geometry.Hits = append(w.geometry.Hits, WidgetHit{Area: r, ControlID: btn.ID, Menu: btn.Menu})

	ink := c.Secondary
	if btn.Variant == "accent" {
		ink = c.Accent
	}
	if hovered {
		w.fill(r, w.alphaOf(c.Hover, max(160, w.surfaceAlpha)), 6)
		w.stroke(r, w.alphaOf(c.Accent, 170), 6, 1)
		ink = c.Accent
	} else if focused {
		w.fill(r, w.alphaOf(c.Selection, max(180, w.surfaceAlpha)), 6)
		w.stroke(r, c.Accent, 6, 1)
		ink = c.OnSelection
	}
	if btn.Icon != "" {
		pad := w.px(5)
		w.icon(r.Inset(pad), btn.Icon, ink)
	} else {
		w.label(r, btn.Text, 11, true, true, ink, WidgetCenter)
	}
}

func isGridHeaderState(state string) bool {
	return state == "header" || state == "header-weekend"
}

func (w *widgetLayout) button(btn v1.Control, r image.Rectangle) {
	c := w.Colors
	hitIdx := len(w.geometry.Hits)
	hovered := w.isHovered(hitIdx)
	focused := w.isKeyboardFocused(hitIdx)
	w.geometry.Hits = append(w.geometry.Hits, WidgetHit{Area: r, ControlID: btn.ID, Menu: btn.Menu})

	bg := w.alphaOf(c.Background, w.scaleAlpha(255, 65, 22))
	border := w.alphaOf(c.Border, w.scaleAlpha(255, 105, 65))
	ink := c.Ink
	if btn.Variant == "accent" {
		ink = c.Accent
	}
	if hovered {
		bg = w.alphaOf(c.Hover, max(165, w.surfaceAlpha))
		border = w.alphaOf(c.Accent, 210)
		ink = c.Accent
	}
	if focused {
		bg = w.alphaOf(c.Selection, max(185, w.surfaceAlpha))
		border = c.Accent
		ink = c.OnSelection
	}
	w.fill(r, bg, 6)
	w.stroke(r, border, 6, 1)
	w.label(r.Inset(w.line()), btn.Text, 12, true, false, ink, WidgetCenter)
}

func (w *widgetLayout) cell(id string, item v1.Item, index int, r image.Rectangle) {
	c := w.Colors
	px := w.px
	ink, bold, mono, size := c.Ink, false, true, 13
	hovered, focused := false, false
	if item.ID != "" && !item.Header() {
		hitIdx := len(w.geometry.Hits)
		hovered = w.isHovered(hitIdx)
		focused = w.isKeyboardFocused(hitIdx)
		w.geometry.Hits = append(w.geometry.Hits, WidgetHit{Area: r, ControlID: id, ItemID: item.ID, Index: index})
	}

	badgeSide := min(max(px(22), r.Dx()-px(6)), max(px(22), r.Dy()-px(2)))
	cx, cy := (r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2
	badge := image.Rect(cx-badgeSide/2, cy-badgeSide/2, cx+(badgeSide+1)/2, cy+(badgeSide+1)/2).Intersect(r)

	if item.Header() {
		ink, bold, size = c.Secondary, true, 11
	}
	switch item.State {
	case "current":
		// Soft ambient glow halo behind Today's pill.
		halo := image.Rect(badge.Min.X-px(2), badge.Min.Y-px(2), badge.Max.X+px(2), badge.Max.Y+px(2)).Intersect(r)
		w.fill(halo, w.alphaOf(c.Accent, 56), 9)
		w.fill(badge, c.Accent, 7)
		ink, bold = c.OnAccent, true
	case "selected":
		w.fill(badge, w.alphaOf(c.Selection, max(180, w.surfaceAlpha)), 7)
		w.stroke(badge, c.Accent, 7, 1)
		ink, bold = c.Accent, true
	case "weekend":
		ink = c.Weekend
		if ink.A == 0 {
			ink = c.Accent
		}
	case "muted":
		ink = w.alphaOf(c.Secondary, 135)
		if hovered {
			ink = c.Ink
		}
	case "error":
		ink = c.Warning
	case "header":
		ink, bold, size = c.Secondary, true, 11
	case "header-weekend":
		ink, bold, size = c.Weekend, true, 11
		if ink.A == 0 {
			ink = c.Accent
		}
	}

	if hovered && item.State != "current" && item.State != "selected" {
		w.fill(badge, w.alphaOf(c.Hover, max(155, w.surfaceAlpha)), 7)
		w.stroke(badge, w.alphaOf(c.Border, 190), 7, 1)
	}
	if focused && item.State != "current" {
		w.stroke(badge, c.Accent, 7, 1)
	}
	w.label(r, item.Text, size, bold, mono, ink, WidgetCenter)
}

func (w *widgetLayout) list(control v1.Control, r image.Rectangle) {
	if r.Empty() {
		return
	}
	if control.Columns > 1 {
		w.iconGrid(control, r)
		return
	}
	c := w.Colors
	px := w.px
	line := w.line()
	if control.Drop {
		w.geometry.DropZones = append(w.geometry.DropZones, WidgetHit{Area: r, ControlID: control.ID, Index: len(control.Items), Drop: true})
		if w.DropControl == control.ID && w.DraggingItemID == "" {
			w.fill(r, w.alphaOf(c.Selection, max(90, w.surfaceAlpha/2)), 8)
			w.stroke(r, c.Accent, 8, 1)
		}
	}
	if len(control.Items) == 0 {
		w.stroke(r, w.alphaOf(c.Border, w.scaleAlpha(150, 80, 45)), 8, 1)
		w.label(r.Inset(px(10)), control.Text, 12, false, false, c.Secondary, WidgetCenter)
		return
	}
	metrics := ComputeIconMetrics(w.Colors.IconSize)
	rowH := max(1, px(metrics.ListRowH))
	rows := max(1, (r.Dy()-2*line)/rowH)
	limit := max(0, len(control.Items)-rows)
	w.geometry.ScrollAreas = append(w.geometry.ScrollAreas, WidgetScrollArea{Area: r, ControlID: control.ID, Max: limit})
	start := max(0, min(w.Scroll[control.ID], limit))
	if w.Scroll != nil {
		w.Scroll[control.ID] = start
	}
	end := min(start+rows, len(control.Items))
	for i, item := range control.Items[start:end] {
		index := start + i
		top := r.Min.Y + line + i*rowH
		row := image.Rect(r.Min.X+line, top, r.Max.X-line, min(top+rowH, r.Max.Y-line))
		if row.Empty() {
			break
		}
		hitIdx := len(w.geometry.Hits)
		hovered := w.isHovered(hitIdx)
		focused := w.isKeyboardFocused(hitIdx)

		ink, secondary := c.Ink, c.Secondary
		switch item.State {
		case "error":
			ink, secondary = c.Warning, c.Warning
		case "muted":
			ink = c.Secondary
		}
		isDragged := w.DraggingItemID != "" && item.ID == w.DraggingItemID
		if isDragged {
			w.fill(row.Inset(px(2)), w.alphaOf(c.Selection, 52), 7)
			w.stroke(row.Inset(px(2)), w.alphaOf(c.Accent, 120), 7, 1)
			ink = w.alphaOf(ink, 95)
			secondary = w.alphaOf(secondary, 85)
		} else if item.State == "current" || focused {
			w.fill(row.Inset(px(2)), w.alphaOf(c.Selection, max(175, w.surfaceAlpha)), 7)
			w.fill(image.Rect(row.Min.X+px(2), row.Min.Y+px(6), row.Min.X+px(5), row.Max.Y-px(6)), c.Accent, 2)
			ink = c.OnSelection
		} else if hovered {
			w.fill(row.Inset(px(2)), w.alphaOf(c.Hover, max(145, w.surfaceAlpha)), 7)
		}
		w.geometry.Hits = append(w.geometry.Hits, WidgetHit{
			Area: row, ControlID: control.ID, ItemID: item.ID,
			Index: index, Total: len(control.Items), Reorder: control.Reorder, Drop: control.Drop,
		})
		pad := px(8)
		textLeft := row.Min.X + pad
		if item.Icon != "" {
			icSize := min(px(metrics.ListIconDp), max(px(16), row.Dy()-px(8)))
			icY := (row.Min.Y + row.Max.Y - icSize) / 2
			icRect := image.Rect(textLeft, icY, textLeft+icSize, icY+icSize)
			w.drawItemIcon(icRect, item)
			if isDragged {
				w.fill(icRect, w.alphaOf(c.Surface, 145), 6)
			}
			textLeft += icSize + px(9)
		}
		textRight := row.Max.X - pad
		if item.Badge != "" {
			badgeUnits := 0
			for _, r := range item.Badge {
				if r > 127 {
					badgeUnits += 2
				} else {
					badgeUnits++
				}
			}
			badgeW := max(px(40), min(px(96), px(14)+badgeUnits*px(6)))
			badgeH := px(20)
			badgeTop := (row.Min.Y + row.Max.Y - badgeH) / 2
			if textRight-badgeW > textLeft+px(64) {
				badgeR := image.Rect(textRight-badgeW, badgeTop, textRight, badgeTop+badgeH)
				w.fill(badgeR, w.alphaOf(c.Accent, 24), 5)
				w.stroke(badgeR, w.alphaOf(c.Accent, 56), 5, 1)
				w.label(badgeR.Inset(px(3)), item.Badge, 10, true, true, c.Accent, WidgetCenter)
				textRight = badgeR.Min.X - px(8)
			}
		}
		if item.Hint == "" {
			labelRect := image.Rect(textLeft, row.Min.Y+px(1), textRight, row.Max.Y)
			w.label(labelRect, item.Text, 13, false, false, ink, WidgetStart)
		} else {
			midY := (row.Min.Y + row.Max.Y) / 2
			titleRect := image.Rect(textLeft, max(row.Min.Y+px(1), midY-px(17)), textRight, min(row.Max.Y, midY+px(2)))
			hintRect := image.Rect(textLeft, max(row.Min.Y, midY+px(1)), textRight, min(row.Max.Y-px(2), midY+px(17)))
			w.label(titleRect, item.Text, 13, false, false, ink, WidgetStart)
			w.label(hintRect, item.Hint, 11, false, true, secondary, WidgetStart)
		}
		if w.DropControl == control.ID && w.DropIndex == index {
			w.fill(image.Rect(row.Min.X+px(4), top, row.Max.X-px(4), top+max(2, px(2))), c.Accent, 1)
		}
	}
	if w.DropControl == control.ID && w.DropIndex == end {
		y := min(r.Max.Y-max(2, px(2)), r.Min.Y+line+(end-start)*rowH)
		w.fill(image.Rect(r.Min.X+px(4), y, r.Max.X-px(4), y+max(2, px(2))), c.Accent, 1)
	}
}

// iconGrid renders a Stardock Fences-style translucent icon grid floating cleanly
// on the unified glass surface with dynamic column count on resize.
func (w *widgetLayout) iconGrid(control v1.Control, r image.Rectangle) {
	c := w.Colors
	px := w.px
	if control.Drop {
		w.geometry.DropZones = append(w.geometry.DropZones, WidgetHit{Area: r, ControlID: control.ID, Index: len(control.Items), Drop: true})
		if w.DropControl == control.ID && w.DraggingItemID == "" {
			w.fill(r, w.alphaOf(c.Selection, max(90, w.surfaceAlpha/2)), 8)
			w.stroke(r, c.Accent, 8, 1)
		}
	}
	if len(control.Items) == 0 {
		w.stroke(r, w.alphaOf(c.Border, w.scaleAlpha(150, 80, 45)), 8, 1)
		w.label(r.Inset(px(10)), control.Text, 12, false, false, c.Secondary, WidgetCenter)
		return
	}
	// Dynamically adapt columns (2..8) and tile geometry to the configured icon size and widget width.
	m := ComputeIconMetrics(w.Colors.IconSize)
	minTileW := max(1, px(m.MinTileW))
	cols := max(2, min(8, r.Dx()/minTileW))
	cellH := max(px(52), px(m.TileH))
	visibleRows := max(1, r.Dy()/cellH)
	totalRows := (len(control.Items) + cols - 1) / cols
	limit := max(0, totalRows-visibleRows)
	w.geometry.ScrollAreas = append(w.geometry.ScrollAreas, WidgetScrollArea{Area: r, ControlID: control.ID, Max: limit})
	startRow := max(0, min(w.Scroll[control.ID], limit))
	if w.Scroll != nil {
		w.Scroll[control.ID] = startRow
	}
	startIdx := startRow * cols
	endIdx := min(len(control.Items), (startRow+visibleRows)*cols)
	for i := startIdx; i < endIdx; i++ {
		item := control.Items[i]
		rel := i - startIdx
		row, col := rel/cols, rel%cols
		x0 := r.Min.X + r.Dx()*col/cols
		x1 := r.Min.X + r.Dx()*(col+1)/cols
		y0 := r.Min.Y + row*cellH
		y1 := min(y0+cellH, r.Max.Y)
		tile := image.Rect(x0, y0, x1, y1)
		if tile.Empty() {
			continue
		}
		hitIdx := len(w.geometry.Hits)
		hovered := w.isHovered(hitIdx)
		focused := w.isKeyboardFocused(hitIdx)
		w.geometry.Hits = append(w.geometry.Hits, WidgetHit{
			Area: tile, ControlID: control.ID, ItemID: item.ID,
			Index: i, Total: len(control.Items), Reorder: control.Reorder, Drop: control.Drop,
		})

		card := tile.Inset(px(3))
		ink := c.Ink
		if item.State == "error" {
			ink = c.Warning
		}
		isDragged := w.DraggingItemID != "" && item.ID == w.DraggingItemID
		if isDragged {
			w.fill(card, w.alphaOf(c.Selection, 52), m.TileRadius)
			w.stroke(card, w.alphaOf(c.Accent, 120), m.TileRadius, 1)
			ink = w.alphaOf(ink, 95)
		} else if item.State == "current" || focused {
			w.fill(card, w.alphaOf(c.Selection, max(175, w.surfaceAlpha)), m.TileRadius)
			w.stroke(card, c.Accent, m.TileRadius, 1)
			ink = c.OnSelection
		} else if hovered {
			w.fill(card, w.alphaOf(c.Hover, max(135, w.surfaceAlpha)), m.TileRadius)
			w.stroke(card, w.alphaOf(c.Border, 180), m.TileRadius, 1)
		} else if item.State == "error" {
			w.stroke(card, w.alphaOf(c.Warning, 180), m.TileRadius, 1)
		}

		labelReserve := px(m.LabelH + 4)
		iconSize := min(px(m.IconDp), max(px(16), card.Dx()-px(6)))
		iconSize = min(iconSize, max(px(16), card.Dy()-labelReserve))
		iconX := (card.Min.X + card.Max.X - iconSize) / 2
		iconY := card.Min.Y + px(max(4, m.PadY-2))
		if hovered && !isDragged {
			iconY = max(card.Min.Y+px(2), iconY-px(1))
		}
		iconRect := image.Rect(iconX, iconY, iconX+iconSize, iconY+iconSize)
		w.drawItemIcon(iconRect, item)
		if isDragged {
			w.fill(iconRect, w.alphaOf(c.Surface, 145), m.TileRadius)
		}

		if item.State == "error" {
			dot := max(px(6), iconSize/5)
			w.fill(image.Rect(iconRect.Max.X-dot, iconRect.Min.Y, iconRect.Max.X, iconRect.Min.Y+dot), c.Warning, 4)
		}

		labelRect := image.Rect(card.Min.X+px(2), iconRect.Max.Y+px(3), card.Max.X-px(2), card.Max.Y-px(2))
		w.label(labelRect, item.Text, m.FontSize, false, false, ink, WidgetCenter)

		if w.DropControl == control.ID && w.DropIndex == i {
			w.fill(image.Rect(card.Min.X, card.Min.Y+px(6), card.Min.X+max(2, px(2)), card.Max.Y-px(6)), c.Accent, 1)
		}
	}
	if w.DropControl == control.ID && w.DropIndex >= endIdx && endIdx > startIdx {
		last := w.geometry.Hits[len(w.geometry.Hits)-1].Area.Inset(px(3))
		w.fill(image.Rect(last.Max.X-max(2, px(2)), last.Min.Y+px(6), last.Max.X, last.Max.Y-px(6)), c.Accent, 1)
	}
}

func (w *widgetLayout) drawItemIcon(r image.Rectangle, item v1.Item) {
	if w.canvas == nil || r.Empty() {
		return
	}
	key := item.Icon
	if v1.IsIconName(key) {
		w.icon(r, key, w.Colors.Ink)
		return
	}
	if key == "" {
		key = item.Hint
	}
	if w.Icons != nil && key != "" {
		if img := w.Icons[key]; img != nil {
			w.canvas.Image(r, img)
			return
		}
	}
	if strings.HasPrefix(key, "favicon:") {
		w.drawMonogramBadge(r, strings.TrimPrefix(key, "favicon:"), item.Text)
		return
	}
	c := w.Colors
	badgeCol := c.Accent
	tag := strings.ToUpper(strings.TrimPrefix(filepath.Ext(item.Text), "."))
	if tag == "" {
		tag = "DIR"
		badgeCol = c.Weekend
		if badgeCol.A == 0 {
			badgeCol = c.Accent
		}
	}
	if len(tag) > 3 {
		tag = tag[:3]
	}
	iconDp := r.Dx() * 96 / max(w.DPI, 1)
	radDp := max(5, min(14, iconDp*22/100))
	tagSize := max(8, min(16, iconDp/4))
	w.fill(r, w.alphaOf(badgeCol, 42), radDp)
	w.stroke(r, w.alphaOf(badgeCol, 170), radDp, 1)
	w.label(r, tag, tagSize, true, true, badgeCol, WidgetCenter)
}

var monogramPalette = []color.NRGBA{
	{R: 46, G: 125, B: 246, A: 255}, // Cobalt
	{R: 16, G: 163, B: 127, A: 255}, // Emerald
	{R: 234, G: 136, B: 30, A: 255}, // Amber
	{R: 139, G: 92, B: 246, A: 255}, // Violet
	{R: 225, G: 75, B: 112, A: 255}, // Rose
	{R: 14, G: 165, B: 201, A: 255}, // Cyan
	{R: 99, G: 102, B: 241, A: 255}, // Indigo
	{R: 20, G: 148, B: 158, A: 255}, // Teal
}

// drawMonogramBadge draws a deterministic, vibrant colored rounded-square badge for URLs
// whose favicon is loading, unreachable, or absent (e.g. intranet/localhost services).
func (w *widgetLayout) drawMonogramBadge(r image.Rectangle, rawURL, title string) {
	host := rawURL
	if parsed, err := url.Parse(strings.TrimSpace(rawURL)); err == nil && parsed.Hostname() != "" {
		host = strings.ToLower(parsed.Hostname())
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(host))
	bg := monogramPalette[int(h.Sum32())%len(monogramPalette)]

	mono := urlMonogram(host, title)
	iconDp := r.Dx() * 96 / max(w.DPI, 1)
	radius := max(5, min(14, iconDp*22/100))
	w.fill(r, w.alphaOf(bg, 228), radius)
	w.stroke(r, color.NRGBA{R: 255, G: 255, B: 255, A: 55}, radius, 1)
	fontSize := max(9, min(22, iconDp*33/100))
	if len([]rune(mono)) == 1 && []rune(mono)[0] >= 128 {
		fontSize = max(10, min(26, iconDp*38/100))
	}
	w.label(r, mono, fontSize, true, false, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, WidgetCenter)
}

func urlMonogram(host, title string) string {
	trimmedTitle := strings.TrimSpace(title)
	if trimmedTitle != "" {
		runes := []rune(trimmedTitle)
		if runes[0] > 127 {
			return string(runes[0])
		}
	}
	h := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
	if h == "localhost" {
		return "LH"
	}
	if ip := net.ParseIP(h); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			parts := strings.Split(v4.String(), ".")
			if len(parts) == 4 && len(parts[3]) <= 2 {
				return parts[3]
			}
		}
		return "IP"
	}
	label := h
	if dot := strings.IndexByte(h, '.'); dot > 0 {
		label = h[:dot]
	}
	if trimmedTitle != "" {
		var letters []rune
		for _, r := range trimmedTitle {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				letters = append(letters, unicode.ToUpper(r))
				if len(letters) == 2 {
					return string(letters)
				}
			}
		}
	}
	var letters []rune
	for _, r := range label {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters = append(letters, unicode.ToUpper(r))
			if len(letters) == 2 {
				break
			}
		}
	}
	if len(letters) > 0 {
		return string(letters)
	}
	return "W"
}
