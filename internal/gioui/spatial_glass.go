package gioui

import (
	"image"
	"image/color"
	"math"
	"sync"

	"gioui.org/f32"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"quailcrate/internal/theme"
)

// SpatialRect describes a floating-point sub-pixel rectangle in backdrop image
// coordinates.
type SpatialRect struct {
	Min, Max f32.Point
}

func (r SpatialRect) Dx() float32 { return r.Max.X - r.Min.X }
func (r SpatialRect) Dy() float32 { return r.Max.Y - r.Min.Y }

// SpatialBackdrop describes a pre-blurred, vibrancy-saturated desktop wallpaper
// image and the floating-point source rectangle (in Image pixel coordinates)
// aligned with a widget's glass frame.
type SpatialBackdrop struct {
	Image   *image.RGBA
	Source  SpatialRect
	imageOp paint.ImageOp // Optional texture handle for immutable preview wallpaper.
	faded   map[uint8]fadedBackdropTexture
}

type fadedBackdropTexture struct {
	crop image.Rectangle
	op   paint.ImageOp
}

// SpatialGlassSpec holds all parameters required to render a visionOS / Linear
// spatial frosted glass surface (real blurred+saturated backdrop, translucent
// surface tint, deep multi-layer Gaussian elevation shadow, outer ambient ring,
// luminous frosted border, and curved top inner specular glow).
type SpatialGlassSpec struct {
	CanvasSize     image.Point
	Frame          image.Rectangle
	Radius         int
	DPI            int
	Surface        color.NRGBA
	BorderOverride color.NRGBA // non-zero when edit/drop highlight overrides the glass rim
	Opacity        int         // 0..100
	SurfaceAlpha   uint8
	IsDark         bool
	Elevated       bool
	Backdrop       *SpatialBackdrop
}

// SpatialGlassCanvas is an optional capability interface implemented by GPU
// canvases (such as GioWidgetCanvas) to render full spatial frosted glass.
type SpatialGlassCanvas interface {
	DrawSpatialGlass(spec SpatialGlassSpec)
}

// SpatialGlassPadding returns the outer pixel margin (left, top, right, bottom)
// reserved around the glass frame for the outer 1px ambient ring and feathered
// downward elevation shadow. Collapsed widgets use a compact margin so the
// 40dp title strip is never cramped.
func SpatialGlassPadding(size image.Point, dpi int, translucent bool, surfaceAlpha uint8) (left, top, right, bottom int) {
	if !translucent || surfaceAlpha < 14 {
		return 0, 0, 0, 0
	}
	px := func(dp int) int { return dp * max(dpi, 1) / 96 }
	if size.Y <= px(WidgetTitleHeight+12) {
		return px(3), px(1), px(3), px(4)
	}
	return px(6), px(2), px(6), px(12)
}

// SpatialGlassTintAlpha maps user Opacity (0..100) to the glass surface tint
// alpha. When a real blurred+saturated wallpaper backdrop is present, default
// opacities (65-78%) land right in the visionOS sweet spot (~56-62% dark,
// ~65-66% light) while keeping 0% pure-transparent and 100% completely solid.
func SpatialGlassTintAlpha(opacity int, isDark, hasBackdrop bool) uint8 {
	op := max(0, min(100, opacity))
	if op == 0 {
		return 0
	}
	if op == 100 {
		return 255
	}
	if !hasBackdrop {
		return uint8(op * 255 / 100)
	}
	t := float64(op) / 100.0
	base := 0.48
	if !isDark {
		base = 0.58
	}
	ratio := t * (base + (1.0-base)*t*t)
	return clampByteF(ratio)
}

// SpatialGlassBackdropAlpha returns the frost backdrop opacity for user Opacity
// (0..100). It smoothly ramps in from 0% to full frost by 25% so translucent
// glass cards show crisp frosted blur without sharp wallpaper bleed-through.
func SpatialGlassBackdropAlpha(opacity int) uint8 {
	op := max(0, min(100, opacity))
	if op <= 0 {
		return 0
	}
	if op >= 25 {
		return 255
	}
	return uint8(op * 255 / 25)
}

// BuildSpatialBackdrop applies a 3-pass separable Gaussian-equivalent box blur
// followed by CSS saturate(185%) vibrancy boosting, reproducing the optical
// look of `backdrop-filter: blur(36px) saturate(185%)`.
func BuildSpatialBackdrop(src *image.RGBA, blurRadius int) *image.RGBA {
	if src == nil || src.Rect.Empty() {
		return nil
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		copy(dst.Pix[y*dst.Stride:y*dst.Stride+w*4], src.Pix[y*src.Stride:y*src.Stride+w*4])
	}
	if blurRadius > 0 && (w > 1 || h > 1) {
		tmp := image.NewRGBA(image.Rect(0, 0, w, h))
		for pass := 0; pass < 3; pass++ {
			boxBlurH(dst, tmp, blurRadius)
			boxBlurV(tmp, dst, blurRadius)
		}
	}
	applyBackdropSaturate(dst, 1.85)
	return dst
}

func boxBlurH(src, dst *image.RGBA, radius int) {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	if w <= 0 || h <= 0 {
		return
	}
	div := 2*radius + 1
	for y := 0; y < h; y++ {
		sRow := src.Pix[y*src.Stride:]
		dRow := dst.Pix[y*dst.Stride:]
		var sr, sg, sb int
		// Seed initial window centered at x = 0 with edge clamping.
		sr = int(sRow[0]) * (radius + 1)
		sg = int(sRow[1]) * (radius + 1)
		sb = int(sRow[2]) * (radius + 1)
		for i := 1; i <= radius; i++ {
			x := min(i, w-1) * 4
			sr += int(sRow[x])
			sg += int(sRow[x+1])
			sb += int(sRow[x+2])
		}
		for x := 0; x < w; x++ {
			off := x * 4
			dRow[off] = uint8((sr + div/2) / div)
			dRow[off+1] = uint8((sg + div/2) / div)
			dRow[off+2] = uint8((sb + div/2) / div)
			dRow[off+3] = 255

			rem := max(0, x-radius) * 4
			add := min(w-1, x+radius+1) * 4
			sr += int(sRow[add]) - int(sRow[rem])
			sg += int(sRow[add+1]) - int(sRow[rem+1])
			sb += int(sRow[add+2]) - int(sRow[rem+2])
		}
	}
}

func boxBlurV(src, dst *image.RGBA, radius int) {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	if w <= 0 || h <= 0 {
		return
	}
	div := 2*radius + 1
	sStride, dStride := src.Stride, dst.Stride
	for x := 0; x < w; x++ {
		xOff := x * 4
		sr := int(src.Pix[xOff]) * (radius + 1)
		sg := int(src.Pix[xOff+1]) * (radius + 1)
		sb := int(src.Pix[xOff+2]) * (radius + 1)
		for i := 1; i <= radius; i++ {
			y := min(i, h-1) * sStride
			sr += int(src.Pix[y+xOff])
			sg += int(src.Pix[y+xOff+1])
			sb += int(src.Pix[y+xOff+2])
		}
		for y := 0; y < h; y++ {
			dOff := y*dStride + xOff
			dst.Pix[dOff] = uint8((sr + div/2) / div)
			dst.Pix[dOff+1] = uint8((sg + div/2) / div)
			dst.Pix[dOff+2] = uint8((sb + div/2) / div)
			dst.Pix[dOff+3] = 255

			rem := max(0, y-radius)*sStride + xOff
			add := min(h-1, y+radius+1)*sStride + xOff
			sr += int(src.Pix[add]) - int(src.Pix[rem])
			sg += int(src.Pix[add+1]) - int(src.Pix[rem+1])
			sb += int(src.Pix[add+2]) - int(src.Pix[rem+2])
		}
	}
}

// applyBackdropSaturate applies the W3C CSS Filter Effects saturate(s) matrix
// (ITU-R BT.709 / sRGB luminance weights) in place.
func applyBackdropSaturate(img *image.RGBA, s float64) {
	r0 := 0.213 + 0.787*s
	r1 := 0.715 - 0.715*s
	r2 := 0.072 - 0.072*s
	g0 := 0.213 - 0.213*s
	g1 := 0.715 + 0.285*s
	g2 := 0.072 - 0.072*s
	b0 := 0.213 - 0.213*s
	b1 := 0.715 - 0.715*s
	b2 := 0.072 + 0.928*s

	w, h := img.Rect.Dx(), img.Rect.Dy()
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for i := 0; i < len(row); i += 4 {
			r, g, b := float64(row[i]), float64(row[i+1]), float64(row[i+2])
			row[i] = clampByte255(r0*r + r1*g + r2*b)
			row[i+1] = clampByte255(g0*r + g1*g + g2*b)
			row[i+2] = clampByte255(b0*r + b1*g + b2*b)
			row[i+3] = 255
		}
	}
}

func clampByte255(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}

func clampByteF(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255.0 + 0.5)
}

func smoothstep(edge0, edge1, x float64) float64 {
	if edge1 <= edge0 {
		if x >= edge1 {
			return 1
		}
		return 0
	}
	t := (x - edge0) / (edge1 - edge0)
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	return t * t * (3 - 2*t)
}

func spatialRoundedRectSDF(px, py, cx, cy, hx, hy, r float64) float64 {
	qx := math.Abs(px-cx) - (hx - r)
	qy := math.Abs(py-cy) - (hy - r)
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

type spatialChromeKey struct {
	canvasSize     image.Point
	frame          image.Rectangle
	radius         int
	dpi            int
	borderOverride color.NRGBA
	surfaceAlpha   uint8
	isDark         bool
	elevated       bool
}

type spatialChromeEntry struct {
	key      spatialChromeKey
	shadow   *image.RGBA
	shadowOp paint.ImageOp
	rim      *image.RGBA
	rimOp    paint.ImageOp
}

const spatialChromeCacheLimit = 24

var (
	spatialChromeMu    sync.Mutex
	spatialChromeCache []*spatialChromeEntry
)

func getSpatialChrome(key spatialChromeKey) *spatialChromeEntry {
	spatialChromeMu.Lock()
	for i, e := range spatialChromeCache {
		if e.key == key {
			if i > 0 {
				copy(spatialChromeCache[1:i+1], spatialChromeCache[0:i])
				spatialChromeCache[0] = e
			}
			spatialChromeMu.Unlock()
			return e
		}
	}
	spatialChromeMu.Unlock()

	shadow, rim := renderSpatialGlassChrome(key)
	entry := &spatialChromeEntry{
		key:    key,
		shadow: shadow,
		rim:    rim,
	}
	if shadow != nil {
		entry.shadowOp = paint.NewImageOp(shadow)
		entry.shadowOp.Filter = paint.FilterNearest
	}
	if rim != nil {
		entry.rimOp = paint.NewImageOp(rim)
		entry.rimOp.Filter = paint.FilterNearest
	}

	spatialChromeMu.Lock()
	for _, e := range spatialChromeCache {
		if e.key == key {
			spatialChromeMu.Unlock()
			return e
		}
	}
	spatialChromeCache = append([]*spatialChromeEntry{entry}, spatialChromeCache...)
	if len(spatialChromeCache) > spatialChromeCacheLimit {
		spatialChromeCache = spatialChromeCache[:spatialChromeCacheLimit]
	}
	spatialChromeMu.Unlock()
	return entry
}

// renderSpatialGlassChrome builds two premultiplied RGBA overlays for a spatial
// frosted glass card:
//  1. shadow: strictly outside frame — 2-layer Gaussian drop shadow + outer 1px
//     ambient hairline ring (`0 0 0 1px`), guaranteed zero interior bleed.
//  2. rim: on and just inside frame's perimeter — crisp 1px frosted glass border
//     + mathematically exact curved `inset 0 1px 0 0` top inner specular glow.
func renderSpatialGlassChrome(k spatialChromeKey) (shadowImg, rimImg *image.RGBA) {
	cw, ch := k.canvasSize.X, k.canvasSize.Y
	f := k.frame
	if cw <= 0 || ch <= 0 || f.Empty() {
		return nil, nil
	}
	fw, fh := f.Dx(), f.Dy()
	scale := float64(max(k.dpi, 96)) / 96.0
	bw := math.Max(1.0, math.Round(scale))
	radF := float64(min(max(0, k.radius), min(fw, fh)/2))
	hx, hy := float64(fw)/2.0, float64(fh)/2.0
	cx, cy := float64(f.Min.X)+hx, float64(f.Min.Y)+hy

	// Smooth opacity multiplier for glass chrome (ramps in smoothly above 0).
	opNorm := float64(k.surfaceAlpha) / 255.0
	opScale := opNorm
	if k.surfaceAlpha >= 14 {
		opScale = 0.35 + 0.65*opNorm
	} else {
		opScale = (float64(k.surfaceAlpha) / 14.0) * 0.35
	}

	hasShadowMargin := f.Min.X > 0 || f.Min.Y > 0 || f.Max.X < cw || f.Max.Y < ch
	if hasShadowMargin && opScale > 0 {
		shadowImg = image.NewRGBA(image.Rect(0, 0, cw, ch))
	}
	rimImg = image.NewRGBA(image.Rect(0, 0, cw, ch))

	// Shadow parameters faithfully tuned to visionOS / Linear dual-layer shadows:
	//   Dark:  0 28px 64px -16px rgba(0,0,0,0.68), 0 6px 20px -3px rgba(0,0,0,0.42), 0 0 0 1px rgba(255,255,255,0.04)
	//   Light: 0 24px 54px -14px rgba(15,23,42,0.11), 0 4px 16px -2px rgba(15,23,42,0.05), 0 0 0 1px rgba(15,23,42,0.04)
	padBottom := float64(max(1, ch-f.Max.Y))
	padSide := float64(max(1, min(f.Min.X, cw-f.Max.X)))
	var shR, shG, shB float64
	var contactPeak, deepPeak float64
	var ringR, ringG, ringB, ringAlpha float64
	var borderR, borderG, borderB, borderAlpha float64
	var glowAlpha float64

	if k.isDark {
		shR, shG, shB = 0, 0, 0
		contactPeak, deepPeak = 0.42, 0.56
		ringR, ringG, ringB, ringAlpha = 1, 1, 1, 0.05
		borderR, borderG, borderB, borderAlpha = 1, 1, 1, 0.135
		glowAlpha = 0.165
		if k.elevated {
			contactPeak, deepPeak = 0.52, 0.68
			ringAlpha = 0.095
			borderAlpha = 0.185
		}
	} else {
		shR, shG, shB = 15.0/255.0, 23.0/255.0, 42.0/255.0
		contactPeak, deepPeak = 0.08, 0.14
		ringR, ringG, ringB, ringAlpha = 15.0/255.0, 23.0/255.0, 42.0/255.0, 0.065
		borderR, borderG, borderB, borderAlpha = 1, 1, 1, 0.85
		glowAlpha = 0.92
		if k.elevated {
			contactPeak, deepPeak = 0.12, 0.21
			ringAlpha = 0.09
		}
	}
	if k.borderOverride.A > 0 {
		borderR = float64(k.borderOverride.R) / 255.0
		borderG = float64(k.borderOverride.G) / 255.0
		borderB = float64(k.borderOverride.B) / 255.0
		borderAlpha = float64(k.borderOverride.A) / 255.0
	} else {
		borderAlpha *= opScale
	}
	ringAlpha *= opScale
	glowAlpha *= opScale

	// Shadow Gaussian geometry scaled cleanly to the available shadow margin so
	// it blooms smoothly and always reaches zero before the window perimeter.
	contactDY := math.Min(padBottom*0.26, 2.0*scale)
	contactSigma := math.Max(1.0, math.Min(padSide*0.42, 2.4*scale))
	contactInv := 1.0 / (contactSigma * contactSigma)

	deepDY := math.Min(padBottom*0.42, 4.8*scale)
	if k.elevated {
		deepDY = math.Min(padBottom*0.48, 5.8*scale)
	}
	deepSpread := math.Min(hx*0.25, 2.0*scale)
	deepSigma := math.Max(1.4, math.Min(padBottom*0.38, 4.6*scale))
	deepInv := 1.0 / (deepSigma * deepSigma)

	hxIn := math.Max(0.5, hx-bw)
	hyIn := math.Max(0.5, hy-bw)
	radIn := math.Max(0, radF-bw)
	topGlowMaxY := float64(f.Min.Y) + bw*2.0 + radF + 2.0
	edgeFeather := math.Max(1.5, 2.5*scale)

	// Skip the deep interior rectangle where both shadow and rim are completely transparent.
	band := int(math.Ceil(math.Max(radF, bw*2))) + 3
	innerTop := f.Min.Y + band
	innerBottom := f.Max.Y - band
	innerLeft := f.Min.X + band
	innerRight := f.Max.X - band

	paintPixel := func(x, y int, py float64) {
		px := float64(x) + 0.5
		d := spatialRoundedRectSDF(px, py, cx, cy, hx, hy, radF)

		// 1) Shadow + outer 1px ambient hairline ring (strictly outside frame, d >= -0.5).
		if shadowImg != nil && d >= -0.5 {
			outsideCov := math.Max(0, math.Min(1, d+0.5))
			// Contact shadow tucked slightly below the card.
			dContact := math.Max(0, spatialRoundedRectSDF(px, py, cx, cy+contactDY, math.Max(0.5, hx-0.5*scale), math.Max(0.5, hy-0.5*scale), radF))
			cA := contactPeak * math.Exp(-dContact*dContact*contactInv)
			// Deep downward ambient bloom (negative spread narrows horizontally like CSS -16px spread).
			dDeep := math.Max(0, spatialRoundedRectSDF(px, py, cx, cy+deepDY, math.Max(0.5, hx-deepSpread), math.Max(0.5, hy-deepSpread*0.5), math.Max(0, radF-deepSpread*0.4)))
			dpA := deepPeak * math.Exp(-dDeep*dDeep*deepInv)
			sA := (cA + dpA*(1.0-cA)) * opScale * outsideCov
			edge := math.Min(math.Min(px, float64(cw)-px), math.Min(py, float64(ch)-py))
			if edge < edgeFeather {
				sA *= smoothstep(0, edgeFeather, edge)
			}

			// Outer 1px ambient ring on d in [0, bw].
			ringHi := math.Min(d+0.5, bw)
			ringLo := math.Max(d-0.5, 0)
			rA := math.Max(0, ringHi-ringLo) * ringAlpha

			outA := rA + sA*(1.0-rA)
			if outA > 0 {
				invR := 1.0 - rA
				outR := ringR*rA + shR*sA*invR
				outG := ringG*rA + shG*sA*invR
				outB := ringB*rA + shB*sA*invR
				a := clampByteF(outA)
				if a > 0 {
					off := y*shadowImg.Stride + x*4
					shadowImg.Pix[off] = min(a, clampByteF(outR))
					shadowImg.Pix[off+1] = min(a, clampByteF(outG))
					shadowImg.Pix[off+2] = min(a, clampByteF(outB))
					shadowImg.Pix[off+3] = a
				}
			}
		}

		// 2) Frosted 1px border (d in [-bw, 0]) + curved top inner specular glow (`inset 0 1px 0 0`).
		if d <= 0.5 {
			hi := math.Min(d+0.5, 0)
			lo := math.Max(d-0.5, -bw)
			bA := math.Max(0, hi-lo) * borderAlpha

			var gA float64
			if glowAlpha > 0 && py <= topGlowMaxY && d <= 0 {
				dIn := spatialRoundedRectSDF(px, py, cx, cy, hxIn, hyIn, radIn)
				if dIn < 0.5 {
					dShift := spatialRoundedRectSDF(px, py, cx, cy+bw, hxIn, hyIn, radIn)
					if dShift > -0.5 {
						insideIn := math.Max(0, math.Min(1, -dIn+0.5))
						aboveShift := math.Max(0, math.Min(1, dShift+0.5))
						gA = insideIn * aboveShift * glowAlpha
					}
				}
			}

			// Composite border OVER top inner glow.
			outA := bA + gA*(1.0-bA)
			if outA > 0 {
				invB := 1.0 - bA
				outR := borderR*bA + gA*invB
				outG := borderG*bA + gA*invB
				outB := borderB*bA + gA*invB
				a := clampByteF(outA)
				if a > 0 {
					off := y*rimImg.Stride + x*4
					rimImg.Pix[off] = min(a, clampByteF(outR))
					rimImg.Pix[off+1] = min(a, clampByteF(outG))
					rimImg.Pix[off+2] = min(a, clampByteF(outB))
					rimImg.Pix[off+3] = a
				}
			}
		}
	}

	for y := 0; y < ch; y++ {
		py := float64(y) + 0.5
		if y >= innerTop && y < innerBottom && innerLeft < innerRight {
			for x := 0; x < innerLeft; x++ {
				paintPixel(x, y, py)
			}
			for x := innerRight; x < cw; x++ {
				paintPixel(x, y, py)
			}
		} else {
			for x := 0; x < cw; x++ {
				paintPixel(x, y, py)
			}
		}
	}
	return shadowImg, rimImg
}

// DrawSpatialGlassOps records all layers of a spatial frosted glass card directly
// into ops: outer Gaussian drop shadow + outer 1px ambient ring, rounded-clipped
// frosted blurred+saturated wallpaper backdrop, translucent theme surface tint,
// crisp 1px frosted border, and curved top inner specular glow.
func DrawSpatialGlassOps(ops *op.Ops, spec SpatialGlassSpec) {
	f := spec.Frame
	if ops == nil || f.Empty() || spec.CanvasSize.X <= 0 || spec.CanvasSize.Y <= 0 {
		return
	}
	effRad := min(max(0, spec.Radius), min(f.Dx()/2, f.Dy()/2))

	chrome := getSpatialChrome(spatialChromeKey{
		canvasSize:     spec.CanvasSize,
		frame:          f,
		radius:         effRad,
		dpi:            max(spec.DPI, 96),
		borderOverride: spec.BorderOverride,
		surfaceAlpha:   spec.SurfaceAlpha,
		isDark:         spec.IsDark,
		elevated:       spec.Elevated,
	})

	// 1) Outer shadow + 1px ambient halo ring strictly outside the glass frame.
	if chrome.shadow != nil {
		chrome.shadowOp.Add(ops)
		paint.PaintOp{}.Add(ops)
	}

	// 2) Clipped glass body: blurred+saturated wallpaper backdrop + translucent tint.
	hasBackdrop := spec.Backdrop != nil && spec.Backdrop.Image != nil && !spec.Backdrop.Image.Rect.Empty() && spec.Backdrop.Source.Dx() > 0 && spec.Backdrop.Source.Dy() > 0
	tintAlpha := SpatialGlassTintAlpha(spec.Opacity, spec.IsDark, hasBackdrop)
	backdropAlpha := SpatialGlassBackdropAlpha(spec.Opacity)

	var bodyClip clip.Stack
	if effRad > 0 {
		bodyClip = clip.UniformRRect(f, effRad).Push(ops)
	} else {
		bodyClip = clip.Rect(f).Push(ops)
	}

	if hasBackdrop && backdropAlpha > 0 && tintAlpha < 255 {
		drawSpatialBackdropSlice(ops, f, spec.Backdrop, backdropAlpha)
	}
	if tintAlpha > 0 {
		paint.ColorOp{Color: theme.WithAlpha(spec.Surface, tintAlpha)}.Add(ops)
		paint.PaintOp{}.Add(ops)
	}
	bodyClip.Pop()

	// 3) Frosted 1px border + curved top inner specular glow (`inset 0 1px 0 0`).
	if chrome.rim != nil {
		chrome.rimOp.Add(ops)
		paint.PaintOp{}.Add(ops)
	}
}

func drawSpatialBackdropSlice(ops *op.Ops, dst image.Rectangle, bd *SpatialBackdrop, alpha uint8) {
	if bd == nil || bd.Image == nil || dst.Empty() || alpha == 0 {
		return
	}
	src := bd.Source
	if src.Dx() <= 0 || src.Dy() <= 0 {
		return
	}
	img := bd.Image
	imgOp := bd.imageOp
	if alpha < 255 {
		// Low-opacity transition (<25%): modulate only the tiny cropped region so
		// frost fades out seamlessly down toward 0% pure float.
		cropMinX := max(0, int(math.Floor(float64(src.Min.X)))-1)
		cropMinY := max(0, int(math.Floor(float64(src.Min.Y)))-1)
		cropMaxX := min(img.Rect.Dx(), int(math.Ceil(float64(src.Max.X)))+1)
		cropMaxY := min(img.Rect.Dy(), int(math.Ceil(float64(src.Max.Y)))+1)
		cw, ch := cropMaxX-cropMinX, cropMaxY-cropMinY
		if cw <= 0 || ch <= 0 {
			return
		}
		imgOp = bd.fadedTexture(image.Rect(cropMinX, cropMinY, cropMaxX, cropMaxY), alpha)
		src = SpatialRect{
			Min: f32.Pt(src.Min.X-float32(cropMinX), src.Min.Y-float32(cropMinY)),
			Max: f32.Pt(src.Max.X-float32(cropMinX), src.Max.Y-float32(cropMinY)),
		}
	} else if imgOp.Size() == (image.Point{}) {
		imgOp = linearImageOp(img)
	}

	sx := float32(dst.Dx()) / src.Dx()
	sy := float32(dst.Dy()) / src.Dy()
	// Map src.Min -> dst.Min and src.Max -> dst.Max with smooth GPU bilinear interpolation.
	tx := float32(dst.Min.X) - src.Min.X*sx
	ty := float32(dst.Min.Y) - src.Min.Y*sy
	t := f32.AffineId().Scale(f32.Point{}, f32.Pt(sx, sy)).Offset(f32.Pt(tx, ty))
	defer op.Affine(t).Push(ops).Pop()
	imgOp.Add(ops)
	paint.PaintOp{}.Add(ops)
}

func (bd *SpatialBackdrop) fadedTexture(crop image.Rectangle, alpha uint8) paint.ImageOp {
	// Each immutable preview wallpaper keeps one crop per opacity below 25%.
	if bd.faded != nil {
		previewBackdropMu.Lock()
		defer previewBackdropMu.Unlock()
		if entry, ok := bd.faded[alpha]; ok && entry.crop == crop {
			return entry.op
		}
	}
	faded := image.NewRGBA(image.Rectangle{Max: crop.Size()})
	a := uint32(alpha)
	for y := 0; y < crop.Dy(); y++ {
		sRow := bd.Image.Pix[(crop.Min.Y+y)*bd.Image.Stride+crop.Min.X*4:]
		dRow := faded.Pix[y*faded.Stride:]
		for x := 0; x < crop.Dx()*4; x += 4 {
			dRow[x] = uint8((uint32(sRow[x])*a + 127) / 255)
			dRow[x+1] = uint8((uint32(sRow[x+1])*a + 127) / 255)
			dRow[x+2] = uint8((uint32(sRow[x+2])*a + 127) / 255)
			dRow[x+3] = alpha
		}
	}
	imgOp := linearImageOp(faded)
	if bd.faded != nil {
		bd.faded[alpha] = fadedBackdropTexture{crop: crop, op: imgOp}
	}
	return imgOp
}

func linearImageOp(img *image.RGBA) paint.ImageOp {
	imgOp := paint.NewImageOp(img)
	imgOp.Filter = paint.FilterLinear
	return imgOp
}

// Cached studio wallpapers (sharp + matching blurred+saturated backdrop) used by
// the Settings Appearance live preview strip for both dark and light themes.
type previewWallpaperEntry struct {
	size     image.Point
	sharp    paint.ImageOp
	backdrop SpatialBackdrop
}

var (
	previewBackdropMu sync.Mutex
	previewWallpapers [2]previewWallpaperEntry // 0 = light, 1 = dark
)

func getSettingsPreviewWallpaper(stripSize image.Point, isDark bool) previewWallpaperEntry {
	if stripSize.X <= 0 || stripSize.Y <= 0 {
		return previewWallpaperEntry{}
	}
	idx := 0
	if isDark {
		idx = 1
	}
	previewBackdropMu.Lock()
	defer previewBackdropMu.Unlock()
	if cur := previewWallpapers[idx]; cur.size == stripSize {
		return cur
	}

	// Render a silky-smooth mesh-gradient studio wallpaper at half resolution so
	// both the background and its Spatial Glass frosted blur match organically.
	sw := max(48, stripSize.X/2)
	sh := max(24, stripSize.Y/2)
	raw := image.NewRGBA(image.Rect(0, 0, sw, sh))

	type bloom struct {
		cx, cy, rad, weight float64
		r, g, b             float64
	}
	var (
		cTL, cTR, cBL, cBR [3]float64
		blooms             [3]bloom
		waveCol            [3]float64
		waveWeight         float64
	)
	if isDark {
		// Deep twilight silk mesh (rich midnight blue -> indigo -> muted plum)
		cTL = [3]float64{13, 20, 38}
		cTR = [3]float64{24, 24, 50}
		cBL = [3]float64{15, 30, 48}
		cBR = [3]float64{38, 24, 58}
		blooms = [3]bloom{
			{cx: 0.22, cy: 0.34, rad: 0.46, weight: 0.44, r: 56, g: 174, b: 242},
			{cx: 0.56, cy: 0.78, rad: 0.52, weight: 0.36, r: 164, g: 108, b: 245},
			{cx: 0.86, cy: 0.26, rad: 0.44, weight: 0.28, r: 236, g: 112, b: 162},
		}
		waveCol = [3]float64{195, 224, 255}
		waveWeight = 0.14
	} else {
		// Luminous daylight pearl silk mesh (soft glacier sky -> cloud pearl -> warm rose mist)
		cTL = [3]float64{212, 229, 250}
		cTR = [3]float64{236, 226, 246}
		cBL = [3]float64{222, 238, 248}
		cBR = [3]float64{246, 232, 238}
		blooms = [3]bloom{
			{cx: 0.18, cy: 0.32, rad: 0.48, weight: 0.44, r: 138, g: 196, b: 252},
			{cx: 0.52, cy: 0.78, rad: 0.52, weight: 0.38, r: 196, g: 178, b: 252},
			{cx: 0.85, cy: 0.28, rad: 0.46, weight: 0.40, r: 252, g: 196, b: 212},
		}
		waveCol = [3]float64{255, 255, 255}
		waveWeight = 0.38
	}

	invW := 1.0 / float64(max(1, sw-1))
	invH := 1.0 / float64(max(1, sh-1))
	for y := 0; y < sh; y++ {
		fy := float64(y) * invH
		for x := 0; x < sw; x++ {
			fx := float64(x) * invW
			// Bilinear base mesh interpolation.
			topR := cTL[0]*(1-fx) + cTR[0]*fx
			topG := cTL[1]*(1-fx) + cTR[1]*fx
			topB := cTL[2]*(1-fx) + cTR[2]*fx
			botR := cBL[0]*(1-fx) + cBR[0]*fx
			botG := cBL[1]*(1-fx) + cBR[1]*fx
			botB := cBL[2]*(1-fx) + cBR[2]*fx
			r := topR*(1-fy) + botR*fy
			g := topG*(1-fy) + botG*fy
			b := topB*(1-fy) + botB*fy

			// Soft feathered aurora blooms.
			for _, bl := range blooms {
				dx := (fx - bl.cx) * 1.45
				dy := fy - bl.cy
				d := math.Hypot(dx, dy) / bl.rad
				if d < 1 {
					t := 1 - d*d
					w := t * t * bl.weight
					r = r*(1-w) + bl.r*w
					g = g*(1-w) + bl.g*w
					b = b*(1-w) + bl.b*w
				}
			}

			// Subtle silky light curve for authentic frosted refraction cues.
			curveY := 0.56 - 0.18*math.Sin(fx*math.Pi*1.35+0.35)
			dist := (fy - curveY) / 0.16
			if dist > -3 && dist < 3 {
				w := math.Exp(-dist*dist) * waveWeight
				r = r*(1-w) + waveCol[0]*w
				g = g*(1-w) + waveCol[1]*w
				b = b*(1-w) + waveCol[2]*w
			}

			off := y*raw.Stride + x*4
			raw.Pix[off] = clampByte255(r)
			raw.Pix[off+1] = clampByte255(g)
			raw.Pix[off+2] = clampByte255(b)
			raw.Pix[off+3] = 255
		}
	}

	blurRad := max(5, sw/22)
	blurredImg := BuildSpatialBackdrop(raw, blurRad)
	previewWallpapers[idx] = previewWallpaperEntry{
		size:  stripSize,
		sharp: linearImageOp(raw),
		backdrop: SpatialBackdrop{
			Image:   blurredImg,
			Source:  SpatialRect{Max: f32.Pt(float32(sw), float32(sh))},
			imageOp: linearImageOp(blurredImg),
			faded:   make(map[uint8]fadedBackdropTexture),
		},
	}
	return previewWallpapers[idx]
}

// DrawSettingsPreviewWallpaper paints the cached smooth studio wallpaper into r
// (caller clips to rounded bounds) and returns its matching blurred backdrop.
func DrawSettingsPreviewWallpaper(ops *op.Ops, r image.Rectangle, isDark bool) SpatialBackdrop {
	if r.Empty() {
		return SpatialBackdrop{}
	}
	wallpaper := getSettingsPreviewWallpaper(r.Size(), isDark)
	size := wallpaper.sharp.Size()
	sx := float32(r.Dx()) / float32(size.X)
	sy := float32(r.Dy()) / float32(size.Y)
	t := f32.AffineId().Scale(f32.Point{}, f32.Pt(sx, sy)).Offset(f32.Pt(float32(r.Min.X), float32(r.Min.Y)))
	defer op.Affine(t).Push(ops).Pop()
	wallpaper.sharp.Add(ops)
	paint.PaintOp{}.Add(ops)
	return wallpaper.backdrop
}
