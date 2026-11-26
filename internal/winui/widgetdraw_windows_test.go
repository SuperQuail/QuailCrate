package winui

import (
	"image"
	"image/color"
	"runtime"
	"testing"
	"unsafe"

	"quailcrate/internal/gioui"
	v1 "quailcrate/pluginapi/v1"
)

func widgetTestDC(t *testing.T, size image.Point) uintptr {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	dc, _, _ := gdi32.NewProc("CreateCompatibleDC").Call(0)
	if dc == 0 {
		t.Fatal("Could not create a memory DC")
	}
	t.Cleanup(func() { gdi32.NewProc("DeleteDC").Call(dc) })
	info := widgetBitmapInfo{Size: 40, Width: int32(size.X), Height: -int32(size.Y), Planes: 1, Bits: 32}
	var bits uintptr
	bitmap, _, _ := gdi32.NewProc("CreateDIBSection").Call(dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 {
		t.Fatal("Could not create test DIB")
	}
	t.Cleanup(func() { deleteObject.Call(bitmap) })
	prior, _, _ := selectObject.Call(dc, bitmap)
	t.Cleanup(func() { selectObject.Call(dc, prior) })
	return dc
}

func TestWidgetBitmapPreservesChannelsAndOrientation(t *testing.T) {
	dc := widgetTestDC(t, image.Pt(2, 2))
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, color.RGBA{R: 252, B: 8, A: 255})
	img.SetRGBA(0, 1, color.RGBA{R: 4, G: 248, A: 255})
	if !drawWidgetBitmap(dc, img) {
		t.Fatal("GDI rejected the Gio frame format")
	}
	getPixel := gdi32.NewProc("GetPixel")
	upper, _, _ := getPixel.Call(dc, 0, 0)
	lower, _, _ := getPixel.Call(dc, 0, 1)
	if upper != rgb(252, 0, 8) || lower != rgb(4, 248, 0) {
		t.Fatalf("Gio frame colors at top and bottom = %#x and %#x", upper, lower)
	}
}

func TestPremultipliedBGRAPacking(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 128}) // clamps R>A to A
	img.SetRGBA(1, 0, color.RGBA{R: 10, G: 20, B: 30, A: 0})     // fully transparent corner
	dst := make([]byte, 8)
	rgbaToPremultipliedBGRA(dst, img)
	want := []byte{50, 100, 128, 128, 0, 0, 0, 0}
	for i := range want {
		if dst[i] != want[i] {
			t.Fatalf("byte %d = %d, want %d (all=%v)", i, dst[i], want[i], dst)
		}
	}
}

func TestWidgetGDIFallbackDrawsPublicView(t *testing.T) {
	size := image.Pt(240, 170)
	dc := widgetTestDC(t, size)
	c := widgetColors()
	c.Translucent = false
	opt := gioui.WidgetOptions{DPI: 96, Size: size, Colors: c}
	canvas := widgetGDICanvas{dc: dc, dpi: 96, fonts: make(map[widgetFontKey]uintptr)}
	defer canvas.release()
	g := gioui.LayoutWidget(v1.View{Title: "Archive", Controls: []v1.Control{{ID: "items", Type: "list", Drop: true, Text: "Drop files"}}}, opt, &canvas)
	pixel := gdi32.NewProc("GetPixel")
	border, _, _ := pixel.Call(dc, 0, 0)
	if border != rgb(c.Border.R, c.Border.G, c.Border.B) || len(g.DropZones) != 1 {
		t.Fatal("Missing widget frame or drop target on the GDI fallback")
	}
	background := rgb(c.Background.R, c.Background.G, c.Background.B)
	for y := 4; y < 35; y++ {
		for x := 12; x < 90; x++ {
			p, _, _ := pixel.Call(dc, uintptr(x), uintptr(y))
			if p != background {
				return
			}
		}
	}
	t.Fatal("Missing fallback title text")
}
