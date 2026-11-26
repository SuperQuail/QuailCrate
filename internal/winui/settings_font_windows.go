package winui

import (
	"syscall"
	"unsafe"
)

const (
	settingsDefaultCharset = 1
	settingsOutlineFont    = 4
	settingsClearType      = 5
)

var (
	settingsFontDC         = user32.NewProc("GetDC")
	settingsReleaseFontDC  = user32.NewProc("ReleaseDC")
	settingsEnumFontFamily = gdi32.NewProc("EnumFontFamiliesExW")
	settingsFoundFont      = syscall.NewCallback(foundSettingsFont)
)

// settingsLogFont has the Win32 LOGFONTW layout.
type settingsLogFont struct {
	height, width, escapement, orientation, weight int32
	italic, underline, strikeout, charset          byte
	outputPrecision, clipPrecision, quality, pitch byte
	family                                         [32]uint16
}

func foundSettingsFont(_, _, _, state uintptr) uintptr {
	*nativePointer[bool](state) = true
	return 0
}

func settingsHasFont(family string) bool {
	dc, _, _ := settingsFontDC.Call(0)
	if dc == 0 {
		return false
	}
	defer settingsReleaseFontDC.Call(0, dc)
	request := settingsLogFont{charset: settingsDefaultCharset}
	copy(request.family[:], syscall.StringToUTF16(family))
	var found bool
	settingsEnumFontFamily.Call(dc, uintptr(unsafe.Pointer(&request)), settingsFoundFont, uintptr(unsafe.Pointer(&found)), 0)
	return found
}

func settingsFontFamilies(locale string) (body, heading string) {
	if locale == "zh-CN" {
		for _, family := range []string{"Microsoft YaHei UI", "Microsoft YaHei"} {
			if settingsHasFont(family) {
				return family, family
			}
		}
	}
	body, heading = "Segoe UI", "Segoe UI"
	if settingsHasFont("Segoe UI Variable Text") {
		body, heading = "Segoe UI Variable Text", "Segoe UI Variable Text"
	}
	if settingsHasFont("Segoe UI Variable Display") {
		heading = "Segoe UI Variable Display"
	}
	return body, heading
}

func makeSettingsFont(family string, dpi, size int32, weight uintptr) uintptr {
	font, _, _ := createFont.Call(coord(-scale(size, dpi)), 0, 0, 0, weight, 0, 0, 0,
		settingsDefaultCharset, settingsOutlineFont, 0, settingsClearType, 0, uintptr(unsafe.Pointer(wstr(family))))
	return font
}
