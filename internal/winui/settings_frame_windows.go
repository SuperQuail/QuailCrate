package winui

import "unsafe"

const (
	wmSettingsNcCalcSize    = 0x0083
	wmSettingsNcHitTest     = 0x0084
	wmSettingsSystemCommand = 0x0112
	settingsFrameChanged    = 0x0020
	settingsCaptionWidth    = 46
)

var (
	settingsIsZoomed    = user32.NewProc("IsZoomed")
	settingsDpiMetric   = user32.NewProc("GetSystemMetricsForDpi")
	settingsExtendFrame = settingsDwm.NewProc("DwmExtendFrameIntoClientArea")
)

func (u *UI) configureSettingsFrame() {
	cornerPreference := uint32(2)
	settingsDwmAttribute.Call(u.settings, 33, uintptr(unsafe.Pointer(&cornerPreference)), 4)
	margin := [4]int32{0, 0, 1, 0}
	settingsExtendFrame.Call(u.settings, uintptr(unsafe.Pointer(&margin)))
	setWindowPos.Call(u.settings, 0, 0, 0, 0, 0, zFlags|swpNoZOrder|settingsFrameChanged)
}

func (u *UI) settingsFrameProcedure(msg, _, lParam uintptr) (uintptr, bool) {
	switch msg {
	case wmSettingsNcCalcSize:
		if lParam != 0 && u.settingsMaximized() {
			// Both forms of WM_NCCALCSIZE start with the proposed client rectangle.
			area := nativePointer[rect](lParam)
			work := monitorOfPoint(point{(area.Left + area.Right) / 2, (area.Top + area.Bottom) / 2}).Work
			if work.width() > 0 && work.height() > 0 {
				area.Left, area.Top = max(area.Left, work.Left), max(area.Top, work.Top)
				area.Right, area.Bottom = min(area.Right, work.Right), min(area.Bottom, work.Bottom)
			}
		}
		return 0, true
	case wmSettingsNcHitTest:
		return u.settingsHitTest(mousePoint(lParam)), true
	}
	return 0, false
}

func (u *UI) settingsMaximized() bool {
	result, _, _ := settingsIsZoomed.Call(u.settings)
	return result != 0
}

func (u *UI) settingsHitTest(screen point) uintptr {
	const (
		htClient      = 1
		htCaption     = 2
		htLeft        = 10
		htRight       = 11
		htTop         = 12
		htTopLeft     = 13
		htTopRight    = 14
		htBottom      = 15
		htBottomLeft  = 16
		htBottomRight = 17
	)
	dpi := dpiForWindow(u.settings)
	if !u.settingsMaximized() {
		frameX, _, _ := settingsDpiMetric.Call(32, uintptr(dpi))
		frameY, _, _ := settingsDpiMetric.Call(33, uintptr(dpi))
		padding, _, _ := settingsDpiMetric.Call(92, uintptr(dpi))
		window := windowRect(u.settings)
		left := screen.X < window.Left+int32(frameX+padding)
		right := screen.X >= window.Right-int32(frameX+padding)
		top := screen.Y < window.Top+int32(frameY+padding)
		bottom := screen.Y >= window.Bottom-int32(frameY+padding)
		switch {
		case top && left:
			return htTopLeft
		case top && right:
			return htTopRight
		case bottom && left:
			return htBottomLeft
		case bottom && right:
			return htBottomRight
		case left:
			return htLeft
		case right:
			return htRight
		case top:
			return htTop
		case bottom:
			return htBottom
		}
	}
	p := screen
	screenToClient.Call(u.settings, uintptr(unsafe.Pointer(&p)))
	client := clientRect(u.settings)
	if p.Y < scale(settingsTitleHeight, dpi) && p.X < client.Right-scale(3*settingsCaptionWidth, dpi) {
		return htCaption
	}
	return htClient
}

func (m settingsMetrics) captionButton(index int32) rect {
	left := m.width - (3-index)*settingsCaptionWidth
	return rect{left, 0, left + settingsCaptionWidth, settingsTitleHeight - 1}
}

func (u *UI) refreshSettingsCaption() {
	label := u.label("最大化", "Maximize")
	if u.settingsMaximized() {
		label = u.label("还原", "Restore")
	}
	setText(u.settingsFields[settingsCaptionMaximize], label)
}

func (u *UI) activateSettingsCaption(id int) {
	switch id {
	case settingsCaptionMinimize:
		sendMessage.Call(u.settings, wmSettingsSystemCommand, 0xf020, 0) // SC_MINIMIZE
	case settingsCaptionMaximize:
		command := uintptr(0xf030) // SC_MAXIMIZE
		if u.settingsMaximized() {
			command = 0xf120
		} // SC_RESTORE
		sendMessage.Call(u.settings, wmSettingsSystemCommand, command, 0)
	case settingsCaptionClose:
		sendMessage.Call(u.settings, wmClose, 0, 0)
	}
}
