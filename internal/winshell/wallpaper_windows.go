package winshell

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// WallpaperPosition mirrors Windows DESKTOP_WALLPAPER_POSITION.
type WallpaperPosition int

const (
	WallpaperCenter  WallpaperPosition = 0
	WallpaperTile    WallpaperPosition = 1
	WallpaperStretch WallpaperPosition = 2
	WallpaperFit     WallpaperPosition = 3
	WallpaperFill    WallpaperPosition = 4
	WallpaperSpan    WallpaperPosition = 5
)

// MonitorWallpaper describes one active display's virtual-screen rectangle and
// assigned desktop wallpaper file path.
type MonitorWallpaper struct {
	Bounds image.Rectangle
	Path   string
}

// WallpaperState describes the active Windows desktop wallpaper layout across
// all monitors, its fit mode, and the solid fallback desktop background color.
type WallpaperState struct {
	Position   WallpaperPosition
	Background color.NRGBA
	Monitors   []MonitorWallpaper
}

var (
	coTaskMemFree         = ole32.NewProc("CoTaskMemFree")
	wallUser32            = windows.NewLazySystemDLL("user32.dll")
	wallSystemParameters  = wallUser32.NewProc("SystemParametersInfoW")
	wallGetSysColor       = wallUser32.NewProc("GetSysColor")
)

type winRect struct {
	Left, Top, Right, Bottom int32
}

// CurrentWallpaper queries the active desktop wallpaper paths, monitor bounds,
// fit mode, and solid background color via Windows IDesktopWallpaper (falling
// back to SPI_GETDESKWALLPAPER + registry when COM is unavailable).
func CurrentWallpaper() (WallpaperState, error) {
	state, err := readWallpaperCOM()
	if err == nil && (len(state.Monitors) > 0 || state.Background.A > 0) {
		return state, nil
	}
	return readWallpaperFallback(), err
}

func readWallpaperCOM() (WallpaperState, error) {
	finish, err := sta()
	if err != nil {
		return WallpaperState{}, err
	}
	defer finish()

	wp, err := instance(&clsidWallpaper, &iidWallpaper)
	if err != nil {
		return WallpaperState{}, err
	}
	defer wp.release()

	state := WallpaperState{
		Position:   WallpaperFill,
		Background: desktopSolidColor(),
	}

	// IDesktopWallpaper::GetBackgroundColor (vtable slot 9)
	var bgRef uint32
	if hr, _, _ := syscall.SyscallN(wp.methods[9], uintptr(unsafe.Pointer(wp)), uintptr(unsafe.Pointer(&bgRef))); !failed(hr) {
		state.Background = color.NRGBA{R: byte(bgRef), G: byte(bgRef >> 8), B: byte(bgRef >> 16), A: 255}
	}

	// IDesktopWallpaper::GetPosition (vtable slot 11)
	var pos uint32
	if hr, _, _ := syscall.SyscallN(wp.methods[11], uintptr(unsafe.Pointer(wp)), uintptr(unsafe.Pointer(&pos))); !failed(hr) {
		state.Position = WallpaperPosition(pos)
	}

	// IDesktopWallpaper::GetMonitorDevicePathCount (vtable slot 6)
	var count uint32
	if hr, _, _ := syscall.SyscallN(wp.methods[6], uintptr(unsafe.Pointer(wp)), uintptr(unsafe.Pointer(&count))); failed(hr) {
		return state, comError("Count wallpaper monitors", hr)
	}

	for i := uint32(0); i < count; i++ {
		// IDesktopWallpaper::GetMonitorDevicePathAt (vtable slot 5)
		var monIDPtr *uint16
		hr, _, _ := syscall.SyscallN(wp.methods[5], uintptr(unsafe.Pointer(wp)), uintptr(i), uintptr(unsafe.Pointer(&monIDPtr)))
		if failed(hr) || monIDPtr == nil {
			continue
		}

		// IDesktopWallpaper::GetMonitorRECT (vtable slot 7)
		var r winRect
		hrRect, _, _ := syscall.SyscallN(wp.methods[7], uintptr(unsafe.Pointer(wp)), uintptr(unsafe.Pointer(monIDPtr)), uintptr(unsafe.Pointer(&r)))
		if failed(hrRect) || r.Right <= r.Left || r.Bottom <= r.Top {
			coTaskMemFree.Call(uintptr(unsafe.Pointer(monIDPtr)))
			continue
		}

		// IDesktopWallpaper::GetWallpaper (vtable slot 4)
		var pathPtr *uint16
		hrPath, _, _ := syscall.SyscallN(wp.methods[4], uintptr(unsafe.Pointer(wp)), uintptr(unsafe.Pointer(monIDPtr)), uintptr(unsafe.Pointer(&pathPtr)))
		var path string
		if !failed(hrPath) && pathPtr != nil {
			path = windows.UTF16PtrToString(pathPtr)
			coTaskMemFree.Call(uintptr(unsafe.Pointer(pathPtr)))
		}
		coTaskMemFree.Call(uintptr(unsafe.Pointer(monIDPtr)))

		state.Monitors = append(state.Monitors, MonitorWallpaper{
			Bounds: image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom)),
			Path:   path,
		})
	}
	return state, nil
}

func readWallpaperFallback() WallpaperState {
	const spiGetDeskWallpaper = 0x0073
	buf := make([]uint16, 1024)
	wallSystemParameters.Call(spiGetDeskWallpaper, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])), 0)
	path := windows.UTF16ToString(buf)
	if path == "" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			candidate := filepath.Join(appData, "Microsoft", "Windows", "Themes", "TranscodedWallpaper")
			if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
				path = candidate
			}
		}
	}
	return WallpaperState{
		Position:   readRegistryWallpaperPosition(),
		Background: desktopSolidColor(),
		Monitors: []MonitorWallpaper{{
			Path: path,
		}},
	}
}

func readRegistryWallpaperPosition() WallpaperPosition {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Control Panel\Desktop`, registry.QUERY_VALUE)
	if err != nil {
		return WallpaperFill
	}
	defer k.Close()
	style, _, _ := k.GetStringValue("WallpaperStyle")
	tile, _, _ := k.GetStringValue("TileWallpaper")
	switch style {
	case "10":
		return WallpaperFill
	case "6":
		return WallpaperFit
	case "2":
		return WallpaperStretch
	case "22":
		return WallpaperSpan
	case "0":
		if tile == "1" {
			return WallpaperTile
		}
		return WallpaperCenter
	default:
		return WallpaperFill
	}
}

func desktopSolidColor() color.NRGBA {
	const colorDesktop = 1
	ref, _, _ := wallGetSysColor.Call(colorDesktop)
	return color.NRGBA{R: byte(ref), G: byte(ref >> 8), B: byte(ref >> 16), A: 255}
}
