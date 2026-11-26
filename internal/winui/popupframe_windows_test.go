package winui

import (
	"testing"
)

func TestRoundedRegionRectsMatchSDF(t *testing.T) {
	// Sharp corners collapse to a single full rectangle.
	sharp := roundedRegionRects(200, 120, 0)
	if len(sharp) != 1 || sharp[0] != (rect{0, 0, 200, 120}) {
		t.Fatalf("sharp region = %+v, want single full rect", sharp)
	}

	// Rounded corners inset top/bottom rows symmetrically and coalesce the middle band.
	const w, h, rad = int32(200), int32(120), int32(12)
	rects := roundedRegionRects(w, h, rad)
	if len(rects) < 5 || len(rects) > 30 {
		t.Fatalf("expected coalesced scanline rects (5..30), got %d", len(rects))
	}
	if rects[0].Top != 0 || rects[0].Left <= 0 || rects[0].Right >= w {
		t.Fatalf("top row should be inset for rounded corners, got %+v", rects[0])
	}
	hasFullMiddle := false
	for _, r := range rects {
		if r.Left == 0 && r.Right == w && r.height() >= h-2*rad {
			hasFullMiddle = true
			break
		}
	}
	if !hasFullMiddle {
		t.Fatalf("expected coalesced full-width middle span in %+v", rects)
	}

	rgn := buildRoundedRegion(w, h, rad)
	if rgn == 0 {
		t.Fatal("ExtCreateRegion returned nil handle")
	}
	deleteObject.Call(rgn)
}

func TestRenderPopupChromeAntialiasingShadowAndInterior(t *testing.T) {
	const w, h, pad, rad, dpi = 160, 100, 14, 12, 96
	totalW, totalH := w+2*pad, h+2*pad
	buf := make([]byte, totalW*totalH*4)
	border := rgb(0x31, 0x32, 0x44)
	renderPopupChrome(buf, w, h, pad, rad, dpi, border, true, false, 255)

	pixel := func(x, y int) (r, g, b, a byte) {
		off := (y*totalW + x) * 4
		return buf[off+2], buf[off+1], buf[off], buf[off+3]
	}

	// Premultiplied BGRA invariant must hold everywhere.
	for y := 0; y < totalH; y++ {
		for x := 0; x < totalW; x++ {
			r, g, b, a := pixel(x, y)
			if r > a || g > a || b > a {
				t.Fatalf("non-premultiplied pixel at (%d,%d): rgba=(%d,%d,%d,%d)", x, y, r, g, b, a)
			}
		}
	}

	// Deep interior must stay completely transparent so native EDIT and content show untouched.
	if r, g, b, a := pixel(pad+w/2, pad+h/2); r != 0 || g != 0 || b != 0 || a != 0 {
		t.Fatalf("interior pixel not transparent: rgba=(%d,%d,%d,%d)", r, g, b, a)
	}

	// Straight top border midpoint must be crisp opaque border color.
	if r, g, b, a := pixel(pad+w/2, pad); a != 255 || r != 0x31 || g != 0x32 || b != 0x44 {
		t.Fatalf("top border midpoint = (%d,%d,%d,%d), want (49,50,68,255)", r, g, b, a)
	}

	// Top-left corner square (pad, pad) lies outside the rounded corner arc: should have soft shadow, not opaque border.
	if _, _, _, a := pixel(pad, pad); a == 0 || a >= 220 {
		t.Fatalf("rounded corner outer cutout alpha = %d, expected soft shadow (1..219)", a)
	}

	// Corner curve should produce intermediate anti-aliased pixels.
	blended := 0
	for y := pad; y < pad+rad; y++ {
		for x := pad; x < pad+rad; x++ {
			r, _, _, a := pixel(x, y)
			if a > 20 && a < 245 && r > 0 {
				blended++
			}
		}
	}
	if blended == 0 {
		t.Fatal("expected anti-aliased blended border pixels along top-left corner curve")
	}

	// Soft drop shadow must be visible below the card and fade to zero at the overlay boundary.
	if _, _, _, a := pixel(pad+w/2, pad+h+2); a == 0 {
		t.Fatal("expected visible soft drop shadow beneath popup card")
	}
	if _, _, _, a := pixel(pad+w/2, totalH-1); a > 2 {
		t.Fatalf("outermost shadow edge did not feather cleanly: alpha=%d", a)
	}

	// Sharp mode (rad = 0) puts opaque border right at (pad, pad).
	renderPopupChrome(buf, w, h, pad, 0, dpi, border, true, false, 255)
	if r, g, b, a := pixel(pad, pad); a != 255 || r != 0x31 || g != 0x32 || b != 0x44 {
		t.Fatalf("sharp corner pixel = (%d,%d,%d,%d), want opaque border", r, g, b, a)
	}
}
