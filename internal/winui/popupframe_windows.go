package winui

import (
	"image"
	"math"
	"unsafe"
)

const (
	chromeClass      = "QuailCrateChrome"
	popupShadowPadDp = 14
)

type popupChromeKey struct {
	width, height int32
	dpi           int32
	radius        int32
	pad           int32
	border        uintptr
	isDark        bool
	highContrast  bool
	opacity       byte
}

// popupFrame pairs a Win32 WS_POPUP content window with an exact SDF window
// region and an owned per-pixel alpha chrome overlay (anti-aliased rounded
// border, dark-mode specular top sheen, and feathered drop shadow) so popups
// render smooth rounded cards consistently on both Windows 10 and Windows 11
// while retaining native child EDIT controls and IME behavior.
type popupFrame struct {
	chrome       uintptr
	topmost      bool
	dib          dibSurface
	key          popupChromeKey
	regionActive bool
	regionW      int32
	regionH      int32
	regionRadius int32
	lastLeft     int32
	lastTop      int32
	posValid     bool
	visible      bool
	syncing      bool
}

func chromeWindowProcedure(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmEraseBkgnd:
		return 1
	case wmNcHitTest:
		return ^uintptr(0) // HTTRANSPARENT
	case wmMouseActivate:
		return 3 // MA_NOACTIVATE
	}
	result, _, _ := defWindowProc.Call(hwnd, msg, wParam, lParam)
	return result
}

func (f *popupFrame) hasChrome() bool {
	return f.chrome != 0
}

func (f *popupFrame) attach(owner, module uintptr, topmost bool) {
	f.release()
	f.topmost = topmost
	if owner == 0 {
		return
	}
	ex := uintptr(wsExToolWindow | wsExLayered | wsExTransparent | wsExNoActivate)
	if topmost {
		ex |= wsExTopmost
	}
	h, err := window(chromeClass, "", ex, wsPopup, rect{Right: 1, Bottom: 1}, owner, 0, module)
	if err != nil {
		// Fall back to Windows 11 DWM rounding if the overlay could not be created.
		corner := uint32(2) // DWMWCP_ROUND
		settingsDwmAttribute.Call(owner, 33, uintptr(unsafe.Pointer(&corner)), 4)
		return
	}
	f.chrome = h
	// Disable Windows 11 automatic DWM corner rounding/border so our per-pixel
	// chrome owns the exact border and shadow across Windows 10 and Windows 11.
	noRound := uint32(1) // DWMWCP_DONOTROUND
	settingsDwmAttribute.Call(owner, 33, uintptr(unsafe.Pointer(&noRound)), 4)
}

func (f *popupFrame) sync(owner uintptr, dpi, radiusDp int32, border uintptr, isDark, highContrast bool, opacity byte) {
	if owner == 0 || f.chrome == 0 || f.syncing {
		return
	}
	f.syncing = true
	defer func() { f.syncing = false }()

	r := windowRect(owner)
	w, h := r.width(), r.height()
	if w < 2 || h < 2 {
		return
	}
	if dpi <= 0 {
		dpi = 96
	}
	rad := int32(0)
	if radiusDp > 0 {
		rad = min(scale(radiusDp, dpi), min(w, h)/2)
	}
	pad := scale(popupShadowPadDp, dpi)
	if highContrast {
		pad = max(int32(1), scale(2, dpi))
	}

	// Keep the content window's region synchronized with the exact SDF boundary.
	if rad == 0 {
		if f.regionActive {
			setWindowRgn.Call(owner, 0, 1)
			f.regionActive = false
			f.regionW, f.regionH, f.regionRadius = w, h, 0
		}
	} else if !f.regionActive || f.regionW != w || f.regionH != h || f.regionRadius != rad {
		if rgn := buildRoundedRegion(w, h, rad); rgn != 0 {
			if ok, _, _ := setWindowRgn.Call(owner, rgn, 1); ok != 0 {
				f.regionActive = true
				f.regionW, f.regionH, f.regionRadius = w, h, rad
			} else {
				deleteObject.Call(rgn)
			}
		}
	}

	key := popupChromeKey{
		width: w, height: h, dpi: dpi, radius: rad, pad: pad,
		border: border, isDark: isDark, highContrast: highContrast, opacity: opacity,
	}
	dstX, dstY := r.Left-pad, r.Top-pad
	if key != f.key {
		totalW, totalH := w+2*pad, h+2*pad
		if f.dib.ensureDIB(image.Pt(int(totalW), int(totalH))) {
			byteLen := int(totalW) * int(totalH) * 4
			dst := unsafe.Slice((*byte)(unsafe.Pointer(f.dib.dibBits)), byteLen)
			renderPopupChrome(dst, int(w), int(h), int(pad), int(rad), int(dpi), border, isDark, highContrast, opacity)
			ptDst := point{dstX, dstY}
			sz := point{totalW, totalH}
			ptSrc := point{}
			blend := blendFunction{
				BlendOp:             acSrcOver,
				SourceConstantAlpha: 255,
				AlphaFormat:         acSrcAlpha,
			}
			if ok, _, _ := updateLayeredWindow.Call(
				f.chrome, 0,
				uintptr(unsafe.Pointer(&ptDst)),
				uintptr(unsafe.Pointer(&sz)),
				f.dib.dibDC,
				uintptr(unsafe.Pointer(&ptSrc)),
				0,
				uintptr(unsafe.Pointer(&blend)),
				ulwAlpha,
			); ok != 0 {
				f.key = key
				f.lastLeft, f.lastTop, f.posValid = dstX, dstY, true
			}
		}
	} else if !f.posValid || f.lastLeft != dstX || f.lastTop != dstY {
		setWindowPos.Call(f.chrome, 0, coord(dstX), coord(dstY), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate|swpNoOwnerZOrder)
		f.lastLeft, f.lastTop, f.posValid = dstX, dstY, true
	}
}

func (f *popupFrame) show(visible bool) {
	if f.chrome == 0 {
		return
	}
	if visible {
		showWindow.Call(f.chrome, swShowNoActivate)
		z := uintptr(0) // HWND_TOP (above non-topmost owner)
		if f.topmost {
			z = hwndTopmost
		}
		setWindowPos.Call(f.chrome, z, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate|swpNoOwnerZOrder)
		f.visible = true
	} else {
		showWindow.Call(f.chrome, swHide)
		f.visible = false
	}
}

// moveWith moves the owner popup and its chrome overlay atomically in one
// DeferWindowPos commit so dragging stays locked with zero frame lag.
func (f *popupFrame) moveWith(owner uintptr, x, y int32) {
	if owner == 0 {
		return
	}
	if f.chrome == 0 {
		setWindowPos.Call(owner, 0, coord(x), coord(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
		return
	}
	f.syncing = true
	defer func() { f.syncing = false }()

	pad := f.key.pad
	dstX, dstY := x-pad, y-pad
	flags := uintptr(swpNoSize | swpNoZOrder | swpNoActivate | swpNoOwnerZOrder)
	hdwp, _, _ := beginDeferWindowPos.Call(2)
	if hdwp != 0 {
		hdwp, _, _ = deferWindowPos.Call(hdwp, owner, 0, coord(x), coord(y), 0, 0, flags)
	}
	if hdwp != 0 {
		hdwp, _, _ = deferWindowPos.Call(hdwp, f.chrome, 0, coord(dstX), coord(dstY), 0, 0, flags)
	}
	if hdwp != 0 {
		if ok, _, _ := endDeferWindowPos.Call(hdwp); ok != 0 {
			f.lastLeft, f.lastTop, f.posValid = dstX, dstY, true
			return
		}
	}
	setWindowPos.Call(owner, 0, coord(x), coord(y), 0, 0, flags)
	setWindowPos.Call(f.chrome, 0, coord(dstX), coord(dstY), 0, 0, flags)
	f.lastLeft, f.lastTop, f.posValid = dstX, dstY, true
}

func (f *popupFrame) release() {
	if f.chrome != 0 && validWindow(f.chrome) {
		destroyWindow.Call(f.chrome)
	}
	f.chrome = 0
	f.dib.release()
	f.key = popupChromeKey{}
	f.regionActive = false
	f.regionW, f.regionH, f.regionRadius = 0, 0, 0
	f.lastLeft, f.lastTop, f.posValid, f.visible = 0, 0, false, false
}

// roundedRectSDF returns the signed Euclidean distance from (px, py) to a
// rounded rectangle centered at (cx, cy) with half-extents (hx, hy) and
// corner radius r. Negative values are inside, 0 is on the boundary, positive outside.
func roundedRectSDF(px, py, cx, cy, hx, hy, r float64) float64 {
	qx := math.Abs(px-cx) - (hx - r)
	qy := math.Abs(py-cy) - (hy - r)
	return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - r
}

// roundedRegionRects computes coalesced horizontal scanline rectangles covering
// exactly the pixels where roundedRectSDF <= -0.5. Because the anti-aliased
// chrome border reaches 100% opacity at d = -0.5, the integer region seam is
// completely hidden behind the border stroke on both straight and curved edges.
func roundedRegionRects(w, h, rad int32) []rect {
	if w <= 0 || h <= 0 {
		return nil
	}
	if rad <= 0 {
		return []rect{{0, 0, w, h}}
	}
	rad = min(rad, min(w, h)/2)
	hx, hy := float64(w)/2.0, float64(h)/2.0
	radF := float64(rad)

	var rects []rect
	var runTop, runLeft, runRight int32
	hasRun := false
	flush := func(bottom int32) {
		if hasRun {
			rects = append(rects, rect{runLeft, runTop, runRight, bottom})
			hasRun = false
		}
	}
	for y := int32(0); y < h; y++ {
		var left int32
		if y < rad || y >= h-rad {
			py := float64(y) + 0.5
			left = rad
			limit := min(rad+1, w/2)
			for x := int32(0); x <= limit; x++ {
				if roundedRectSDF(float64(x)+0.5, py, hx, hy, hx, hy, radF) <= -0.5+1e-9 {
					left = x
					break
				}
			}
		}
		right := w - left
		if left >= right {
			flush(y)
			continue
		}
		if hasRun && left == runLeft && right == runRight {
			continue
		}
		flush(y)
		runTop, runLeft, runRight, hasRun = y, left, right, true
	}
	flush(h)
	return rects
}

func buildRoundedRegion(w, h, rad int32) uintptr {
	rects := roundedRegionRects(w, h, rad)
	if len(rects) == 0 {
		return 0
	}
	// RGNDATAHEADER is 8 int32s (32 bytes), followed by n RECTs (4 int32s each).
	buf := make([]int32, 8+4*len(rects))
	buf[0] = 32 // dwSize
	buf[1] = 1  // RDH_RECTANGLES
	buf[2] = int32(len(rects))
	buf[3] = int32(len(rects) * 16)
	buf[4] = 0
	buf[5] = 0
	buf[6] = w
	buf[7] = h
	for i, r := range rects {
		base := 8 + 4*i
		buf[base] = r.Left
		buf[base+1] = r.Top
		buf[base+2] = r.Right
		buf[base+3] = r.Bottom
	}
	rgn, _, _ := extCreateRegion.Call(0, uintptr(len(buf)*4), uintptr(unsafe.Pointer(&buf[0])))
	return rgn
}

// renderPopupChrome renders a premultiplied 32-bit BGRA chrome frame (soft
// multi-layer drop shadow outside the card, mathematically exact anti-aliased
// border stroke along the card edge, optional 1px dark-mode top specular sheen,
// and completely transparent interior) into dst.
func renderPopupChrome(dst []byte, w, h, pad, rad, dpi int, border uintptr, isDark, highContrast bool, opacity byte) {
	totalW, totalH := w+2*pad, h+2*pad
	if totalW <= 0 || totalH <= 0 || len(dst) < totalW*totalH*4 {
		return
	}
	clear(dst[:totalW*totalH*4])

	hx, hy := float64(w)/2.0, float64(h)/2.0
	cx, cy := float64(pad)+hx, float64(pad)+hy
	radF := float64(min(rad, min(w, h)/2))
	bw := float64(max(1, int(scale(1, int32(dpi)))))
	opScale := float64(opacity) / 255.0

	bR := float64(byte(border)) / 255.0
	bG := float64(byte(border>>8)) / 255.0
	bB := float64(byte(border>>16)) / 255.0
	bBase := 1.0
	if !highContrast {
		// Keep border legible and hide the HRGN seam even on translucent Launcher.
		bBase = math.Max(0.90, opScale)
	}

	ambPeak, keyPeak := 0.08, 0.19
	if isDark {
		ambPeak, keyPeak = 0.16, 0.36
	}
	padF := float64(max(1, pad))
	dy := float64(scale(3, int32(dpi)))
	ambSigma := math.Max(1.0, padF*0.24)
	keySigma := math.Max(1.0, padF*0.38)
	ambInv := 1.0 / (ambSigma * ambSigma)
	keyInv := 1.0 / (keySigma * keySigma)

	rimTop := float64(pad) + bw
	rimStart := float64(pad) + math.Max(radF*0.85, 4.0)
	rimEnd := float64(pad+w) - math.Max(radF*0.85, 4.0)
	rimFade := math.Max(6.0, radF*0.55)
	drawRim := isDark && !highContrast && rimEnd > rimStart+2

	// Skip the completely transparent deep interior on middle rows.
	bwInt := int(math.Ceil(bw))
	innerTop := pad + max(rad, bwInt) + 2
	innerBottom := pad + h - max(rad, bwInt) - 2
	innerLeft := pad + max(rad, bwInt) + 2
	innerRight := pad + w - max(rad, bwInt) - 2

	paintPixel := func(x, y int, py float64) {
		px := float64(x) + 0.5
		d := roundedRectSDF(px, py, cx, cy, hx, hy, radF)

		// Exact box-filtered coverage of stroke [-bw, 0].
		hi := math.Min(d+0.5, 0.0)
		lo := math.Max(d-0.5, -bw)
		borderCov := math.Max(0.0, hi-lo)
		bA := borderCov * bBase

		var outR, outG, outB, outA float64
		if d > -0.5+1e-9 {
			// Outside content HRGN: composite anti-aliased border over soft shadow.
			var sA float64
			if !highContrast && pad > 0 {
				dAmb := math.Max(0.0, d)
				amb := ambPeak * math.Exp(-dAmb*dAmb*ambInv)
				dKey := math.Max(0.0, roundedRectSDF(px, py, cx, cy+dy, math.Max(0.5, hx-0.5), math.Max(0.5, hy-0.5), radF))
				key := keyPeak * math.Exp(-dKey*dKey*keyInv)
				sA = (amb + key*(1.0-amb)) * opScale
				edge := math.Min(math.Min(px, float64(totalW)-px), math.Min(py, float64(totalH)-py))
				if edge < 2.0 {
					sA *= math.Max(0.0, edge/2.0)
				}
			}
			outA = bA + sA*(1.0-bA)
			outR, outG, outB = bR*bA, bG*bA, bB*bA
		} else {
			// Inside content HRGN: inner border anti-aliasing + optional top sheen.
			var rimA float64
			if drawRim && py >= rimTop && py < rimTop+1.0 && px >= rimStart && px <= rimEnd {
				t := math.Min(1.0, math.Min((px-rimStart)/rimFade, (rimEnd-px)/rimFade))
				rimA = 0.11 * opScale * t
			}
			if rimA > 0 {
				invB := 1.0 - bA
				outA = bA + rimA*invB
				outR = bR*bA + rimA*invB
				outG = bG*bA + rimA*invB
				outB = bB*bA + rimA*invB
			} else {
				outA = bA
				outR, outG, outB = bR*bA, bG*bA, bB*bA
			}
		}

		if outA <= 0 {
			return
		}
		a := clampByte(outA)
		if a == 0 {
			return
		}
		r := min(a, clampByte(outR))
		g := min(a, clampByte(outG))
		b := min(a, clampByte(outB))
		off := (y*totalW + x) * 4
		dst[off] = b
		dst[off+1] = g
		dst[off+2] = r
		dst[off+3] = a
	}

	for y := 0; y < totalH; y++ {
		py := float64(y) + 0.5
		if y >= innerTop && y < innerBottom && innerLeft < innerRight {
			for x := 0; x < innerLeft; x++ {
				paintPixel(x, y, py)
			}
			for x := innerRight; x < totalW; x++ {
				paintPixel(x, y, py)
			}
		} else {
			for x := 0; x < totalW; x++ {
				paintPixel(x, y, py)
			}
		}
	}
}

func clampByte(v float64) byte {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return byte(v*255.0 + 0.5)
}

// scaleCornerDiam converts a global corner radius (0..20dp) and a design-time
// reference radius (in dp at DefaultCornerRadius = 12) into a DPI-scaled
// diameter for GDI/GDI+ settingsRoundFill. Returns 0 when sharp (radius == 0).
func scaleCornerDiam(cornerRadius, baseRadiusDp int, dpi int32) int32 {
	if cornerRadius <= 0 || baseRadiusDp <= 0 {
		return 0
	}
	r := (baseRadiusDp*cornerRadius + 6) / 12
	if r < 1 {
		r = 1
	}
	return scale(int32(r*2), dpi)
}
