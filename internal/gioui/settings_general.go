package gioui

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"gioui.org/font"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"quailcrate/internal/theme"
)

// toggleSwitch renders a 36x20 pill switch flush against the right edge of r so it
// aligns cleanly on the same right margin (w - 16dp) as adjacent card buttons.
func (f settingsFrame) toggleSwitch(gtx layout.Context, r image.Rectangle, b *widget.Clickable, desc string, on, enabled bool) {
	p := gtx.Dp
	btnCtx := gtx
	if !enabled {
		btnCtx = gtx.Disabled()
	}
	f.button(btnCtx, r, b, desc, func(gtx layout.Context) {
		semantic.Switch.Add(gtx.Ops)
		semantic.SelectedOp(on).Add(gtx.Ops)
		bw, bh := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
		midY := bh / 2
		trackRight := bw
		trackLeft := max(0, trackRight-p(36))
		track := image.Rect(trackLeft, midY-p(10), trackRight, midY+p(10))
		at := track.Max.X - p(10)
		if on {
			bg, knob := f.p.Accent, f.p.OnAccent
			if !enabled {
				bg, knob = f.p.Line, f.p.Surface
			}
			settingsFill(gtx, track, 10, bg)
			settingsFill(gtx, image.Rect(at-p(6), midY-p(6), at+p(6), midY+p(6)), 6, knob)
			return
		}
		border, inner, knob := f.p.Secondary, f.p.Surface, f.p.Secondary
		if !enabled {
			border = f.p.Line
			inner = f.p.Background
			knob = theme.WithAlpha(f.p.Secondary, 130)
		}
		settingsFill(gtx, track, 10, border)
		settingsFill(gtx, track.Inset(max(1, p(1))), 9, inner)
		at = track.Min.X + p(10)
		settingsFill(gtx, image.Rect(at-p(6), midY-p(6), at+p(6), midY+p(6)), 6, knob)
	})
}

func (f settingsFrame) actionButton(gtx layout.Context, r image.Rectangle, b *widget.Clickable, title string, primary, enabled bool) {
	if enabled {
		f.textButton(gtx, r, b, title, primary)
		return
	}
	btnR := f.radius(7)
	f.button(gtx.Disabled(), r, b, title, func(gtx layout.Context) {
		bg := f.p.Background
		border := theme.WithAlpha(f.p.Line, 175)
		ink := theme.WithAlpha(f.p.Secondary, 175)
		if f.model.HighContrast {
			bg = f.p.Surface
			border = f.p.Line
			ink = f.p.Secondary
		}
		settingsFill(gtx, settingsBounds(gtx), btnR, bg)
		settingsStroke(gtx, settingsBounds(gtx), btnR, 1, border)
		f.label(gtx, settingsBounds(gtx), title, 13, font.Medium, ink, layout.Center)
	})
}

func (f settingsFrame) paragraphSmall(gtx layout.Context, r image.Rectangle, value string, c color.NRGBA, lines int) {
	settingsArea(gtx, r, func(gtx layout.Context) {
		layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.Y = 0
			l := material.Label(f.theme, 12.5, value)
			l.Color, l.Font.Typeface, l.MaxLines = c, f.typeface(), lines
			return l.Layout(gtx)
		})
	})
}

func (f settingsFrame) autostartCard(gtx layout.Context) int {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	a := f.model.Autostart
	canToggle := a.CanToggle()
	if f.autostartToggle.Clicked(gtx) && canToggle {
		f.emit(SettingsAction{Kind: ToggleAutostart, Enabled: !a.Enabled})
	}
	showRetry := a.Error == "read_failed" && !a.Loading && !a.Saving
	if f.autostartRetry.Clicked(gtx) && showRetry {
		f.emit(SettingsAction{Kind: RefreshAutostart})
	}

	notice := f.autostartNoticeText(a)
	compact := w < p(500)
	headerH := p(76)
	if compact {
		headerH = p(86)
	}
	noticeH := 0
	if notice != "" {
		noticeH = p(36)
		if compact {
			noticeH = p(44)
		}
	}
	cardH := headerH
	if noticeH > 0 {
		cardH += noticeH + p(12)
	}

	f.box(gtx, image.Rect(0, 0, w, cardH), 12, f.p.Surface)

	title := f.heading("开机启动", "Start on sign-in")
	sub, subInk := f.autostartSubtitle(a)
	textRight := w - p(68)
	f.label(gtx, image.Rect(p(18), p(12), textRight, p(38)), title, 16, font.SemiBold, f.p.Text, layout.W)
	f.paragraphSmall(gtx, image.Rect(p(18), p(40), textRight, headerH-p(8)), sub, subInk, 2)
	f.toggleSwitch(gtx, image.Rect(w-p(60), (headerH-p(36))/2, w-p(16), (headerH+p(36))/2), &f.autostartToggle, title, a.Enabled, canToggle)

	if noticeH > 0 {
		banner := image.Rect(p(16), headerH, w-p(16), headerH+noticeH)
		settingsFill(gtx, banner, f.radius(6), f.p.WarningBg)
		settingsStroke(gtx, banner, f.radius(6), 1, theme.WithAlpha(f.p.Warning, 110))
		bannerRight := banner.Max.X - p(10)
		if showRetry {
			retryW := p(66)
			retryRect := image.Rect(banner.Max.X-retryW-p(6), banner.Min.Y+(noticeH-p(26))/2, banner.Max.X-p(6), banner.Min.Y+(noticeH+p(26))/2)
			f.actionButton(gtx, retryRect, &f.autostartRetry, f.tr("重试", "Retry"), false, true)
			bannerRight = retryRect.Min.X - p(8)
		}
		f.paragraphSmall(gtx, image.Rect(banner.Min.X+p(10), banner.Min.Y+p(2), bannerRight, banner.Max.Y-p(2)), notice, f.p.Warning, 2)
	}
	return cardH
}

func (f settingsFrame) autostartSubtitle(a SettingsAutostart) (string, color.NRGBA) {
	if !a.Loaded {
		return f.tr("正在读取当前 Windows 用户的登录启动状态...", "Checking Windows sign-in startup status..."), f.p.Secondary
	}
	if a.Saving {
		ink := f.p.Accent
		if f.model.HighContrast {
			ink = f.p.Text
		}
		return f.tr("正在保存并确认登录启动状态...", "Saving and verifying sign-in startup status..."), ink
	}
	if !a.Available && a.Error == "" {
		return f.tr(
			"登录 Windows 时自动启动 · 仅正式安装版或便携版可用",
			"Launch automatically on Windows sign-in · Packaged builds only",
		), f.p.Secondary
	}
	if a.Enabled && a.Available && !a.Conflict {
		return f.tr(
			"已为当前 Windows 用户注册登录启动（可在系统「启动应用」中单独暂停）",
			"Registered to start when you sign in to Windows (can be paused in Startup Apps)",
		), f.p.Secondary
	}
	return f.tr(
		"在当前 Windows 用户登录时自动启动 QuailCrate",
		"Start QuailCrate automatically when you sign in to Windows",
	), f.p.Secondary
}

func (f settingsFrame) autostartNoticeText(a SettingsAutostart) string {
	if !a.Loaded || a.Saving {
		return ""
	}
	if a.Error != "" {
		return f.autostartErrorText(a.Error)
	}
	if a.Conflict {
		return f.tr(
			"开机启动入口已被其他位置的 QuailCrate 占用，当前副本已禁用此开关。",
			"Another copy of QuailCrate owns the sign-in startup entry. This switch is disabled.",
		)
	}
	return ""
}

func (f settingsFrame) autostartErrorText(code string) string {
	switch code {
	case "conflict":
		return f.tr(
			"开机启动入口已被其他位置的 QuailCrate 占用，未修改注册状态。",
			"Another copy of QuailCrate owns the sign-in startup entry. No change was made.",
		)
	case "unavailable":
		return f.tr(
			"当前安装路径或启动参数不支持注册 Windows 登录启动。",
			"Sign-in startup is unavailable for this installation path or launch configuration.",
		)
	case "read_failed":
		return f.tr(
			"读取 Windows 登录启动注册状态失败，请检查权限后重试。",
			"Could not read Windows sign-in startup status. Check permissions and retry.",
		)
	case "write_failed":
		return f.tr(
			"保存开机启动状态失败，已恢复显示系统当前状态。",
			"Failed to save sign-in startup status. Showing current system state.",
		)
	default:
		return f.tr(
			"开机启动状态异常，已恢复显示系统当前状态。",
			"Sign-in startup error. Showing current system state.",
		)
	}
}

func (f settingsFrame) updatesCard(gtx layout.Context) int {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	u := f.model.Update

	supported := u.Supported()
	canToggleAuto := u.CanToggleAutoUpdate()
	canCheck := u.CanCheck()
	canRestart := u.CanRestartToUpdate()
	hasRelease := u.ReleaseURL != ""
	manualMigration := hasRelease && !u.CanInstall
	showCheckBtn := supported
	showRestartBtn := canRestart || u.RestartPending || u.Phase == "installing"
	showManualBtn := !showRestartBtn && manualMigration
	twoButtons := showCheckBtn && (showRestartBtn || showManualBtn)

	// Always drain clickables so hidden/disabled clicks never buffer across states.
	if f.autoUpdateToggle.Clicked(gtx) && supported && canToggleAuto {
		f.emit(SettingsAction{Kind: ToggleAutoUpdate, Enabled: !u.AutoUpdate})
	}
	if f.checkUpdateButton.Clicked(gtx) && showCheckBtn && canCheck {
		f.emit(SettingsAction{Kind: CheckUpdateNow})
	}
	if f.restartUpdateButton.Clicked(gtx) && showRestartBtn && canRestart {
		f.emit(SettingsAction{Kind: RestartUpdate})
	}
	if f.releaseLinkButton.Clicked(gtx) && hasRelease {
		f.emit(SettingsAction{Kind: OpenUpdateRelease, Value: u.ReleaseURL})
	}

	zh := strings.HasPrefix(f.model.Locale, "zh")
	verText := formatUpdateVersion(u.CurrentVersion)

	checkLabel := f.tr("检查更新", "Check for updates")
	if u.Phase == "checking" || u.CheckPending {
		checkLabel = f.tr("检查中...", "Checking...")
	}
	checkW := p(96)
	if !zh {
		checkW = p(142)
	}

	var primaryLabel string
	var primaryW int
	if showRestartBtn {
		primaryLabel = f.tr("立即重启并更新", "Restart and update")
		if u.Phase == "installing" {
			primaryLabel = f.tr("正在安装...", "Installing...")
		} else if u.RestartPending && u.Phase == "downloading" {
			primaryLabel = f.tr("正在下载...", "Downloading...")
		} else if u.RestartPending {
			primaryLabel = f.tr("正在准备...", "Preparing...")
		}
		primaryW = p(134)
		if !zh {
			primaryW = p(158)
		}
	} else if showManualBtn {
		primaryLabel = f.tr("打开发布页下载", "Open release page")
		primaryW = p(130)
		if !zh {
			primaryW = p(152)
		}
	}

	hasHeaderButtons := showCheckBtn || showRestartBtn || showManualBtn
	buttonsInline := true
	if twoButtons && w < p(590) {
		buttonsInline = false
	} else if hasHeaderButtons && w < p(410) {
		buttonsInline = false
	}

	showProgress := u.Phase == "downloading"
	warningText := f.updateWarningText(u)
	showReleaseStrip := manualMigration

	// Measure sections deterministically before drawing the outer card box.
	statusLine, statusInk := f.updateHeaderStatus(u)
	headerH := p(76)
	if w < p(500) || (buttonsInline && twoButtons) || len([]rune(statusLine)) > 38 {
		headerH = p(88)
	}
	btnRowH := 0
	if hasHeaderButtons && !buttonsInline {
		btnRowH = p(44)
	}
	progressH := 0
	if showProgress {
		progressH = p(34)
	}
	releaseStripH := 0
	if showReleaseStrip {
		releaseStripH = p(34)
	}
	warningH := 0
	if warningText != "" {
		warningH = p(40)
		if w < p(540) {
			warningH = p(52)
		}
	}
	autoH := 0
	if supported {
		autoH = p(60)
		if w < p(480) {
			autoH = p(70)
		}
	}

	cardH := headerH + btnRowH
	if progressH > 0 {
		cardH += progressH + p(6)
	}
	if releaseStripH > 0 {
		cardH += releaseStripH + p(8)
	}
	if warningH > 0 {
		cardH += warningH + p(8)
	}
	if autoH > 0 {
		if progressH > 0 || releaseStripH > 0 || warningH > 0 || btnRowH > 0 {
			cardH += p(4)
		}
		cardH += autoH
	} else if progressH > 0 || releaseStripH > 0 || warningH > 0 || btnRowH > 0 {
		cardH += p(6)
	}

	f.box(gtx, image.Rect(0, 0, w, cardH), 12, f.p.Surface)

	// 1. Header row:
	// - Supported builds: Title + soft version tag on left, action button(s) flush right at w - 16dp.
	// - Unsupported/dev builds: Title + single status line on left, read-only version pill flush right at w - 16dp (no dead button).
	title := f.heading("应用更新", "App updates")
	verW := min(p(168), max(p(64), p(unit.Dp(len(verText)*7+18))))

	rightEdge := w - p(16)
	if !hasHeaderButtons {
		pillY := (headerH - p(26)) / 2
		verRect := image.Rect(rightEdge-verW, pillY, rightEdge, pillY+p(26))
		f.drawVersionTag(gtx, verRect, verText, false)
		rightEdge = verRect.Min.X - p(12)
	} else if buttonsInline {
		btnY := (headerH - p(34)) / 2
		if showCheckBtn {
			checkRect := image.Rect(rightEdge-checkW, btnY, rightEdge, btnY+p(34))
			f.actionButton(gtx, checkRect, &f.checkUpdateButton, checkLabel, false, canCheck)
			rightEdge = checkRect.Min.X - p(8)
		}
		if showRestartBtn {
			restartRect := image.Rect(rightEdge-primaryW, btnY, rightEdge, btnY+p(34))
			f.actionButton(gtx, restartRect, &f.restartUpdateButton, primaryLabel, true, canRestart)
			rightEdge = restartRect.Min.X - p(10)
		} else if showManualBtn {
			manualRect := image.Rect(rightEdge-primaryW, btnY, rightEdge, btnY+p(34))
			f.actionButton(gtx, manualRect, &f.releaseLinkButton, primaryLabel, true, true)
			rightEdge = manualRect.Min.X - p(10)
		}
	}

	titleW := p(74)
	if !zh {
		titleW = p(102)
	}
	f.label(gtx, image.Rect(p(18), p(12), min(rightEdge, p(18)+titleW), p(38)), title, 16, font.SemiBold, f.p.Text, layout.W)
	if hasHeaderButtons {
		badgeLeft := p(18) + titleW + p(8)
		if badgeLeft+p(56) <= rightEdge {
			badgeRect := image.Rect(badgeLeft, p(15), min(rightEdge, badgeLeft+verW), p(36))
			f.drawVersionTag(gtx, badgeRect, verText, true)
		}
	}

	f.paragraphSmall(gtx, image.Rect(p(18), p(40), max(p(18), rightEdge), headerH-p(8)), statusLine, statusInk, 2)

	y := headerH

	// Optional wrapped button row on narrow widths.
	if hasHeaderButtons && !buttonsInline {
		x := p(16)
		if showRestartBtn {
			r := image.Rect(x, y, min(w-p(16), x+primaryW), y+p(34))
			f.actionButton(gtx, r, &f.restartUpdateButton, primaryLabel, true, canRestart)
			x = r.Max.X + p(8)
		} else if showManualBtn {
			r := image.Rect(x, y, min(w-p(16), x+primaryW), y+p(34))
			f.actionButton(gtx, r, &f.releaseLinkButton, primaryLabel, true, true)
			x = r.Max.X + p(8)
		}
		if showCheckBtn {
			f.actionButton(gtx, image.Rect(x, y, min(w-p(16), x+checkW), y+p(34)), &f.checkUpdateButton, checkLabel, false, canCheck)
		}
		y += btnRowH
	}

	// 2. Inline download progress bar (only while downloading).
	if showProgress {
		trackR := image.Rect(p(18), y+p(4), w-p(18), y+p(10))
		settingsFill(gtx, trackR, f.radius(3), f.p.Background)
		ratio := 0.0
		if u.TotalBytes > 0 && u.DownloadedBytes > 0 {
			ratio = min(1, float64(u.DownloadedBytes)/float64(u.TotalBytes))
		}
		if fillW := int(float64(trackR.Dx()) * ratio); fillW > 0 {
			settingsFill(gtx, image.Rect(trackR.Min.X, trackR.Min.Y, trackR.Min.X+fillW, trackR.Max.Y), f.radius(3), f.p.Accent)
		}
		ink := f.p.Accent
		if f.model.HighContrast {
			ink = f.p.Text
		}
		f.monoLabel(gtx, image.Rect(p(18), y+p(14), w-p(18), y+p(32)), f.updateProgressText(u), 11, font.Medium, ink, layout.W)
		y += progressH + p(6)
	}

	// 3. Compact release link strip (shown when portable manual migration is needed).
	if showReleaseStrip {
		strip := image.Rect(p(16), y, w-p(16), y+releaseStripH)
		ink := f.p.Accent
		if f.model.HighContrast {
			ink = f.p.OnAccent
		}
		settingsFill(gtx, strip, f.radius(6), f.p.Selected)
		settingsStroke(gtx, strip, f.radius(6), 1, theme.WithAlpha(f.p.Accent, 95))
		f.monoLabel(gtx, image.Rect(strip.Min.X+p(10), strip.Min.Y, strip.Max.X-p(10), strip.Max.Y), u.ReleaseURL, 11, font.Normal, ink, layout.W)
		y += releaseStripH + p(8)
	}

	// 4. Compact warning strip (only on failure / last install failure / action error).
	if warningH > 0 {
		warnRect := image.Rect(p(16), y, w-p(16), y+warningH)
		settingsFill(gtx, warnRect, f.radius(6), f.p.WarningBg)
		settingsStroke(gtx, warnRect, f.radius(6), 1, theme.WithAlpha(f.p.Warning, 110))
		textRight := warnRect.Max.X - p(10)
		if hasRelease && !manualMigration {
			linkBtnW := p(86)
			if !zh {
				linkBtnW = p(98)
			}
			linkBtn := image.Rect(warnRect.Max.X-linkBtnW-p(6), warnRect.Min.Y+(warningH-p(26))/2, warnRect.Max.X-p(6), warnRect.Min.Y+(warningH+p(26))/2)
			f.actionButton(gtx, linkBtn, &f.releaseLinkButton, f.tr("查看发布页", "Release page"), false, true)
			textRight = linkBtn.Min.X - p(8)
		}
		f.paragraphSmall(gtx, image.Rect(warnRect.Min.X+p(10), warnRect.Min.Y+p(2), textRight, warnRect.Max.Y-p(2)), warningText, f.p.Warning, 2)
		y += warningH + p(8)
	}

	// 5. Auto-update toggle sub-row (only shown when online updates are supported).
	if supported {
		if progressH > 0 || releaseStripH > 0 || warningH > 0 || btnRowH > 0 {
			y += p(4)
		}
		settingsFill(gtx, image.Rect(p(16), y, w-p(16), y+max(1, p(1))), 0, f.p.Line)
		autoTitle := f.tr("自动更新", "Automatic updates")
		autoDesc := f.tr(
			"后台检查更新，下载就绪后在托盘退出时自动安装",
			"Check in the background and install silently on tray exit when ready",
		)
		f.label(gtx, image.Rect(p(18), y+p(8), w-p(68), y+p(30)), autoTitle, 13.5, font.Medium, f.p.Text, layout.W)
		f.paragraphSmall(gtx, image.Rect(p(18), y+p(30), w-p(68), y+autoH-p(6)), autoDesc, f.p.Secondary, 2)
		f.toggleSwitch(gtx, image.Rect(w-p(60), y+(autoH-p(36))/2, w-p(16), y+(autoH+p(36))/2), &f.autoUpdateToggle, autoTitle, u.AutoUpdate, canToggleAuto)
	}

	return cardH
}

// drawVersionTag renders a soft, borderless mono pill so the version number stays
// readable without competing against the section title or action buttons.
func (f settingsFrame) drawVersionTag(gtx layout.Context, r image.Rectangle, text string, accented bool) {
	rad := f.radius(5)
	bg := f.p.Background
	ink := f.p.Secondary
	if accented {
		bg = f.p.Selected
		ink = f.p.Accent
		if f.model.HighContrast {
			ink = f.p.OnAccent
		}
	}
	settingsFill(gtx, r, rad, bg)
	if f.model.HighContrast {
		settingsStroke(gtx, r, rad, 1, f.p.Line)
	}
	f.monoLabel(gtx, r, text, 11, font.Medium, ink, layout.Center)
}

func formatUpdateVersion(ver string) string {
	ver = strings.TrimSpace(ver)
	if ver == "" {
		return "dev"
	}
	if strings.HasPrefix(ver, "v") {
		return ver
	}
	return "v" + ver
}

func (f settingsFrame) updateHeaderStatus(u SettingsUpdate) (string, color.NRGBA) {
	target := formatUpdateVersion(u.TargetVersion)
	accentInk := f.p.Accent
	if f.model.HighContrast {
		accentInk = f.p.Text
	}
	checkedSuffix := ""
	if !u.LastCheckedAt.IsZero() {
		stamp := u.LastCheckedAt.Local().Format("2006-01-02 15:04")
		checkedSuffix = " · " + f.tr("上次检查 "+stamp, "Last checked "+stamp)
	}

	switch {
	case u.Phase == "installing":
		return f.tr("正在启动更新程序并重启...", "Launching updater and restarting..."), accentInk
	case u.RestartPending && u.Phase == "ready":
		return f.tr("正在准备重启并安装 "+target+"...", "Preparing to restart and install "+target+"..."), accentInk
	case u.Phase == "downloading":
		if u.RestartPending {
			return f.tr("正在下载 "+target+"，校验完成后将自动重启安装", "Downloading "+target+"; will restart to install once verified"), accentInk
		}
		if u.TargetVersion != "" {
			return f.tr("正在后台下载 "+target+"...", "Downloading "+target+"..."), accentInk
		}
		return f.tr("正在下载更新...", "Downloading update..."), accentInk
	case u.Phase == "checking" || u.CheckPending:
		return f.tr("正在检查更新...", "Checking for updates..."), accentInk
	case u.Phase == "ready":
		if u.AutoUpdate {
			return f.tr(target+" 已就绪 · 可立即重启或在托盘退出时静默安装", target+" ready · Restart now or install silently on tray exit"), accentInk
		}
		return f.tr(target+" 已下载并校验完成，可立即重启更新", target+" is ready to install"), accentInk
	case u.Phase == "available":
		if u.CanInstall {
			if u.TotalBytes > 0 {
				return fmt.Sprintf(
					f.tr("发现新版本 %s（%s）· 点击更新将下载校验后重启", "Update %s available (%s) · Downloads and verifies before restart"),
					target, formatByteSize(u.TotalBytes),
				), accentInk
			}
			return f.tr("发现新版本 "+target+" · 点击更新将下载校验后重启", "Update "+target+" available · Downloads and verifies before restart"), accentInk
		}
		return f.tr(
			"发现新版本 "+target+" · 旧便携版需手动覆盖更新一次，后续支持自动更新",
			"Update "+target+" available · One-time manual download required for legacy portable",
		), accentInk
	case u.Phase == "failed":
		if u.CanInstall && u.TargetVersion != "" {
			return f.tr("更新未完成 · 可点击「立即重启并更新」重试 "+target, "Update incomplete · Click Restart and update to retry "+target), f.p.Warning
		}
		return f.tr("更新未完成"+checkedSuffix, "Update failed"+checkedSuffix), f.p.Warning
	default:
		if !u.Supported() {
			return f.tr("开发构建不支持在线更新", "Online updates are unavailable in development builds"), f.p.Secondary
		}
		if u.LastCheckedAt.IsZero() {
			return f.tr("尚未检查更新", "Not checked yet"), f.p.Secondary
		}
		return f.tr("已是最新版本"+checkedSuffix, "Up to date"+checkedSuffix), f.p.Secondary
	}
}

func (f settingsFrame) updateWarningText(u SettingsUpdate) string {
	if u.Phase == "failed" {
		code := u.ErrorCode
		if code == "" {
			code = u.LastInstallErrorCode
		}
		return f.updateErrorText(code)
	}
	if u.Phase != "installing" && u.LastInstallErrorCode != "" {
		return f.updateErrorText(u.LastInstallErrorCode)
	}
	if u.ActionError != "" && u.ActionError != u.ErrorCode {
		return f.updateErrorText(u.ActionError)
	}
	return ""
}

func (f settingsFrame) updateErrorText(code string) string {
	switch code {
	case "network":
		return f.tr("无法连接更新服务器，请检查网络连接后重试。", "Could not reach the update server. Check your connection and try again.")
	case "release":
		return f.tr("发布清单信息无效或不匹配，请稍后重试。", "Release metadata is invalid or mismatched. Please try again later.")
	case "signature":
		return f.tr("更新签名校验失败，已拒绝本次更新。", "Update signature verification failed. The update was rejected.")
	case "integrity":
		return f.tr("更新包完整性校验失败，请重试下载。", "Update package integrity verification failed. Please retry.")
	case "download":
		return f.tr("更新包下载中断，请检查网络后重试。", "Update download was interrupted. Please try again.")
	case "cache":
		return f.tr("无法读写本地更新缓存，请检查数据目录权限与磁盘空间。", "Could not access the local update cache. Check permissions and disk space.")
	case "settings":
		return f.tr("无法读写自动更新偏好设置，请检查本地数据库。", "Could not read or save the auto-update setting.")
	case "install_failed":
		return f.tr("上次更新安装失败，已保留原版本；可重试或前往发布页下载。", "Previous update failed to install. Your version was kept; retry or download from the release page.")
	case "install_busy":
		return f.tr("上次更新因文件被占用或其他实例运行而中止，关闭其他实例后重试。", "Previous update stopped because files were in use or another instance was running.")
	case "restart_failed":
		return f.tr("上次更新后未能自动重启 QuailCrate，请确认当前版本。", "QuailCrate could not restart automatically after the previous update.")
	case "unavailable":
		return f.tr("当前构建未启用在线更新或暂无可安装的更新包。", "Online updates are unavailable in this build or no installable package is ready.")
	default:
		if code == "" {
			return f.tr("更新过程发生未知错误，请稍后重试。", "An unknown update error occurred. Please try again.")
		}
		return fmt.Sprintf(f.tr("更新出现异常（%s），请稍后重试。", "Update error (%s). Please try again."), code)
	}
}

func (f settingsFrame) updateProgressText(u SettingsUpdate) string {
	if u.TotalBytes > 0 {
		pct := min(100, max(0, int(u.DownloadedBytes*100/u.TotalBytes)))
		return fmt.Sprintf("%d%%  ·  %s / %s", pct, formatByteSize(u.DownloadedBytes), formatByteSize(u.TotalBytes))
	}
	if u.DownloadedBytes > 0 {
		return formatByteSize(u.DownloadedBytes)
	}
	return f.tr("准备下载...", "Starting download...")
}

func formatByteSize(bytes int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.0f KB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%d B", max(0, bytes))
	}
}
