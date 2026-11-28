package gioui

import (
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"quailcrate/internal/pluginapps"
	"quailcrate/internal/theme"
	v2 "quailcrate/pluginapi/v2"
)

func TestDeclaredSettingsNavigatesCardsBySection(t *testing.T) {
	view := NewSettingsView()
	var router input.Router
	gtx := layout.Context{Ops: new(op.Ops), Source: router.Source(), Constraints: layout.Exact(image.Pt(1040, 680)), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	def := &v2.Settings{UIVersion: 2, Title: "Workbench", Sections: []v2.SettingsSection{
		{ID: "quick", Title: "Quick changes", Cards: []v2.SettingsCard{{Type: "desktopLayout", Title: "Arrange"}, {Type: "language", Title: "Language"}}},
		{ID: "keybinds", Title: "Key bindings", Cards: []v2.SettingsCard{{Type: "hotkey", Title: "Keys"}}},
	}}
	if err := def.Validate(); err != nil {
		t.Fatal(err)
	}
	var actions []SettingsAction
	frame := func() {
		gtx.Reset()
		view.Layout(gtx, SettingsModel{Definition: def, Locale: "en-US"}, func(a SettingsAction) { actions = append(actions, a) })
		router.Frame(gtx.Ops)
	}
	frame()
	if view.CurrentPage() != "quick" || len(view.extraNav) != 2 {
		t.Fatal("declared navigation unavailable")
	}
	view.edit.Click()
	frame()
	view.extraNav["keybinds"].Click()
	frame()
	view.shortcut.SetText("Ctrl+Shift+K")
	view.apply.Click()
	frame()
	if view.CurrentPage() != "keybinds" || !slices.Equal(actions, []SettingsAction{{Kind: ToggleEditing, Enabled: true}, {Kind: ChangeHotkey, Value: "Ctrl+Shift+K"}}) {
		t.Fatalf("unexpected declared settings actions: %+v", actions)
	}
}

// At 125% DPI the old settings window showed a scrollbar with five short
// rows and rebuilt their controls every time the user switched pages.
func TestSettingsNavigationKeepsControlsAndRoutesActions(t *testing.T) {
	view := NewSettingsView()
	var router input.Router
	gtx := layout.Context{
		Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1.25, PxPerSp: 1.25},
		Constraints: layout.Exact(image.Pt(1290, 625)),
	}
	m := SettingsModel{
		Locale: "zh-CN", LanguagePreference: "system", Hotkey: "Alt+Space",
		Appearance: theme.DefaultConfig(),
		Commands:   []SettingsCommand{{PluginID: "demo", ID: "create", PluginName: "演示插件", Title: "新建", Generation: 7}},
	}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		m.Plugins = append(m.Plugins, SettingsPlugin{ID: id, Name: id, CanToggle: id != "d"})
	}
	var actions []SettingsAction
	frame := func() {
		gtx.Reset()
		gtx.Now = time.Now()
		view.Layout(gtx, m, func(a SettingsAction) { actions = append(actions, a) })
		router.Frame(gtx.Ops)
	}
	frame()
	view.shortcut.SetText("Ctrl+Alt+Q")
	view.nav[pluginsPage].Click()
	frame()
	if view.CurrentPage() != PagePlugins || view.pluginsList.Position.Count != 5 || view.pluginsList.Position.OffsetLast < 0 {
		t.Fatalf("five rows should fit together: %+v", view.pluginsList.Position)
	}
	toggle := &view.pluginButtons["b"].toggle
	gtx.Execute(key.FocusCmd{Tag: toggle})
	frame()
	router.Queue(key.Event{Name: key.NameSpace, State: key.Press}, key.Event{Name: key.NameSpace, State: key.Release})
	view.pluginButtons["d"].toggle.Click()
	frame()
	view.nav[desktopPage].Click()
	frame()
	view.edit.Click()
	frame()
	view.nav[commandsPage].Click()
	frame()
	if view.CurrentPage() != PageCommands {
		t.Fatalf("expected commands page, got %q", view.CurrentPage())
	}
	view.commandButtons[[2]string{"demo", "create"}].Click()
	frame()
	view.nav[generalPage].Click()
	frame()
	view.apply.Click()
	view.languages[1].Click()
	frame()
	want := []SettingsAction{
		{Kind: TogglePlugin, PluginID: "b", Enabled: true},
		{Kind: ToggleEditing, Enabled: true},
		{Kind: RunCommand, PluginID: "demo", CommandID: "create", Generation: 7},
		{Kind: ChangeHotkey, Value: "Ctrl+Alt+Q"},
		{Kind: ChangeLanguage, Value: "en-US"},
	}
	if !slices.Equal(actions, want) {
		t.Fatalf("routed actions: got %+v, want %+v", actions, want)
	}
	view.ShowPlugins()
	frame()
	if &view.pluginButtons["b"].toggle != toggle {
		t.Fatal("lost plugin input state on navigation")
	}
}

func TestSettingsAppsMenuDisambiguationAndCommandsEmptyState(t *testing.T) {
	tabs := []pluginapps.Tab{
		{TabRef: pluginapps.TabRef{PluginID: "p1", TabID: "main", Generation: 2}, PluginName: "Alpha", Title: "概览"},
		{TabRef: pluginapps.TabRef{PluginID: "p2", TabID: "main", Generation: 3}, PluginName: "Beta", Title: "概览"},
		{TabRef: pluginapps.TabRef{PluginID: "p1", TabID: "counter", Generation: 2}, PluginName: "Alpha", Title: "计数器"},
	}
	labels := DisambiguateTabLabels(tabs)
	wantLabels := []string{"概览 · Alpha", "概览 · Beta", "计数器"}
	if !slices.Equal(labels, wantLabels) {
		t.Fatalf("DisambiguateTabLabels = %+v, want %+v", labels, wantLabels)
	}

	view := NewSettingsView()
	var router input.Router
	gtx := layout.Context{
		Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(960, 640)),
	}
	m := SettingsModel{Locale: "zh-CN", Appearance: theme.DefaultConfig(), AppTabs: tabs}
	var actions []SettingsAction
	frame := func() {
		gtx.Reset()
		gtx.Now = time.Now()
		view.Layout(gtx, m, func(a SettingsAction) { actions = append(actions, a) })
		router.Frame(gtx.Ops)
	}
	view.SetPage(PageCommands)
	frame()
	// Empty commands state navigates to Plugins when clicking "前往插件管理".
	view.goPluginsButton.Click()
	frame()
	if view.CurrentPage() != PagePlugins {
		t.Fatalf("goPluginsButton should navigate to plugins, got %q", view.CurrentPage())
	}

	// Clicking Apps in wide sidebar only toggles collapse/expand without changing page.
	view.appsToggle.Click()
	frame()
	if view.CurrentPage() != PagePlugins || !view.AppsExpanded() {
		t.Fatalf("apps toggle should expand without leaving plugins page: page=%q expanded=%v", view.CurrentPage(), view.AppsExpanded())
	}
	view.appTabButtons[[2]string{"p1", "counter"}].Click()
	frame()

	// Narrow sidebar (<660dp) emits OpenAppsMenu popup trigger.
	gtx.Constraints = layout.Exact(image.Pt(560, 640))
	frame()
	view.appsToggle.Click()
	frame()

	want := []SettingsAction{
		{Kind: ToggleAppsExpanded, Enabled: true},
		{Kind: OpenAppTab, PluginID: "p1", CommandID: "counter", Generation: 2},
		{Kind: OpenAppsMenu},
	}
	if !slices.Equal(actions, want) {
		t.Fatalf("apps menu actions = %+v, want %+v", actions, want)
	}
	menuVisible := func() bool {
		return slices.ContainsFunc(router.AppendSemantics(nil), func(node input.SemanticNode) bool {
			return node.Desc.Description == "应用"
		})
	}
	if !menuVisible() {
		t.Fatal("missing menu for registered plugin apps")
	}
	m.AppTabs = nil
	for _, width := range []int{560, 960} {
		gtx.Constraints = layout.Exact(image.Pt(width, 640))
		frame()
		if menuVisible() {
			t.Fatalf("empty Apps menu is visible at width %d", width)
		}
	}
}

func TestAppearanceTargetClickUpdatesPreviewInSameFrame(t *testing.T) {
	const playerID = "example.player"
	cfg := theme.DefaultConfig()
	cfg.GlobalColors.Accent = "#A1B2C3"
	cfg.Components[playerID] = theme.ComponentOverride{Custom: true, Colors: theme.PartialPalette{Accent: "#123ABC"}}
	for _, tt := range []struct {
		name string
		def  *v2.Settings
	}{
		{name: "built-in"},
		{name: "workbench", def: &v2.Settings{Sections: []v2.SettingsSection{
			{ID: PageAppearance, Title: "Appearance", Cards: []v2.SettingsCard{{Type: "target", Title: "Component"}}},
		}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			view := NewSettingsView()
			view.ShowAppearance()
			var router input.Router
			gtx := layout.Context{
				Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: layout.Exact(image.Pt(1040, 680)),
			}
			m := SettingsModel{Locale: "en-US", Definition: tt.def, Appearance: cfg, AppearanceTargets: []SettingsAppearanceTarget{
				{ID: theme.ComponentGlobal, LabelEN: "Global", SupportsOpacity: true},
				{ID: playerID, LabelEN: "Player", SupportsOpacity: true},
			}}
			frame := func() {
				gtx.Reset()
				gtx.Now = time.Now()
				view.Layout(gtx, m, func(a SettingsAction) { t.Fatalf("unexpected action on target selection: %+v", a) })
				router.Frame(gtx.Ops)
			}
			frame()
			for _, id := range []string{playerID, theme.ComponentGlobal} {
				view.targetButtons[id].Click()
				frame()
				// The preview strip displays the resolved accent for the current target.
				want := theme.FormatHex(cfg.Resolve(id).Palette.Accent)
				if !slices.ContainsFunc(router.AppendSemantics(nil), func(node input.SemanticNode) bool {
					return node.Desc.Label == want
				}) {
					t.Fatalf("first frame after selecting %s is missing preview accent %s", id, want)
				}
			}
		})
	}
}

func TestSettingsAppearancePerComponentCustomization(t *testing.T) {
	view := NewSettingsView()
	var router input.Router
	gtx := layout.Context{
		Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1040, 860)),
	}
	m := SettingsModel{Locale: "zh-CN", Appearance: theme.DefaultConfig()}
	var latest SettingsAction
	frame := func() {
		gtx.Reset()
		gtx.Now = time.Now()
		view.Layout(gtx, m, func(a SettingsAction) {
			latest = a
			if a.Kind == SaveAppearance || a.Kind == PreviewAppearance {
				m.Appearance = theme.ParseConfig(a.Value)
			}
		})
		router.Frame(gtx.Ops)
	}
	view.ShowAppearance()
	frame()
	if view.CurrentPage() != "appearance" {
		t.Fatalf("page = %q, want appearance", view.CurrentPage())
	}
	// Select Calendar component tab.
	view.targetButtons[theme.ComponentCalendar].Click()
	frame()
	// Open the theme dropdown, select preset 2 ("graphite-mono"), then pick 0% floating opacity chip.
	view.presetDropdown.Click()
	frame()
	if !view.presetMenuOpen {
		t.Fatal("expected theme dropdown to open after clicking trigger")
	}
	view.presetButtons[2].Click()
	frame()
	if view.presetMenuOpen {
		t.Fatal("expected theme dropdown to close after selecting a preset")
	}
	view.opacityChips[0].Click()
	frame()
	if latest.Kind != SaveAppearance {
		t.Fatalf("expected SaveAppearance action, got %+v", latest)
	}
	cal := m.Appearance.Resolve(theme.ComponentCalendar)
	if !cal.Custom || cal.Opacity != 0 || theme.FormatHex(cal.Palette.Accent) != "#A8BACB" {
		t.Fatalf("calendar override = %+v", cal)
	}
	// Quick next-theme button steps Calendar from preset 2 ("graphite-mono") to preset 3 ("midnight-slate").
	view.presetNext.Click()
	frame()
	cal = m.Appearance.Resolve(theme.ComponentCalendar)
	if theme.FormatHex(cal.Palette.Accent) != "#7BB5E8" {
		t.Fatalf("after presetNext calendar accent = %s, want #7BB5E8", theme.FormatHex(cal.Palette.Accent))
	}
	// Global and Desktop Groups remain untouched on default Catppuccin Mocha & 78% opacity.
	groups := m.Appearance.Resolve(theme.ComponentGroups)
	if groups.Custom || groups.Opacity != theme.DefaultWidgetOpacity || theme.FormatHex(groups.Palette.Accent) != "#CBA6F7" {
		t.Fatalf("groups should remain on global defaults, got %+v", groups)
	}

	// Verify frameless custom titlebar window control routing.
	m.CustomTitleBar = true
	frame()
	view.winMin.Click()
	frame()
	if latest.Kind != WindowMinimize {
		t.Fatalf("expected WindowMinimize, got %+v", latest)
	}
	view.winMax.Click()
	frame()
	if latest.Kind != WindowToggleMaximize {
		t.Fatalf("expected WindowToggleMaximize, got %+v", latest)
	}
	view.winClose.Click()
	frame()
	if latest.Kind != WindowClose {
		t.Fatalf("expected WindowClose, got %+v", latest)
	}
}

func TestSettingsAppearanceGlobalCornerRadius(t *testing.T) {
	view := NewSettingsView()
	var router input.Router
	gtx := layout.Context{
		Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1040, 960)),
	}
	m := SettingsModel{Locale: "zh-CN", Appearance: theme.DefaultConfig()}
	var latest SettingsAction
	frame := func() {
		gtx.Reset()
		gtx.Now = time.Now()
		view.Layout(gtx, m, func(a SettingsAction) {
			latest = a
			if a.Kind == SaveAppearance || a.Kind == PreviewAppearance {
				m.Appearance = theme.ParseConfig(a.Value)
			}
		})
		router.Frame(gtx.Ops)
	}
	view.ShowAppearance()
	frame()
	if got := m.Appearance.EffectiveCornerRadius(); got != theme.DefaultCornerRadius {
		t.Fatalf("initial corner radius = %d, want %d", got, theme.DefaultCornerRadius)
	}
	// Click "0px 直角" chip.
	view.radiusChips[0].Click()
	frame()
	if latest.Kind != SaveAppearance || m.Appearance.EffectiveCornerRadius() != 0 {
		t.Fatalf("after 0px chip: action=%+v radius=%d", latest, m.Appearance.EffectiveCornerRadius())
	}
	// Click "18px 柔和" chip.
	view.radiusChips[4].Click()
	frame()
	if latest.Kind != SaveAppearance || m.Appearance.EffectiveCornerRadius() != 18 {
		t.Fatalf("after 18px chip: action=%+v radius=%d", latest, m.Appearance.EffectiveCornerRadius())
	}
	// Title accent bar defaults to off and toggles cleanly.
	if m.Appearance.TitleAccentBar {
		t.Fatal("title accent bar should default to off")
	}
	view.titleBarToggle.Click()
	frame()
	if latest.Kind != SaveAppearance || !m.Appearance.TitleAccentBar {
		t.Fatalf("after enabling title bar toggle: action=%+v bar=%v", latest, m.Appearance.TitleAccentBar)
	}
	view.titleBarToggle.Click()
	frame()
	if latest.Kind != SaveAppearance || m.Appearance.TitleAccentBar {
		t.Fatalf("after disabling title bar toggle: action=%+v bar=%v", latest, m.Appearance.TitleAccentBar)
	}
}

func TestSettingsAppearanceIconSize(t *testing.T) {
	view := NewSettingsView()
	var router input.Router
	gtx := layout.Context{
		Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1040, 1140)),
	}
	m := SettingsModel{Locale: "zh-CN", Appearance: theme.DefaultConfig()}
	var latest SettingsAction
	frame := func() {
		gtx.Reset()
		gtx.Now = time.Now()
		view.Layout(gtx, m, func(a SettingsAction) {
			latest = a
			if a.Kind == SaveAppearance || a.Kind == PreviewAppearance {
				m.Appearance = theme.ParseConfig(a.Value)
			}
		})
		router.Frame(gtx.Ops)
	}
	view.ShowAppearance()
	frame()
	if got := m.Appearance.EffectiveIconSize(); got != theme.DefaultIconSize {
		t.Fatalf("initial icon size = %d, want %d", got, theme.DefaultIconSize)
	}
	// Click "22px 极简" chip.
	view.iconSizeChips[0].Click()
	frame()
	if latest.Kind != SaveAppearance || m.Appearance.EffectiveIconSize() != 22 {
		t.Fatalf("after 22px chip: action=%+v iconSize=%d", latest, m.Appearance.EffectiveIconSize())
	}
	// Click "66px 超大" chip.
	view.iconSizeChips[4].Click()
	frame()
	if latest.Kind != SaveAppearance || m.Appearance.EffectiveIconSize() != 66 {
		t.Fatalf("after 66px chip: action=%+v iconSize=%d", latest, m.Appearance.EffectiveIconSize())
	}
	// External update (e.g. Ctrl+Wheel on desktop) resyncs slider value.
	m.Appearance.SetIconSize(52)
	frame()
	wantSlider := float32(52-theme.MinIconSize) / float32(theme.MaxIconSize-theme.MinIconSize)
	if diff := view.iconSizeSlider.Value - wantSlider; diff < -0.01 || diff > 0.01 {
		t.Fatalf("iconSizeSlider.Value = %v, want %v", view.iconSizeSlider.Value, wantSlider)
	}
}

func TestSettingsPluginPermissionsDisplayAndRevoke(t *testing.T) {
	view := NewSettingsView()
	var router input.Router
	gtx := layout.Context{
		Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(980, 640)),
	}
	m := SettingsModel{
		Locale:     "zh-CN",
		Appearance: theme.DefaultConfig(),
		Plugins: []SettingsPlugin{
			{
				ID:                   "quailcrate.music",
				Name:                 "音乐播放器",
				Description:          "独立音乐与 Bilibili 播放插件",
				Icon:                 "music",
				Runtime:              "lua",
				State:                "running",
				Status:               "运行中",
				Enabled:              true,
				CanToggle:            true,
				RequestedPermissions: []string{"network.http", "network.local", "audio.play"},
				GrantedPermissions:   []string{"network.http", "audio.play"},
			},
		},
	}
	var actions []SettingsAction
	frame := func() {
		gtx.Reset()
		gtx.Now = time.Now()
		view.Layout(gtx, m, func(a SettingsAction) { actions = append(actions, a) })
		router.Frame(gtx.Ops)
	}
	view.ShowPlugins()
	frame()

	btns := view.pluginButtons["quailcrate.music"]
	if btns == nil {
		t.Fatal("missing plugin buttons")
	}
	// By default the permission drawer stays collapsed so plugin rows stay compact.
	if len(btns.revoke) != 0 {
		t.Fatalf("expected permission drawer collapsed by default, got revoke buttons %+v", btns.revoke)
	}
	// Expand the inline permission drawer via the permission summary button.
	btns.permToggle.Click()
	frame()
	if view.expandedPluginPerms != "quailcrate.music" {
		t.Fatalf("expected expandedPluginPerms=quailcrate.music, got %q", view.expandedPluginPerms)
	}
	if btns.revoke["network.http"] == nil || btns.revoke["audio.play"] == nil {
		t.Fatalf("expected revoke buttons after expanding drawer, got %+v", btns.revoke)
	}
	if btns.revoke["network.local"] != nil {
		t.Fatal("ungranted network.local permission should not create a revoke button")
	}
	// Verify semantic descriptions distinguish public internet vs audio revoke buttons.
	hasPublicInternet := slices.ContainsFunc(router.AppendSemantics(nil), func(node input.SemanticNode) bool {
		return node.Desc.Description == "音乐播放器 · 撤销权限 公开网络访问"
	})
	if !hasPublicInternet {
		t.Fatal("missing accessible label for public internet permission revoke button")
	}

	btns.revoke["network.http"].Click()
	frame()
	want := []SettingsAction{{Kind: RevokePermission, PluginID: "quailcrate.music", Value: "network.http"}}
	if !slices.Equal(actions, want) {
		t.Fatalf("revoke actions = %+v, want %+v", actions, want)
	}
}

func TestSettingsThirdPartyNativePluginWarningBadge(t *testing.T) {
	view := NewSettingsView()
	var router input.Router
	gtx := layout.Context{
		Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(980, 640)),
	}
	m := SettingsModel{
		Locale:     "zh-CN",
		Appearance: theme.DefaultConfig(),
		Plugins: []SettingsPlugin{
			{
				ID:               "quailcrate.music",
				Name:             "音乐",
				Description:      "随包原生插件",
				Runtime:          "dll",
				State:            "disabled",
				Status:           "已停用",
				CanToggle:        true,
				Bundled:          true,
				FullSystemAccess: false,
			},
			{
				ID:               "example.native-tools",
				Name:             "系统工具箱",
				Description:      "第三方全权限原生插件",
				Runtime:          "dll",
				State:            "disabled",
				Status:           "已停用",
				CanToggle:        true,
				Bundled:          false,
				FullSystemAccess: true,
			},
		},
	}
	view.ShowPlugins()
	gtx.Reset()
	gtx.Now = time.Now()
	view.Layout(gtx, m, func(SettingsAction) {})
	router.Frame(gtx.Ops)

	semantics := router.AppendSemantics(nil)
	hasThirdPartyWarn := slices.ContainsFunc(semantics, func(node input.SemanticNode) bool {
		return node.Desc.Description == "系统工具箱 · ⚠ 原生插件 · 完全系统权限"
	})
	if !hasThirdPartyWarn {
		t.Fatal("missing full-system-access warning badge for third-party DLL plugin")
	}
	hasBundledWarn := slices.ContainsFunc(semantics, func(node input.SemanticNode) bool {
		return node.Desc.Description == "音乐 · ⚠ 原生插件 · 完全系统权限"
	})
	if hasBundledWarn {
		t.Fatal("bundled DLL plugin should not display third-party risk warning badge")
	}
}

func TestSettingsGitHubPluginActionsAndInstallStatus(t *testing.T) {
	view := NewSettingsView()
	view.ShowPlugins()
	var router input.Router
	gtx := layout.Context{
		Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1.25, PxPerSp: 1.25},
		Constraints: layout.Exact(image.Pt(620, 650)),
	}
	m := SettingsModel{
		Locale: "en-US",
		Definition: &v2.Settings{Sections: []v2.SettingsSection{
			{ID: PagePlugins, Title: "Plugins", Cards: []v2.SettingsCard{{Type: "plugins", Title: "Plugin management"}}},
		}},
		Plugins: []SettingsPlugin{
			{ID: "example.counter", Name: "Counter", Version: "1.2.0", SourceRepo: "owner/counter", HomepageURL: "https://github.com/owner/counter", CanUninstall: true},
			{ID: "quailcrate.calendar", Name: "Calendar", Version: "2.0.0", Bundled: true},
		},
	}
	var actions []SettingsAction
	frame := func() []input.SemanticNode {
		gtx.Reset()
		gtx.Now = time.Now()
		view.Layout(gtx, m, func(a SettingsAction) { actions = append(actions, a) })
		router.Frame(gtx.Ops)
		return router.AppendSemantics(nil)
	}
	semantics := frame()
	for _, label := range []string{"Counter", "v1.2.0", "owner/counter", "Calendar", "v2.0.0"} {
		if !slices.ContainsFunc(semantics, func(n input.SemanticNode) bool { return n.Desc.Label == label }) {
			t.Errorf("missing visible plugin metadata %q", label)
		}
	}
	if slices.ContainsFunc(semantics, func(n input.SemanticNode) bool { return n.Desc.Description == "Calendar · Uninstall" }) {
		t.Error("bundled plugin exposes an uninstall button")
	}
	buttons := view.pluginButtons["example.counter"]
	view.installPlugin.Click()
	buttons.homepage.Click()
	buttons.uninstall.Click()
	frame()
	want := []SettingsAction{
		{Kind: InstallPluginFromGitHub},
		{Kind: UninstallPlugin, PluginID: "example.counter"},
		{Kind: OpenPluginHomepage, PluginID: "example.counter"},
	}
	if !slices.Equal(actions, want) {
		t.Fatalf("dispatched actions = %+v, want %+v", actions, want)
	}

	m.PluginInstall = SettingsPluginInstall{Busy: true, Message: "Downloading and verifying the package…"}
	view.installPlugin.Click()
	buttons.uninstall.Click()
	semantics = frame()
	if !slices.Equal(actions, want) {
		t.Fatalf("dispatch while busy = %+v, want %+v", actions, want)
	}
	for _, button := range []string{"Installing…", "Counter · Uninstall"} {
		if !slices.ContainsFunc(semantics, func(n input.SemanticNode) bool { return n.Desc.Description == button && n.Desc.Disabled }) {
			t.Errorf("missing disabled install control %q", button)
		}
	}
	if !slices.ContainsFunc(semantics, func(n input.SemanticNode) bool { return n.Desc.Label == m.PluginInstall.Message }) {
		t.Error("missing accessible installation progress")
	}

	m.PluginInstall = SettingsPluginInstall{Busy: true, Uninstalling: true}
	semantics = frame()
	if !slices.ContainsFunc(semantics, func(n input.SemanticNode) bool {
		return n.Desc.Description == "Install from GitHub…" && n.Desc.Disabled
	}) {
		t.Error("missing disabled install button during uninstall")
	}

	m.Locale = "zh-CN"
	m.PluginInstall = SettingsPluginInstall{Message: "无法确定应安装的 ZIP 附件，请复制对应的下载链接。", IsError: true}
	semantics = frame()
	if !slices.ContainsFunc(semantics, func(n input.SemanticNode) bool { return n.Desc.Label == m.PluginInstall.Message }) {
		t.Error("missing accessible installation error")
	}
	if !slices.ContainsFunc(semantics, func(n input.SemanticNode) bool {
		return n.Desc.Description == "从 GitHub 安装…" && !n.Desc.Disabled
	}) {
		t.Error("missing enabled Chinese install button after error")
	}
}

func TestSettingsGeneralAutostartStatesAndActions(t *testing.T) {
	view := NewSettingsView()
	var router input.Router
	// Tall viewport so all General cards render without needing scroll input.
	gtx := layout.Context{
		Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(980, 1100)),
	}
	var actions []SettingsAction
	render := func(m SettingsModel) []input.SemanticNode {
		gtx.Reset()
		gtx.Now = time.Now()
		view.Layout(gtx, m, func(a SettingsAction) { actions = append(actions, a) })
		router.Frame(gtx.Ops)
		return router.AppendSemantics(nil)
	}

	// Disabled while loading, unavailable, conflicted, or saving.
	for _, state := range []SettingsAutostart{
		{Loaded: false, Loading: true},
		{Loaded: true, Available: false},
		{Loaded: true, Available: true, Conflict: true},
		{Loaded: true, Available: true, Saving: true},
	} {
		actions = nil
		render(SettingsModel{Locale: "zh-CN", Autostart: state})
		view.autostartToggle.Click()
		render(SettingsModel{Locale: "zh-CN", Autostart: state})
		if len(actions) != 0 {
			t.Fatalf("disabled autostart state %+v emitted actions: %+v", state, actions)
		}
	}

	// Available and off -> emits ToggleAutostart{Enabled: true}; English and Chinese labels both render.
	for _, tc := range []struct {
		locale, wantDesc string
		enabled          bool
	}{
		{"zh-CN", "开机启动", false},
		{"en-US", "Start on sign-in", true},
	} {
		actions = nil
		m := SettingsModel{
			Locale:    tc.locale,
			Autostart: SettingsAutostart{Loaded: true, Available: true, Enabled: tc.enabled},
		}
		sem := render(m)
		if !slices.ContainsFunc(sem, func(n input.SemanticNode) bool { return n.Desc.Description == tc.wantDesc }) {
			t.Fatalf("locale %s missing autostart switch description %q", tc.locale, tc.wantDesc)
		}
		view.autostartToggle.Click()
		render(m)
		want := []SettingsAction{{Kind: ToggleAutostart, Enabled: !tc.enabled}}
		if !slices.Equal(actions, want) {
			t.Fatalf("locale %s autostart actions = %+v, want %+v", tc.locale, actions, want)
		}
	}

	// Read failure exposes Retry button.
	actions = nil
	errModel := SettingsModel{
		Locale:    "en-US",
		Autostart: SettingsAutostart{Loaded: true, Error: "read_failed"},
	}
	render(errModel)
	view.autostartRetry.Click()
	render(errModel)
	if !slices.Equal(actions, []SettingsAction{{Kind: RefreshAutostart}}) {
		t.Fatalf("retry actions = %+v, want RefreshAutostart", actions)
	}
}

func TestSettingsGeneralUpdatesActionsGuardsAndLocalization(t *testing.T) {
	view := NewSettingsView()
	var router input.Router
	gtx := layout.Context{
		Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(980, 1280)),
	}
	var actions []SettingsAction
	render := func(m SettingsModel) []input.SemanticNode {
		gtx.Reset()
		gtx.Now = time.Now()
		view.Layout(gtx, m, func(a SettingsAction) { actions = append(actions, a) })
		router.Frame(gtx.Ops)
		return router.AppendSemantics(nil)
	}

	// 1. Dev build disables auto-update, check, and restart.
	devModel := SettingsModel{
		Locale: "zh-CN",
		Update: SettingsUpdate{CurrentVersion: "0.0.0-dev", Channel: "dev", Phase: "idle"},
	}
	render(devModel)
	view.autoUpdateToggle.Click()
	view.checkUpdateButton.Click()
	view.restartUpdateButton.Click()
	render(devModel)
	if len(actions) != 0 {
		t.Fatalf("dev build emitted update actions: %+v", actions)
	}

	// 2. Signed alpha build in idle allows toggling auto-update and checking, but not restarting.
	alphaIdle := SettingsModel{
		Locale: "zh-CN",
		Update: SettingsUpdate{
			CurrentVersion: "0.1.0-alpha.1",
			Channel:        "alpha",
			AutoUpdate:     true,
			Phase:          "idle",
			LastCheckedAt:  time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC),
		},
	}
	render(alphaIdle)
	view.autoUpdateToggle.Click()
	view.checkUpdateButton.Click()
	view.restartUpdateButton.Click()
	render(alphaIdle)
	wantIdle := []SettingsAction{
		{Kind: ToggleAutoUpdate, Enabled: false},
		{Kind: CheckUpdateNow},
	}
	if !slices.Equal(actions, wantIdle) {
		t.Fatalf("alpha idle actions = %+v, want %+v", actions, wantIdle)
	}

	// 3. Busy phases block duplicate check and restart clicks.
	for _, busy := range []SettingsUpdate{
		{CurrentVersion: "0.1.0-alpha.1", Channel: "alpha", Phase: "checking", TargetVersion: "0.1.0-alpha.2", CanInstall: true},
		{CurrentVersion: "0.1.0-alpha.1", Channel: "alpha", Phase: "downloading", TargetVersion: "0.1.0-alpha.2", DownloadedBytes: 4 << 20, TotalBytes: 10 << 20, CanInstall: true},
		{CurrentVersion: "0.1.0-alpha.1", Channel: "alpha", Phase: "installing", TargetVersion: "0.1.0-alpha.2", CanInstall: true},
		{CurrentVersion: "0.1.0-alpha.1", Channel: "alpha", Phase: "ready", TargetVersion: "0.1.0-alpha.2", CanInstall: true, RestartPending: true},
	} {
		actions = nil
		m := SettingsModel{Locale: "en-US", Update: busy}
		render(m)
		view.checkUpdateButton.Click()
		view.restartUpdateButton.Click()
		render(m)
		if len(actions) != 0 {
			t.Fatalf("busy update state %+v emitted actions: %+v", busy, actions)
		}
	}

	// 4. Legacy portable requiring manual migration disables restart and opens release URL.
	const releaseURL = "https://github.com/SuperQuail/QuailCrate/releases/tag/v0.1.0-alpha.2"
	actions = nil
	manualModel := SettingsModel{
		Locale: "zh-CN",
		Update: SettingsUpdate{
			CurrentVersion: "0.1.0-alpha.1",
			TargetVersion:  "0.1.0-alpha.2",
			Channel:        "alpha",
			Phase:          "available",
			CanInstall:     false,
			ReleaseURL:     releaseURL,
		},
	}
	sem := render(manualModel)
	if !slices.ContainsFunc(sem, func(n input.SemanticNode) bool { return n.Desc.Description == "打开发布页下载" }) {
		t.Fatal("missing manual migration release download button")
	}
	view.restartUpdateButton.Click()
	view.releaseLinkButton.Click()
	render(manualModel)
	if !slices.Equal(actions, []SettingsAction{{Kind: OpenUpdateRelease, Value: releaseURL}}) {
		t.Fatalf("manual portable actions = %+v, want OpenUpdateRelease only", actions)
	}

	// 5. Ready or failed-with-cached-package allows RestartUpdate.
	for _, u := range []SettingsUpdate{
		{CurrentVersion: "0.1.0-alpha.1", TargetVersion: "0.1.0-alpha.2", Channel: "alpha", Phase: "ready", CanInstall: true, ReleaseURL: releaseURL},
		{CurrentVersion: "0.1.0-alpha.1", TargetVersion: "0.1.0-alpha.2", Channel: "alpha", Phase: "failed", ErrorCode: "install_busy", LastInstallErrorCode: "install_busy", CanInstall: true, ReleaseURL: releaseURL},
	} {
		actions = nil
		m := SettingsModel{Locale: "en-US", Update: u}
		render(m)
		view.restartUpdateButton.Click()
		render(m)
		if !slices.Equal(actions, []SettingsAction{{Kind: RestartUpdate}}) {
			t.Fatalf("installable state %+v actions = %+v, want RestartUpdate", u, actions)
		}
	}

	// 6. All documented error codes have distinct zh-CN and en-US copy.
	codes := []string{"network", "release", "signature", "integrity", "download", "cache", "settings", "install_failed", "install_busy", "restart_failed", "unavailable"}
	for _, code := range codes {
		zh := settingsFrame{model: SettingsModel{Locale: "zh-CN"}}.updateErrorText(code)
		en := settingsFrame{model: SettingsModel{Locale: "en-US"}}.updateErrorText(code)
		if zh == "" || en == "" || zh == en {
			t.Fatalf("error code %q missing bilingual text: zh=%q en=%q", code, zh, en)
		}
	}
}
