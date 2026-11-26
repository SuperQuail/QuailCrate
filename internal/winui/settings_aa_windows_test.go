package winui

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestSettingsRoundedGeometryProducesBlendedEdges(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	const side = 64
	dc, _, _ := gdi32.NewProc("CreateCompatibleDC").Call(0)
	if dc == 0 {
		t.Fatal("Could not create an offscreen DC")
	}
	defer gdi32.NewProc("DeleteDC").Call(dc)

	// A top-down, 32-bit DIB gives the test direct access to the rendered pixels.
	header := struct {
		Size               uint32
		Width, Height      int32
		Planes, BitCount   uint16
		Compression, Image uint32
		Xppm, Yppm         int32
		Used, Important    uint32
	}{Size: 40, Width: side, Height: -side, Planes: 1, BitCount: 32}
	var bits uintptr
	bitmap, _, _ := gdi32.NewProc("CreateDIBSection").Call(dc, uintptr(unsafe.Pointer(&header)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == 0 {
		t.Fatal("Could not create the offscreen bitmap")
	}
	defer deleteObject.Call(bitmap)
	previous, _, _ := selectObject.Call(dc, bitmap)
	defer selectObject.Call(dc, previous)

	background, foreground := rgb(255, 255, 255), rgb(20, 102, 91)
	fill(dc, rect{Right: side, Bottom: side}, background)
	var aa settingsAntialias
	defer aa.close()
	shapes := []struct {
		name     string
		area     rect
		diameter int32
	}{
		{"switch track", rect{2, 2, 37, 21}, 19},
		{"switch thumb", rect{43, 2, 57, 16}, 14},
		{"title badge", rect{2, 29, 24, 51}, 12},
	}
	for _, shape := range shapes {
		if !aa.fill(dc, shape.area, shape.diameter, foreground) {
			t.Fatalf("Could not render %s using native antialiasing", shape.name)
		}
	}
	gdi32.NewProc("GdiFlush").Call()

	pixels := unsafe.Slice(nativePointer[uint32](bits), side*side)
	for _, shape := range shapes {
		area := shape.area
		blended := 0
		for y := area.Top; y < area.Bottom; y++ {
			for x := area.Left; x < area.Right; x++ {
				pixel := pixels[y*side+x] & 0x00ffffff
				if pixel != 0x00ffffff && pixel != uint32(settingsARGB(foreground))&0x00ffffff {
					blended++
				}
			}
		}
		center := pixels[(area.Top+area.height()/2)*side+area.Left+area.width()/2] & 0x00ffffff
		if center != uint32(settingsARGB(foreground))&0x00ffffff || blended == 0 {
			t.Errorf("%s: got center %#06x and %d blended pixels", shape.name, center, blended)
		}
	}
}
