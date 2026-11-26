package winui

import (
	"math"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	settingsGDIPlus         = windows.NewLazySystemDLL("gdiplus.dll")
	settingsGDIPlusStartup  = settingsGDIPlus.NewProc("GdiplusStartup")
	settingsGDIPlusShutdown = settingsGDIPlus.NewProc("GdiplusShutdown")
	settingsCreateGraphics  = settingsGDIPlus.NewProc("GdipCreateFromHDC")
	settingsDeleteGraphics  = settingsGDIPlus.NewProc("GdipDeleteGraphics")
	settingsSmoothingMode   = settingsGDIPlus.NewProc("GdipSetSmoothingMode")
	settingsPixelOffsetMode = settingsGDIPlus.NewProc("GdipSetPixelOffsetMode")
	settingsCreateFill      = settingsGDIPlus.NewProc("GdipCreateSolidFill")
	settingsDeleteFill      = settingsGDIPlus.NewProc("GdipDeleteBrush")
	settingsFillEllipse     = settingsGDIPlus.NewProc("GdipFillEllipseI")
	settingsCreatePath      = settingsGDIPlus.NewProc("GdipCreatePath")
	settingsDeletePath      = settingsGDIPlus.NewProc("GdipDeletePath")
	settingsAddPathArc      = settingsGDIPlus.NewProc("GdipAddPathArcI")
	settingsClosePath       = settingsGDIPlus.NewProc("GdipClosePathFigure")
	settingsFillPath        = settingsGDIPlus.NewProc("GdipFillPath")
)

type settingsAntialias struct {
	token uintptr
	tried bool
}

func (a *settingsAntialias) start() bool {
	if a.tried {
		return a.token != 0
	}
	a.tried = true
	for _, proc := range []*windows.LazyProc{
		settingsGDIPlusStartup, settingsGDIPlusShutdown, settingsCreateGraphics, settingsDeleteGraphics,
		settingsSmoothingMode, settingsPixelOffsetMode, settingsCreateFill, settingsDeleteFill, settingsFillEllipse,
		settingsCreatePath, settingsDeletePath, settingsAddPathArc, settingsClosePath, settingsFillPath,
	} {
		if proc.Find() != nil {
			return false
		}
	}
	input := struct {
		Version               uint32
		DebugCallback         uintptr
		NoBackgroundThread    int32
		NoExternalImageCodecs int32
	}{Version: 1, NoExternalImageCodecs: 1}
	var token uintptr
	status, _, _ := settingsGDIPlusStartup.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&input)), 0)
	if status != 0 {
		return false
	}
	a.token = token
	return a.token != 0
}

func (a *settingsAntialias) close() {
	if a.token != 0 {
		settingsGDIPlusShutdown.Call(a.token)
		a.token = 0
		a.tried = false
	}
}

func settingsARGB(color uintptr) uintptr {
	// COLORREF is BGR, while GDI+ takes ARGB.
	return 0xff000000 | (color&0xff)<<16 | color&0xff00 | (color>>16)&0xff
}

func (a *settingsAntialias) fill(hdc uintptr, r rect, diameter int32, color uintptr) bool {
	if !a.start() {
		return false
	}
	// Release the graphics wrapper before the caller uses this DC for GDI text
	// or any other drawing. Each shape can reuse the same native paint DC.
	var graphics uintptr
	if status, _, _ := settingsCreateGraphics.Call(hdc, uintptr(unsafe.Pointer(&graphics))); status != 0 {
		return false
	}
	defer settingsDeleteGraphics.Call(graphics)
	const smoothingModeAntiAlias = 4
	if status, _, _ := settingsSmoothingMode.Call(graphics, smoothingModeAntiAlias); status != 0 {
		return false
	}
	const pixelOffsetModeHalf = 4
	settingsPixelOffsetMode.Call(graphics, pixelOffsetModeHalf)
	var brush uintptr
	if status, _, _ := settingsCreateFill.Call(settingsARGB(color), uintptr(unsafe.Pointer(&brush))); status != 0 {
		return false
	}
	defer settingsDeleteFill.Call(brush)

	width, height := r.width(), r.height()
	diameter = min(diameter, width, height)
	if diameter == min(width, height) && width-height >= -1 && width-height <= 1 {
		status, _, _ := settingsFillEllipse.Call(graphics, brush, coord(r.Left), coord(r.Top), coord(width), coord(height))
		return status == 0
	}
	var path uintptr
	if status, _, _ := settingsCreatePath.Call(0, uintptr(unsafe.Pointer(&path))); status != 0 {
		return false
	}
	defer settingsDeletePath.Call(path)
	sweep := uintptr(math.Float32bits(90))
	for _, arc := range [...]struct {
		x, y  int32
		start float32
	}{
		{r.Left, r.Top, 180},
		{r.Right - diameter, r.Top, 270},
		{r.Right - diameter, r.Bottom - diameter, 0},
		{r.Left, r.Bottom - diameter, 90},
	} {
		// On Windows x64, the REAL values here occupy stack argument slots.
		start := uintptr(math.Float32bits(arc.start))
		if status, _, _ := settingsAddPathArc.Call(path, coord(arc.x), coord(arc.y), coord(diameter), coord(diameter), start, sweep); status != 0 {
			return false
		}
	}
	if status, _, _ := settingsClosePath.Call(path); status != 0 {
		return false
	}
	status, _, _ := settingsFillPath.Call(graphics, brush, path)
	return status == 0
}
