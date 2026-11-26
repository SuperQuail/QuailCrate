package winui

import (
	"image"
	"image/color"
	"testing"

	"quailcrate/internal/winshell"
)

func TestComposeMonitorWallpaperPositions(t *testing.T) {
	// 200x100 source image: left half pure red, right half pure blue.
	src := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			if x < 100 {
				src.SetRGBA(x, y, color.RGBA{R: 240, G: 20, B: 20, A: 255})
			} else {
				src.SetRGBA(x, y, color.RGBA{R: 20, G: 40, B: 240, A: 255})
			}
		}
	}
	mon := image.Rect(0, 0, 400, 400) // square monitor -> downsampled to 100x100 at scaleDiv=4
	bg := color.NRGBA{R: 12, G: 34, B: 56, A: 255}

	// Fill covers top and bottom completely (no bg letterbox).
	filled := composeMonitorWallpaper(src, mon, mon, winshell.WallpaperFill, bg, 4)
	if filled.Rect.Size() != image.Pt(100, 100) {
		t.Fatalf("filled size = %v, want 100x100", filled.Rect.Size())
	}
	topFill := filled.RGBAAt(10, 2)
	if topFill.R < 180 {
		t.Fatalf("WallpaperFill top-left should come from red half, got %+v", topFill)
	}

	// Fit letterboxes top & bottom with solid background color.
	fitted := composeMonitorWallpaper(src, mon, mon, winshell.WallpaperFit, bg, 4)
	topBar := fitted.RGBAAt(50, 2)
	if topBar.R != bg.R || topBar.G != bg.G || topBar.B != bg.B {
		t.Fatalf("WallpaperFit top bar = %+v, want bg %+v", topBar, bg)
	}
	midRight := fitted.RGBAAt(75, 50)
	if midRight.B < 180 {
		t.Fatalf("WallpaperFit middle-right should come from blue half, got %+v", midRight)
	}

	// Span across two side-by-side 400x400 monitors puts red on left monitor and blue on right monitor.
	leftMon := image.Rect(-400, 0, 0, 400)
	rightMon := image.Rect(0, 0, 400, 400)
	virt := leftMon.Union(rightMon)
	spanLeft := composeMonitorWallpaper(src, leftMon, virt, winshell.WallpaperSpan, bg, 4)
	spanRight := composeMonitorWallpaper(src, rightMon, virt, winshell.WallpaperSpan, bg, 4)
	if spanLeft.RGBAAt(50, 50).R < 180 || spanRight.RGBAAt(50, 50).B < 180 {
		t.Fatalf("WallpaperSpan split mismatch: left=%+v right=%+v", spanLeft.RGBAAt(50, 50), spanRight.RGBAAt(50, 50))
	}
}

func TestWallpaperBackdropSliceSingleAndMultiMonitor(t *testing.T) {
	imgA := image.NewRGBA(image.Rect(0, 0, 100, 50))
	fillSolidRGBA(imgA, color.NRGBA{R: 220, G: 50, B: 60, A: 255})
	imgB := image.NewRGBA(image.Rect(0, 0, 100, 50))
	fillSolidRGBA(imgB, color.NRGBA{R: 40, G: 90, B: 230, A: 255})

	bd := wallpaperBackdrop{
		monitors: []monitorBackdrop{
			{bounds: image.Rect(0, 0, 400, 200), blurred: imgA},
			{bounds: image.Rect(400, 0, 800, 200), blurred: imgB},
		},
	}

	// Single-monitor slice returns zero-copy reference to imgA with exact fractional UV rect.
	single := bd.sliceForRect(image.Rect(100, 40, 300, 140))
	if single == nil || single.Image != imgA {
		t.Fatalf("expected direct imgA reference for single-monitor widget, got %+v", single)
	}
	if single.Source.Min.X != 25 || single.Source.Min.Y != 10 || single.Source.Max.X != 75 || single.Source.Max.Y != 35 {
		t.Fatalf("unexpected single-monitor UV rect: %+v", single.Source)
	}

	// Multi-monitor straddling slice seamlessly blends left (red) and right (blue).
	straddle := bd.sliceForRect(image.Rect(360, 40, 440, 120))
	if straddle == nil || straddle.Image == nil || straddle.Image == imgA || straddle.Image == imgB {
		t.Fatalf("expected stitched composite for straddling widget, got %+v", straddle)
	}
	leftPx := straddle.Image.RGBAAt(2, straddle.Image.Rect.Dy()/2)
	rightPx := straddle.Image.RGBAAt(straddle.Image.Rect.Dx()-3, straddle.Image.Rect.Dy()/2)
	if leftPx.R < 200 || rightPx.B < 200 {
		t.Fatalf("straddling slice colors mismatch: left=%+v right=%+v", leftPx, rightPx)
	}
}
