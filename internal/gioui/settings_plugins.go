package gioui

import (
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"

	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"quailcrate/internal/theme"
)

func (f settingsFrame) runningBadge(gtx layout.Context, r image.Rectangle) {
	n := 0
	for _, plugin := range f.model.Plugins {
		if plugin.State == "running" {
			n++
		}
	}
	badgeR := f.radius(7)
	settingsFill(gtx, r, badgeR, f.p.Selected)
	settingsStroke(gtx, r, badgeR, 1, theme.WithAlpha(f.p.Accent, 100))
	p := gtx.Dp
	mid := (r.Min.Y + r.Max.Y) / 2
	ink := f.p.Accent
	if f.model.HighContrast {
		ink = f.p.OnAccent
	}
	settingsFill(gtx, image.Rect(r.Min.X+p(9), mid-p(3), r.Min.X+p(15), mid+p(3)), 3, ink)
	f.monoLabel(gtx, image.Rect(r.Min.X+p(18), r.Min.Y, r.Max.X-p(3), r.Max.Y), fmt.Sprintf(f.tr("%d 运行中", "%d ONLINE"), n), 11, font.Medium, ink, layout.Center)
}

type settingsColumns struct {
	perms, runtime, status, toggle int
	compact                        bool
}

func tableColumns(gtx layout.Context) settingsColumns {
	p, w := gtx.Dp, gtx.Constraints.Max.X
	if w < p(440) {
		return settingsColumns{toggle: w - p(60), compact: true}
	}
	permsWidth, runtimeWidth, statusWidth := p(96), p(58), p(94)
	if w < p(660) {
		permsWidth, runtimeWidth, statusWidth = p(82), p(50), p(76)
	}
	toggle := w - p(64)
	status := toggle - statusWidth
	runtime := status - runtimeWidth
	perms := runtime - permsWidth
	return settingsColumns{perms: perms, runtime: runtime, status: status, toggle: toggle}
}

func (f settingsFrame) list(gtx layout.Context, list *widget.List, count int, element layout.ListElement) {
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NamePageUp}, key.Filter{Name: key.NamePageDown})
		if !ok {
			break
		}
		if k, ok := ev.(key.Event); ok && k.State == key.Press {
			step := float32(max(1, list.Position.Count-1))
			if k.Name == key.NamePageUp {
				step = -step
			}
			list.ScrollBy(step)
		}
	}
	style := material.List(f.theme, list)
	style.AnchorStrategy = material.Overlay
	style.Indicator.MinorWidth = 5
	style.Indicator.CornerRadius = 3
	style.Indicator.Color = theme.WithAlpha(f.p.Secondary, 115)
	style.Indicator.HoverColor = theme.WithAlpha(f.p.Secondary, 195)
	style.Track.MinorPadding = 3
	style.Track.MajorPadding = 6
	style.Layout(gtx, count, element)
}

func (f settingsFrame) plugins(gtx layout.Context) int {
	p, w, h := gtx.Dp, gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	install := f.model.PluginInstall
	if f.rescan.Clicked(gtx) {
		f.emit(SettingsAction{Kind: Rescan})
	}
	if f.directory.Clicked(gtx) {
		f.emit(SettingsAction{Kind: OpenPluginDir})
	}
	if f.installPlugin.Clicked(gtx) && !install.Busy {
		f.emit(SettingsAction{Kind: InstallPluginFromGitHub})
	}
	// Process permission drawer toggles before measuring row & table heights so the
	// expanded drawer never overlaps subsequent rows on the click frame.
	for _, plugin := range f.model.Plugins {
		if btns := f.pluginButtons[plugin.ID]; btns != nil && len(plugin.RequestedPermissions) > 0 && btns.permToggle.Clicked(gtx) {
			if f.expandedPluginPerms == plugin.ID {
				f.expandedPluginPerms = ""
			} else {
				f.expandedPluginPerms = plugin.ID
			}
		}
	}
	var banner op.CallOp
	var bannerHeight int
	if install.Message != "" {
		bannerText := material.Label(f.theme, 13, install.Message)
		bannerText.Font.Typeface = f.typeface()
		bannerText.MaxLines = max(1, min(4, h/3/max(1, gtx.Sp(18))))
		bannerText.Color = f.p.Text
		if install.IsError {
			bannerText.Color = f.p.Warning
		}
		ctx := gtx
		ctx.Constraints = layout.Constraints{Max: image.Pt(max(0, w-p(24)), h)}
		record := op.Record(gtx.Ops)
		bannerHeight = bannerText.Layout(ctx).Size.Y + 2*p(8)
		banner = record.Stop()
	}
	head, foot := p(35), p(50)
	if bannerHeight > 0 {
		foot += p(8) + bannerHeight
	}
	gutter := p(1)
	dataHeight := 0
	rowContext := gtx
	rowContext.Constraints.Max.X -= 2 * gutter
	for _, plugin := range f.model.Plugins {
		dataHeight += f.pluginHeight(rowContext, plugin)
	}
	if len(f.model.Plugins) == 0 {
		dataHeight = p(70)
	}
	tableHeight := max(0, min(h-foot, dataHeight+head+2*gutter))
	if tableHeight > head {
		r := image.Rect(0, 0, w, tableHeight)
		f.box(gtx, r, 12, f.p.Surface)
		rounded := clip.UniformRRect(r.Inset(gutter), p(f.radius(11))).Push(gtx.Ops)
		settingsArea(gtx, image.Rect(gutter, gutter, w-gutter, gutter+head), f.tableHeader)
		settingsArea(gtx, image.Rect(gutter, head+gutter, w-gutter, tableHeight-gutter), func(gtx layout.Context) {
			if len(f.model.Plugins) == 0 {
				f.label(gtx, settingsBounds(gtx), f.tr("尚未发现插件", "No plugins found"), 14, font.Normal, f.p.Secondary, layout.Center)
				return
			}
			f.list(gtx, &f.pluginsList, len(f.model.Plugins), func(gtx layout.Context, index int) layout.Dimensions {
				plugin := f.model.Plugins[index]
				size := image.Pt(gtx.Constraints.Max.X, f.pluginHeight(gtx, plugin))
				gtx.Constraints = layout.Exact(size)
				f.pluginRow(gtx, plugin, index)
				return layout.Dimensions{Size: size}
			})
		})
		rounded.Pop()
	}
	y := tableHeight + p(12)
	if bannerHeight > 0 {
		bg := f.p.Surface
		if install.IsError {
			bg = f.p.WarningBg
		}
		f.box(gtx, image.Rect(0, y, w, y+bannerHeight), 7, bg)
		settingsArea(gtx, image.Rect(p(12), y+p(8), w-p(12), y+bannerHeight-p(8)), func(gtx layout.Context) {
			banner.Add(gtx.Ops)
		})
		y += bannerHeight + p(8)
	}
	gap := p(8)
	rescanWidth, directoryWidth, installWidth := p(96), p(152), p(162)
	buttonWidth := rescanWidth + directoryWidth + installWidth
	available := min(buttonWidth, max(0, w-2*gap))
	if available < buttonWidth {
		rescanWidth = available * rescanWidth / buttonWidth
		directoryWidth = available * directoryWidth / buttonWidth
	}
	x := w - available - 2*gap
	f.textButton(gtx, image.Rect(x, y, x+rescanWidth, y+p(32)), &f.rescan, f.tr("重新扫描", "Rescan"), false)
	x += rescanWidth + gap
	f.textButton(gtx, image.Rect(x, y, x+directoryWidth, y+p(32)), &f.directory, f.tr("打开插件目录", "Open plugins folder"), false)
	x += directoryWidth + gap
	installLabel := f.tr("从 GitHub 安装…", "Install from GitHub…")
	if install.Busy && !install.Uninstalling {
		installLabel = f.tr("正在安装…", "Installing…")
	}
	f.textButtonWithDescription(gtx, image.Rect(x, y, w, y+p(32)), &f.installPlugin, installLabel, installLabel, true, install.Busy)
	return min(h, tableHeight+foot)
}

func (f settingsFrame) tableHeader(gtx layout.Context) {
	p, w, h := gtx.Dp, gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	cols := tableColumns(gtx)
	settingsFill(gtx, settingsBounds(gtx), 0, f.p.Sidebar)
	settingsFill(gtx, image.Rect(0, h-p(1), w, h), 0, f.p.Line)
	f.label(gtx, image.Rect(p(13), 0, cols.toggle, h), f.tr("插件", "Plugin"), 12, font.Medium, f.p.Secondary, layout.W)
	if !cols.compact {
		f.label(gtx, image.Rect(cols.perms, 0, cols.runtime, h), f.tr("权限", "Permissions"), 12, font.Medium, f.p.Secondary, layout.W)
		f.label(gtx, image.Rect(cols.runtime, 0, cols.status, h), f.tr("运行时", "Runtime"), 12, font.Medium, f.p.Secondary, layout.W)
		f.label(gtx, image.Rect(cols.status, 0, cols.toggle, h), f.tr("状态", "Status"), 12, font.Medium, f.p.Secondary, layout.W)
	}
	f.label(gtx, image.Rect(cols.toggle, 0, w-p(6), h), f.tr("启用", "Enabled"), 12, font.Medium, f.p.Secondary, layout.Center)
}

func (f settingsFrame) pluginPermissionInfo(perm string) (title, desc string) {
	switch perm {
	case "network.http":
		return f.tr("公开网络访问", "Public internet access"),
			f.tr("仅允许向公网域名发起 HTTP/HTTPS 请求，屏蔽本机与局域网", "HTTP/HTTPS to public internet domains only; blocks local & LAN")
	case "network.local":
		return f.tr("本机与局域网访问", "Local & LAN network access"),
			f.tr("允许访问 127.0.0.1、localhost 与局域网内网服务", "Allows connections to localhost and private LAN addresses")
	case "audio.play":
		return f.tr("音频播放", "Audio playback"),
			f.tr("通过宿主音频引擎播放、定位和控制音量", "Play, seek, and control audio output through the host engine")
	case "files.directory":
		return f.tr("授权目录访问", "Authorized folder access"),
			f.tr("仅读写你手动选择并授权的本地目录", "Access only local folders you explicitly select and authorize")
	case "apps.read":
		return f.tr("应用列表", "Read app list"),
			f.tr("读取本机已安装应用名称与图标用于搜索和分组", "Read installed application names and icons for search and groups")
	case "apps.launch":
		return f.tr("启动应用", "Launch applications"),
			f.tr("由用户操作触发启动已安装的应用程序", "Launch installed applications when triggered by the user")
	case "paths.open":
		return f.tr("打开文件与目录", "Open files & folders"),
			f.tr("调用系统默认程序打开文件或在资源管理器中显示", "Open files with the system default handler or reveal in Explorer")
	case "urls.open":
		return f.tr("打开网页链接", "Open web links"),
			f.tr("调用系统默认浏览器打开外部网页链接", "Open external URLs in the system default browser")
	case "wallpaper.set":
		return f.tr("设置桌面壁纸", "Set desktop wallpaper"),
			f.tr("将所选本地图片设为 Windows 桌面壁纸", "Apply a chosen local image as the Windows desktop wallpaper")
	default:
		if strings.HasPrefix(perm, "service:") {
			svc := strings.TrimPrefix(perm, "service:")
			return f.tr("插件服务调用", "Plugin service call"),
				f.tr("调用其他插件公开的服务能力：", "Invoke public plugin service: ") + svc
		}
		return perm, f.tr("插件声明的宿主扩展能力", "Host capability declared by this plugin")
	}
}

func (f settingsFrame) pluginMissingPermissions(plugin SettingsPlugin) int {
	missing := 0
	for _, perm := range plugin.RequestedPermissions {
		if !slices.Contains(plugin.GrantedPermissions, perm) {
			missing++
		}
	}
	return missing
}

func (f settingsFrame) pluginPermissionsExpanded(plugin SettingsPlugin) bool {
	return len(plugin.RequestedPermissions) > 0 && f.expandedPluginPerms == plugin.ID
}

func (f settingsFrame) pluginDrawerHeight(gtx layout.Context, plugin SettingsPlugin) int {
	if !f.pluginPermissionsExpanded(plugin) {
		return 0
	}
	p := gtx.Dp
	// Header (30dp) + N * 44dp permission rows + bottom margin (12dp)
	return p(30) + len(plugin.RequestedPermissions)*p(44) + p(12)
}

func (p SettingsPlugin) hasMetadataRow() bool {
	return p.SourceRepo != "" || p.HomepageURL != "" || p.CanUninstall
}

func (f settingsFrame) pluginHeaderHeight(gtx layout.Context, plugin SettingsPlugin) int {
	p := gtx.Dp
	height := p(61)
	if tableColumns(gtx).compact {
		height = p(77)
	}
	if plugin.FullSystemAccess {
		height += p(24)
	}
	if plugin.hasMetadataRow() {
		height += p(36)
	}
	return height
}

func (f settingsFrame) pluginHeight(gtx layout.Context, plugin SettingsPlugin) int {
	height := f.pluginHeaderHeight(gtx, plugin) + f.pluginDrawerHeight(gtx, plugin)
	if plugin.Detail != "" {
		height += gtx.Dp(56)
	}
	return height
}

func (f settingsFrame) pluginRow(gtx layout.Context, plugin SettingsPlugin, index int) {
	p, w, h := gtx.Dp, gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	cols := tableColumns(gtx)
	buttons := f.pluginButtons[plugin.ID]
	if buttons == nil {
		buttons = &settingsPluginButtons{revoke: make(map[string]*widget.Clickable)}
		f.pluginButtons[plugin.ID] = buttons
	}
	if buttons.revoke == nil {
		buttons.revoke = make(map[string]*widget.Clickable)
	}
	if buttons.toggle.Clicked(gtx) && plugin.CanToggle {
		f.emit(SettingsAction{Kind: TogglePlugin, PluginID: plugin.ID, Enabled: !plugin.Enabled})
	}
	if buttons.copy.Clicked(gtx) {
		f.emit(SettingsAction{Kind: CopyDiagnostics, PluginID: plugin.ID, Value: plugin.ID + "\r\n" + plugin.Status + "\r\n" + plugin.Detail})
	}
	if index != 0 {
		settingsFill(gtx, image.Rect(0, 0, w, p(1)), 0, f.p.Line)
	}
	bg, fg := f.iconColors(plugin.Icon)
	settingsFill(gtx, image.Rect(p(13), p(14), p(46), p(47)), f.radius(8), bg)
	settingsIcon(gtx, image.Rect(p(20), p(20), p(40), p(40)), plugin.Icon, fg)
	right := cols.perms - p(10)
	if cols.compact {
		right = cols.toggle - p(4)
	}
	f.pluginTitle(gtx, image.Rect(p(57), p(7), right, p(33)), plugin)
	f.label(gtx, image.Rect(p(57), p(31), right, p(53)), plugin.Description, 12.5, font.Normal, f.p.Secondary, layout.W)
	statusInk := f.p.Secondary
	switch plugin.State {
	case "running":
		statusInk = f.p.Accent
	case "failed":
		statusInk = f.p.Warning
	}
	runtime := strings.ToUpper(plugin.Runtime)
	if strings.EqualFold(runtime, "lua") {
		runtime = "Lua"
	}

	expanded := f.pluginPermissionsExpanded(plugin)
	missingPerms := f.pluginMissingPermissions(plugin)
	permCount := len(plugin.RequestedPermissions)
	permPillText := ""
	if permCount > 0 {
		arrow := " ▾"
		if expanded {
			arrow = " ▴"
		}
		if missingPerms > 0 {
			permPillText = f.tr("待授权", "Pending") + arrow
		} else if plugin.FullSystemAccess {
			permPillText = f.tr("完全权限", "Full") + arrow
		} else {
			permPillText = fmt.Sprintf(f.tr("%d 项权限", "%d perms"), permCount) + arrow
		}
	}

	if cols.compact {
		metaRight := right
		if permCount > 0 {
			pillW := p(74)
			metaRight = max(p(140), right-pillW-p(6))
			pillRect := image.Rect(metaRight+p(4), p(51), right, p(73))
			f.drawPermTrigger(gtx, pillRect, &buttons.permToggle, plugin.Name, permPillText, expanded, missingPerms > 0 || plugin.FullSystemAccess)
		}
		f.label(gtx, image.Rect(p(57), p(52), metaRight, p(72)), runtime+"  ·  "+plugin.Status, 11.5, font.Medium, statusInk, layout.W)
	} else {
		if permCount > 0 {
			pillRect := image.Rect(cols.perms, p(18), cols.runtime-p(10), p(43))
			f.drawPermTrigger(gtx, pillRect, &buttons.permToggle, plugin.Name, permPillText, expanded, missingPerms > 0 || plugin.FullSystemAccess)
		} else if plugin.FullSystemAccess {
			warnRect := image.Rect(cols.perms, p(18), cols.runtime-p(10), p(43))
			settingsFill(gtx, warnRect, f.radius(6), f.p.WarningBg)
			settingsStroke(gtx, warnRect, f.radius(6), 1, theme.WithAlpha(f.p.Warning, 110))
			f.label(gtx, warnRect, f.tr("完全权限", "Full access"), 11, font.Medium, f.p.Warning, layout.Center)
		} else {
			f.label(gtx, image.Rect(cols.perms, 0, cols.runtime-p(6), p(61)), f.tr("无需权限", "None"), 11.5, font.Normal, theme.WithAlpha(f.p.Secondary, 145), layout.W)
		}
		badge := image.Rect(cols.runtime, p(18), cols.runtime+p(42), p(42))
		if plugin.FullSystemAccess {
			settingsFill(gtx, badge, f.radius(5), f.p.WarningBg)
			settingsStroke(gtx, badge, f.radius(5), 1, theme.WithAlpha(f.p.Warning, 110))
			f.monoLabel(gtx, badge, strings.ToUpper(runtime), 10.5, font.Medium, f.p.Warning, layout.Center)
		} else {
			f.box(gtx, badge, 5, f.p.Background)
			f.monoLabel(gtx, badge, strings.ToUpper(runtime), 10.5, font.Medium, f.p.Secondary, layout.Center)
		}
		settingsFill(gtx, image.Rect(cols.status, p(27), cols.status+p(7), p(34)), 4, statusInk)
		f.label(gtx, image.Rect(cols.status+p(13), 0, cols.toggle-p(3), p(61)), plugin.Status, 12.5, font.Normal, statusInk, layout.W)
	}

	if plugin.FullSystemAccess {
		badgeTop := p(56)
		if cols.compact {
			badgeTop = p(74)
		}
		warnText := f.tr("⚠ 原生插件 · 完全系统权限", "⚠ Native DLL · Full System Access")
		badgeWidth := min(w-p(70), p(230))
		warnRect := image.Rect(p(57), badgeTop, p(57)+badgeWidth, badgeTop+p(21))
		settingsArea(gtx, warnRect, func(gtx layout.Context) {
			semantic.DescriptionOp(plugin.Name + " · " + warnText).Add(gtx.Ops)
			box := settingsBounds(gtx)
			settingsFill(gtx, box, f.radius(5), f.p.WarningBg)
			settingsStroke(gtx, box, f.radius(5), 1, theme.WithAlpha(f.p.Warning, 110))
			f.label(gtx, image.Rect(p(8), 0, box.Max.X-p(8), box.Max.Y), warnText, 11, font.Medium, f.p.Warning, layout.W)
		})
	}

	toggleCtx := gtx
	if !plugin.CanToggle {
		toggleCtx = gtx.Disabled()
	}
	f.button(toggleCtx, image.Rect(cols.toggle, 0, w-p(6), p(61)), &buttons.toggle, plugin.Name+" · "+f.tr("启用", "Enabled"), func(gtx layout.Context) {
		semantic.Switch.Add(gtx.Ops)
		semantic.SelectedOp(plugin.Enabled).Add(gtx.Ops)
		mid := gtx.Constraints.Max.Div(2)
		track := image.Rect(mid.X-p(18), mid.Y-p(10), mid.X+p(18), mid.Y+p(10))
		bg, knob := f.p.Accent, f.p.OnAccent
		if !plugin.CanToggle {
			bg = f.p.Line
		}
		at := track.Max.X - p(10)
		if !plugin.Enabled {
			settingsFill(gtx, track, 10, f.p.Secondary)
			settingsFill(gtx, track.Inset(max(1, p(1))), 9, f.p.Surface)
			knob, at = f.p.Secondary, track.Min.X+p(10)
		} else {
			settingsFill(gtx, track, 10, bg)
		}
		settingsFill(gtx, image.Rect(at-p(6), mid.Y-p(6), at+p(6), mid.Y+p(6)), 6, knob)
	})

	drawerTop := f.pluginHeaderHeight(gtx, plugin)
	if plugin.hasMetadataRow() {
		f.pluginMetadataRow(gtx, plugin, buttons, drawerTop-p(36))
	}
	if expanded {
		drawerH := f.pluginDrawerHeight(gtx, plugin) - p(12)
		drawerLeft := p(57)
		if cols.compact {
			drawerLeft = p(13)
		}
		drawer := image.Rect(drawerLeft, drawerTop, w-p(16), drawerTop+drawerH)
		drawerR := f.radius(8)
		settingsFill(gtx, drawer, drawerR, f.p.Background)
		settingsStroke(gtx, drawer, drawerR, 1, f.p.Line)

		// Subtle drawer header strip
		hdrH := p(30)
		hdrText := f.tr("已声明权限 · 撤销权限后将自动停用此插件", "Declared permissions · Revoking a permission disables this plugin")
		if plugin.FullSystemAccess {
			hdrText = f.tr("原生插件（完全系统权限）· 以下为额外声明的宿主能力", "Native DLL (Full System Access) · Declared host shell capabilities")
		}
		f.label(gtx, image.Rect(drawer.Min.X+p(12), drawer.Min.Y, drawer.Max.X-p(12), drawer.Min.Y+hdrH),
			hdrText,
			11, font.Medium, f.p.Secondary, layout.W)

		rowH := p(44)
		for i, perm := range plugin.RequestedPermissions {
			ry := drawer.Min.Y + hdrH + i*rowH
			settingsFill(gtx, image.Rect(drawer.Min.X+p(10), ry, drawer.Max.X-p(10), ry+p(1)), 0, f.p.Line)
			granted := slices.Contains(plugin.GrantedPermissions, perm)
			title, desc := f.pluginPermissionInfo(perm)

			dotInk := f.p.Warning
			if granted {
				dotInk = f.p.Accent
			}
			settingsFill(gtx, image.Rect(drawer.Min.X+p(12), ry+p(13), drawer.Min.X+p(18), ry+p(19)), 3, dotInk)

			actionW := p(68)
			textRight := drawer.Max.X - actionW - p(14)
			f.label(gtx, image.Rect(drawer.Min.X+p(26), ry+p(5), textRight, ry+p(24)),
				title+"  ·  "+perm, 12, font.SemiBold, f.p.Text, layout.W)
			f.label(gtx, image.Rect(drawer.Min.X+p(26), ry+p(23), textRight, ry+p(40)),
				desc, 11, font.Normal, f.p.Secondary, layout.W)

			actRect := image.Rect(drawer.Max.X-actionW-p(10), ry+p(9), drawer.Max.X-p(10), ry+rowH-p(9))
			if granted {
				clk := buttons.revoke[perm]
				if clk == nil {
					clk = new(widget.Clickable)
					buttons.revoke[perm] = clk
				}
				if clk.Clicked(gtx) {
					f.emit(SettingsAction{Kind: RevokePermission, PluginID: plugin.ID, Value: perm})
				}
				permID := perm
				f.button(gtx, actRect, clk, plugin.Name+" · "+f.tr("撤销权限 ", "Revoke ")+title, func(gtx layout.Context) {
					box := settingsBounds(gtx)
					r := f.radius(6)
					if clk.Hovered() {
						settingsFill(gtx, box, r, f.p.WarningBg)
						settingsStroke(gtx, box, r, 1, theme.WithAlpha(f.p.Warning, 140))
						f.label(gtx, box, f.tr("撤销", "Revoke"), 11.5, font.SemiBold, f.p.Warning, layout.Center)
					} else {
						settingsFill(gtx, box, r, f.p.Surface)
						settingsStroke(gtx, box, r, 1, f.p.Line)
						f.label(gtx, box, f.tr("撤销", "Revoke"), 11.5, font.Medium, f.p.Secondary, layout.Center)
					}
				})
				_ = permID
			} else {
				f.label(gtx, actRect, f.tr("未授权", "Not granted"), 11, font.Medium, f.p.Warning, layout.Center)
			}
		}
	}

	if plugin.Detail != "" {
		start := h - p(56)
		detail := image.Rect(p(13), start, w-p(13), h-p(10))
		settingsFill(gtx, detail, f.radius(7), f.p.WarningBg)
		copyWidth := p(128)
		f.paragraph(gtx, image.Rect(detail.Min.X+p(9), detail.Min.Y, detail.Max.X-copyWidth, detail.Max.Y), plugin.Detail, f.p.Warning, 2)
		f.button(gtx, image.Rect(detail.Max.X-copyWidth, detail.Min.Y, detail.Max.X-p(3), detail.Max.Y), &buttons.copy, f.tr("复制诊断信息", "Copy diagnostics"), func(gtx layout.Context) {
			f.label(gtx, settingsBounds(gtx), f.tr("复制诊断信息", "Copy diagnostics"), 12, font.SemiBold, f.p.Warning, layout.Center)
		})
	}
}

func (f settingsFrame) pluginTitle(gtx layout.Context, r image.Rectangle, plugin SettingsPlugin) {
	if plugin.Version == "" {
		f.label(gtx, r, plugin.Name, 14, font.SemiBold, f.p.Text, layout.W)
		return
	}
	settingsArea(gtx, r, func(gtx layout.Context) {
		versionWidth := gtx.Constraints.Max.X / 2
		layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			row := layout.Flex{Alignment: layout.Baseline, Gap: gtx.Dp(8)}
			return row.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = 0
					name := material.Label(f.theme, 14, plugin.Name)
					name.Color, name.Font.Typeface, name.Font.Weight, name.MaxLines = f.p.Text, f.typeface(), font.SemiBold, 1
					return name.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, versionWidth)
					version := material.Label(f.theme, 12, "v"+plugin.Version)
					version.Color, version.Font.Typeface, version.MaxLines = f.p.Secondary, f.typeface(), 1
					return version.Layout(gtx)
				}),
			)
		})
	})
}

func (f settingsFrame) pluginMetadataRow(gtx layout.Context, plugin SettingsPlugin, buttons *settingsPluginButtons, y int) {
	p := gtx.Dp
	right := gtx.Constraints.Max.X - p(16)
	if plugin.CanUninstall {
		busy := f.model.PluginInstall.Busy
		if buttons.uninstall.Clicked(gtx) && !busy {
			f.emit(SettingsAction{Kind: UninstallPlugin, PluginID: plugin.ID})
		}
		title := f.tr("卸载", "Uninstall")
		f.textButtonWithDescription(gtx, image.Rect(right-p(96), y, right, y+p(28)), &buttons.uninstall,
			title, plugin.Name+" · "+title, false, busy)
		right -= p(104)
	}
	if plugin.HomepageURL != "" {
		if buttons.homepage.Clicked(gtx) {
			f.emit(SettingsAction{Kind: OpenPluginHomepage, PluginID: plugin.ID})
		}
		f.textButtonWithDescription(gtx, image.Rect(right-p(105), y, right, y+p(28)), &buttons.homepage,
			f.tr("主页", "Homepage"), plugin.Name+" · "+f.tr("打开主页", "Open homepage"), false, false)
		right -= p(113)
	}
	if plugin.SourceRepo != "" && right > p(57) {
		f.label(gtx, image.Rect(p(57), y, right, y+p(28)), plugin.SourceRepo, 12, font.Medium, f.p.Secondary, layout.W)
	}
}

func (f settingsFrame) drawPermTrigger(gtx layout.Context, r image.Rectangle, clk *widget.Clickable, pluginName, label string, expanded, hasMissing bool) {
	f.button(gtx, r, clk, pluginName+" · "+f.tr("管理权限", "Manage permissions"), func(gtx layout.Context) {
		box := settingsBounds(gtx)
		rad := f.radius(6)
		fill := f.p.Background
		stroke := f.p.Line
		ink := f.p.Secondary
		if hasMissing {
			fill = f.p.WarningBg
			stroke = theme.WithAlpha(f.p.Warning, 110)
			ink = f.p.Warning
		} else if expanded {
			fill = f.p.Selected
			stroke = theme.WithAlpha(f.p.Accent, 115)
			ink = f.p.Accent
		} else if clk.Hovered() {
			fill = f.p.Hover
			ink = f.p.Text
		}
		if f.model.HighContrast {
			ink = f.p.Text
			stroke = f.p.Line
		}
		settingsFill(gtx, box, rad, fill)
		settingsStroke(gtx, box, rad, 1, stroke)
		f.label(gtx, box, label, 11, font.Medium, ink, layout.Center)
	})
}

func (f settingsFrame) iconColors(icon string) (color.NRGBA, color.NRGBA) {
	if f.model.HighContrast {
		return f.p.Surface, f.p.Text
	}
	if f.isDark() {
		var tint color.NRGBA
		switch icon {
		case "search":
			tint = settingsColor(0x38bdf8)
		case "folder":
			tint = settingsColor(0xf59e0b)
		case "image":
			tint = settingsColor(0xc084fc)
		case "calendar":
			tint = f.p.Accent
		case "music":
			tint = settingsColor(0xf472b6)
		default:
			tint = f.p.Secondary
		}
		return theme.Blend(f.p.Surface, tint, 0.16), tint
	}
	switch icon {
	case "search":
		return settingsColor(0xe5f1ff), settingsColor(0x165e9c)
	case "folder":
		return settingsColor(0xf9ebd3), settingsColor(0x895122)
	case "image":
		return settingsColor(0xede8fc), settingsColor(0x645095)
	case "calendar":
		return settingsColor(0xe1f1ea), settingsColor(0x14665b)
	case "music":
		return settingsColor(0xfce7f3), settingsColor(0x9d174d)
	default:
		return settingsColor(0xe6edef), settingsColor(0x355c6b)
	}
}
