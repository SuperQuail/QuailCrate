package plugins

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	v1 "quailcrate/pluginapi/v1"
)

func productFixture(t *testing.T, path ...string) *luaRuntime {
	t.Helper()
	directory := filepath.Join(append([]string{"..", ".."}, path...)...)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runtime, err := openLua(ctx, Package{Dir: directory, Entry: filepath.Join(directory, "plugin.lua")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func productCall(t *testing.T, runtime *luaRuntime, op string, payload v1.Payload) v1.Data {
	t.Helper()
	if payload.Locale == "" {
		payload.Locale = "zh-CN"
	}
	translations, err := loadTranslations(runtime.packageDir)
	if err != nil {
		t.Fatal(err)
	}
	payload.Strings = translations[payload.Locale]
	request := v1.Request{RequestID: op + "-test", Op: op, Payload: payload}
	input, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := runtime.Call(ctx, input)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	var response v1.Response
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("%s: %v, response %s", op, err, output)
	}
	if !response.OK || response.RequestID != request.RequestID {
		t.Fatalf("%s: incorrect response %s", op, output)
	}
	return response.Data
}

func TestLauncherPluginSearchAndLaunch(t *testing.T) {
	runtime := productFixture(t, "plugins", "launcher")
	init := productCall(t, runtime, "init", v1.Payload{})
	if !init.Search || len(init.Commands) != 1 {
		t.Fatalf("init data = %+v", init)
	}
	if init.Launcher == nil {
		t.Fatal("missing launcher surface")
	}
	if err := init.Launcher.Validate(); err != nil {
		t.Fatalf("invalid bundled launcher tree: %v", err)
	}
	payload := v1.Payload{Text: "  计算器  ", Limit: 2, Apps: []v1.App{
		{ID: "start:calc", Name: "计算器快捷方式", Source: "start-menu"},
		{ID: "other", Name: "画图", Source: "start-menu"},
		{ID: "shell:calc", Name: "计算器", Source: "packaged"},
	}}
	search := productCall(t, runtime, "search", payload)
	if len(search.Results) != 2 || search.Results[0].ID != "shell:calc" || search.Results[0].Subtitle != "打包应用" {
		t.Fatalf("search results = %+v", search.Results)
	}
	payload.Locale = "en-US"
	english := productCall(t, runtime, "search", payload)
	if english.Results[0].Subtitle != "Packaged app" || english.Results[1].Subtitle != "Start menu" {
		t.Fatalf("English sources = %+v", english.Results)
	}
	command := search.Results[0].Command
	launch := productCall(t, runtime, "command", v1.Payload{CommandID: command.ID, Args: command.Args})
	if len(launch.Actions) != 1 || launch.Actions[0].Type != "apps.launch" || launch.Actions[0].AppID != "shell:calc" {
		t.Fatalf("launch actions = %+v", launch.Actions)
	}
}

func TestWorkbenchRegistersSettingsInBothLocales(t *testing.T) {
	rt := productFixture(t, "plugins", "workbench")
	for _, locale := range []string{"en-US", "zh-CN"} {
		init := productCall(t, rt, "init", v1.Payload{Locale: locale})
		if init.Settings == nil {
			t.Fatal("missing settings registration")
		}
		if err := init.Settings.Validate(); err != nil {
			t.Fatal(err)
		}
		if len(init.Settings.Sections) != 5 || init.Settings.Section("appearance") == nil || len(init.Settings.Section("appearance").Cards) != 6 {
			t.Fatalf("incomplete workbench in %s", locale)
		}
	}
}

func TestCalendarPluginLeapDateAndMonthNavigation(t *testing.T) {
	runtime := productFixture(t, "plugins", "calendar")
	init := productCall(t, runtime, "init", v1.Payload{})
	if len(init.Widgets) != 1 || !init.Widgets[0].Default || len(init.Commands) != 0 || len(init.AppTabs) != 0 {
		t.Fatalf("init data = %+v", init)
	}
	payload := v1.Payload{Date: "2024-02-29", WidgetID: "month"}
	view := productCall(t, runtime, "widget_state", payload).View
	grid := view.DisplayControls()[3]
	if view.Title != "2024 年 2 月" || grid.Columns != 7 || len(grid.Items) != 49 {
		t.Fatalf("invalid leap-month view: %+v", view)
	}
	if !grid.Items[0].Header() || !grid.Items[5].Header() || !grid.Items[6].Header() || grid.Items[5].State != "weekend" || grid.Items[6].State != "weekend" {
		t.Fatalf("weekday headers should use header/header-weekend states: %+v", grid.Items[:7])
	}
	// Monday is column zero, so leap day in February 2024 falls in column three.
	if cell := grid.Items[7+3+28]; cell.Text != "29" || cell.State != "current" || cell.ID != "day-2024-02-29" {
		t.Fatalf("leap day cell = %+v", cell)
	}
	// Saturday Feb 3, 2024 is at column 5 in the first week row.
	if sat := grid.Items[7+5]; sat.Text != "3" || sat.State != "weekend" {
		t.Fatalf("weekend cell = %+v", sat)
	}
	if len(view.DisplayControls()) < 5 || view.DisplayControls()[4].Text != "2024-02-29  ·  W09  ·  D060/366  ·  16%" || view.DisplayControls()[5].Value <= 0 {
		t.Fatalf("telemetry footer = %+v (progress=%v)", view.DisplayControls(), view.DisplayControls()[5].Value)
	}

	// Selecting a date updates telemetry with delta days; clicking a muted next-month cell jumps months.
	payload.Event = &v1.Event{Type: "widget.action", WidgetID: "month", ControlID: "days", ItemID: "day-2024-03-02"}
	productCall(t, runtime, "event", payload)
	jumped := productCall(t, runtime, "widget_state", payload).View
	if jumped.Title != "2024 年 3 月" || jumped.DisplayControls()[4].Text != "2024-03-02  ·  +2d  ·  W09  ·  D062/366" {
		t.Fatalf("muted day jump = %+v", jumped)
	}

	// Navigating back to the current month clears browsing so midnight rollover follows automatically.
	payload.Event = &v1.Event{Type: "widget.action", WidgetID: "month", ControlID: "previous"}
	productCall(t, runtime, "event", payload)
	payload.Date = "2024-03-01"
	if title := productCall(t, runtime, "widget_state", payload).View.Title; title != "2024 年 3 月" {
		t.Fatalf("rollover after returning to current month stayed stuck on %q", title)
	}

	payload.Date = "2024-12-31"
	payload.Event = &v1.Event{Type: "widget.action", WidgetID: "month", ControlID: "next"}
	next := productCall(t, runtime, "event", payload)
	if len(next.Actions) != 1 || next.Actions[0].Type != "widget.refresh" {
		t.Fatalf("next month actions = %+v", next.Actions)
	}
	if title := productCall(t, runtime, "widget_state", payload).View.Title; title != "2025 年 1 月" {
		t.Fatalf("next month = %q", title)
	}
	payload.Event.ControlID = "today"
	productCall(t, runtime, "event", payload)
	if title := productCall(t, runtime, "widget_state", payload).View.Title; title != "2024 年 12 月" {
		t.Fatalf("current month = %q", title)
	}
	payload.Locale = "en-US"
	view = productCall(t, runtime, "widget_state", payload).View
	if view.Title != "December 2024" || len(view.LayoutTree().Header) != 3 || view.DisplayControls()[3].Items[0].Text != "MON" {
		t.Fatalf("English calendar = %+v", view)
	}
}

func TestGroupsPluginUsesReferencesAndStableWidgetIDs(t *testing.T) {
	runtime := productFixture(t, "plugins", "desktop-groups")
	create := productCall(t, runtime, "event", v1.Payload{Event: &v1.Event{
		Type: "action.result", ActionID: "new", Value: " 工作 ",
	}})
	if len(create.Actions) != 1 || create.Actions[0].Type != "widget.create" || len(create.State) == 0 {
		t.Fatalf("create result = %+v", create)
	}
	widgetID := create.Actions[0].WidgetID
	payload := v1.Payload{State: create.State, WidgetID: widgetID, Event: &v1.Event{
		Type: "widget.drop", WidgetID: widgetID, ControlID: "items", Paths: []string{`C:\work\文档.txt`, `C:\work\资料`, `C:\work\应用.lnk`},
	}}
	payload.State = productCall(t, runtime, "event", payload).State
	view := productCall(t, runtime, "widget_state", payload).View
	list := view.DisplayControls()[2]
	if view.Title != "工作" || len(list.Items) != 3 || list.Items[0].Text != "文档.txt" ||
		list.Items[2].Text != "应用" || list.Items[0].Icon != `C:\work\文档.txt` ||
		list.Columns != 4 || !list.Drop || !list.Reorder || len(view.Menu) < 5 {
		t.Fatalf("group view = %+v", view)
	}
	shortcut := list.Items[2]
	payload.Event = &v1.Event{Type: "widget.reorder", WidgetID: widgetID, ControlID: "items", ItemID: shortcut.ID, From: 2, To: 0}
	payload.State = productCall(t, runtime, "event", payload).State
	view = productCall(t, runtime, "widget_state", payload).View
	if view.DisplayControls()[2].Items[0].ID != shortcut.ID {
		t.Fatalf("order = %+v", view.DisplayControls()[2].Items)
	}
	payload.Event = &v1.Event{Type: "widget.action", WidgetID: widgetID, ControlID: "items", ItemID: shortcut.ID}
	open := productCall(t, runtime, "event", payload)
	if len(open.Actions) != 1 || open.Actions[0].Type != "paths.open" || open.Actions[0].Path != `C:\work\应用.lnk` {
		t.Fatalf("open actions = %+v", open.Actions)
	}
	payload.Event = &v1.Event{Type: "action.result", WidgetID: widgetID, ItemID: shortcut.ID, ActionID: "open", Error: "missing"}
	productCall(t, runtime, "event", payload)
	view = productCall(t, runtime, "widget_state", payload).View
	if item := view.DisplayControls()[2].Items[0]; item.State != "error" || item.Hint != "File not found or could not be opened." {
		t.Fatalf("missing item = %+v", item)
	}
	payload.Locale = "en-US"
	enView := productCall(t, runtime, "widget_state", payload).View
	foundAddFiles := false
	for _, entry := range enView.Menu {
		if entry.ID == "add_files" && entry.Text == "Add files…" {
			foundAddFiles = true
			break
		}
	}
	if !foundAddFiles {
		t.Fatalf("English menu missing add_files = %+v", enView.Menu)
	}
}

func TestGroupsPluginV2MigrationTabsURLsAppsOpenersAndSearch(t *testing.T) {
	runtime := productFixture(t, "plugins", "desktop-groups")
	init := productCall(t, runtime, "init", v1.Payload{})
	if !init.Search || len(init.Commands) < 2 {
		t.Fatalf("expected search contribution and launch command: %+v", init)
	}

	// Verify v1 state migrates cleanly to v2 without data loss.
	v1State := json.RawMessage(`{"nextGroup":2,"nextItem":3,"groups":{"group-1":{"name":"工作","items":[{"id":"item-1","path":"F:\\Projects\\QuailCrate"}]}}}`)
	tools := []v1.Tool{
		{ID: "explorer", Name: "Explorer", Kind: "system"},
		{ID: "terminal", Name: "Terminal", Kind: "terminal"},
		{ID: "vscode", Name: "VS Code", Kind: "editor"},
	}
	apps := []v1.App{
		{ID: "package:Microsoft.GamingApp!App", Name: "Xbox", Source: "packaged", Icon: `shell:AppsFolder\Microsoft.GamingApp!App`},
	}

	// Single tab hides inline tab bar.
	view1 := productCall(t, runtime, "widget_state", v1.Payload{State: v1State, WidgetID: "group-1", Tools: tools, Apps: apps}).View
	if len(view1.Tabs) != 0 || len(view1.DisplayControls()[2].Items) != 1 || view1.DisplayControls()[2].Items[0].Text != "QuailCrate" {
		t.Fatalf("migrated single-tab view = %+v", view1)
	}

	// Set tab default opener to VS Code and switch to compact list view.
	state := productCall(t, runtime, "event", v1.Payload{
		State: v1State, WidgetID: "group-1", Tools: tools,
		Event: &v1.Event{Type: "action.result", WidgetID: "group-1", ActionID: "tab_opener", Items: []string{"tool:vscode"}},
	}).State
	state = productCall(t, runtime, "event", v1.Payload{
		State: state, WidgetID: "group-1", Tools: tools,
		Event: &v1.Event{Type: "widget.action", WidgetID: "group-1", ControlID: "toggle_view"},
	}).State
	listView := productCall(t, runtime, "widget_state", v1.Payload{State: state, WidgetID: "group-1", Tools: tools}).View
	if listView.DisplayControls()[2].Columns != 1 || listView.DisplayControls()[2].Items[0].Badge != "VS Code" {
		t.Fatalf("project list view with VS Code badge = %+v", listView.DisplayControls()[2])
	}

	// Clicking project item emits paths.openWith using VS Code.
	openProj := productCall(t, runtime, "event", v1.Payload{
		State: state, WidgetID: "group-1", Tools: tools,
		Event: &v1.Event{Type: "widget.action", WidgetID: "group-1", ControlID: "items", ItemID: "item-1"},
	})
	if len(openProj.Actions) != 1 || openProj.Actions[0].Type != "paths.openWith" || openProj.Actions[0].ToolID != "vscode" {
		t.Fatalf("open project actions = %+v", openProj.Actions)
	}

	// Add a second tab ("游戏") -> inline Tabs appear.
	state = productCall(t, runtime, "event", v1.Payload{
		State: state, WidgetID: "group-1",
		Event: &v1.Event{Type: "action.result", WidgetID: "group-1", ActionID: "new_tab", Value: "游戏"},
	}).State
	// Drop an imported Steam .url shortcut, a browser URL, and add Xbox UWP app.
	state = productCall(t, runtime, "event", v1.Payload{
		State: state, WidgetID: "group-1", Apps: apps,
		Event: &v1.Event{
			Type: "widget.drop", WidgetID: "group-1", ControlID: "items",
			Imported: []v1.ImportedShortcut{{Name: "黑神话：悟空", URL: "steam://rungameid/2358720", CachedIcon: `imgfile:C:\cache\wukong.png`}},
			Urls:     []string{"http://localhost:3000"},
		},
	}).State
	state = productCall(t, runtime, "event", v1.Payload{
		State: state, WidgetID: "group-1", Apps: apps,
		Event: &v1.Event{Type: "action.result", WidgetID: "group-1", ActionID: "add_apps", Items: []string{"package:Microsoft.GamingApp!App"}},
	}).State

	tabView := productCall(t, runtime, "widget_state", v1.Payload{State: state, WidgetID: "group-1", Apps: apps, Tools: tools}).View
	if len(tabView.Tabs) != 2 || tabView.ActiveTab != tabView.Tabs[1].ID || len(tabView.DisplayControls()[2].Items) != 3 {
		t.Fatalf("multi-tab view = %+v", tabView)
	}
	if tabView.DisplayControls()[2].Items[0].Icon != `imgfile:C:\cache\wukong.png` ||
		tabView.DisplayControls()[2].Items[1].Icon != "favicon:http://localhost:3000" ||
		tabView.DisplayControls()[2].Items[2].Icon != `shell:AppsFolder\Microsoft.GamingApp!App` {
		t.Fatalf("item icons = %+v", tabView.DisplayControls()[2].Items)
	}

	// Alt+Space search finds the intranet URL and the Steam game with icons.
	search := productCall(t, runtime, "search", v1.Payload{State: state, Text: "悟空", Limit: 5, Apps: apps, Tools: tools})
	if len(search.Results) != 1 || search.Results[0].Icon != `imgfile:C:\cache\wukong.png` {
		t.Fatalf("search results for game = %+v", search.Results)
	}
	launchGame := productCall(t, runtime, "command", v1.Payload{State: state, CommandID: search.Results[0].Command.ID, Args: search.Results[0].Command.Args})
	if len(launchGame.Actions) != 1 || launchGame.Actions[0].Type != "urls.open" || launchGame.Actions[0].URL != "steam://rungameid/2358720" {
		t.Fatalf("launch game actions = %+v", launchGame.Actions)
	}

	// Delete group uses ui.confirm instead of typing DELETE.
	delReq := productCall(t, runtime, "event", v1.Payload{
		State: state, WidgetID: "group-1",
		Event: &v1.Event{Type: "widget.action", WidgetID: "group-1", ControlID: "delete"},
	})
	if len(delReq.Actions) != 1 || delReq.Actions[0].Type != "ui.confirm" {
		t.Fatalf("delete should request ui.confirm, got %+v", delReq.Actions)
	}
}

func TestIndependentLuaWidgetCanPersistState(t *testing.T) {
	runtime := productFixture(t, "examples", "lua-widget")
	event := v1.Payload{Event: &v1.Event{Type: "widget.action", WidgetID: "counter", ControlID: "plus"}}
	result := productCall(t, runtime, "event", event)
	runtime = productFixture(t, "examples", "lua-widget")
	payload := v1.Payload{WidgetID: "counter", State: result.State}
	view := productCall(t, runtime, "widget_state", payload).View
	if view.Controls[0].Text != "当前计数  1" {
		t.Fatalf("restored view = %+v", view)
	}
	payload.Locale = "en-US"
	view = productCall(t, runtime, "widget_state", payload).View
	if view.Title != "Example Counter" || view.Controls[0].Text != "Current count  1" {
		t.Fatalf("restored English view = %+v", view)
	}
}

func TestCounterExampleAppTabsShareWidgetState(t *testing.T) {
	pkg := (Catalog{}).readPackage(filepath.Join("..", "..", "examples"), "lua-widget", false)
	if pkg.Err != nil {
		t.Fatal(pkg.Err)
	}
	for _, tc := range []struct{ locale, overview, counter, count string }{
		{"zh-CN", "概览", "计数器", "当前计数"},
		{"en-US", "Overview", "Counter", "Current count"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			manager := NewManager()
			ctx := context.Background()
			init, err := manager.Enable(ctx, pkg, nil, v1.Payload{Locale: tc.locale})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { manager.Disable(ctx, pkg.Manifest.ID) })
			tabs := init.Data.AppTabs
			if len(tabs) != 2 || tabs[0].ID != "overview" || tabs[0].Title != tc.overview ||
				tabs[1].ID != "counter" || tabs[1].Title != tc.counter || len(init.Data.Widgets) != 1 ||
				init.Data.Widgets[0].ID != "counter" || !init.Data.Widgets[0].Default {
				t.Fatalf("unexpected localized contributions: %+v", init.Data)
			}
			call := func(op string, payload v1.Payload) v1.Data {
				t.Helper()
				payload.Locale = tc.locale
				result, err := manager.CallGeneration(ctx, pkg.Manifest.ID, init.Generation, op, payload)
				if err != nil {
					t.Fatalf("%s: %v", op, err)
				}
				return result.Data
			}
			plus := call("event", v1.Payload{TabID: "counter", Event: &v1.Event{
				Type: "tab.action", TabID: "counter", ControlID: "plus",
			}})
			overview := call("tab_state", v1.Payload{TabID: "overview", State: plus.State}).View
			if overview.Title != tc.overview || len(overview.Controls) != 1 || overview.Controls[0].Text != tc.count+"  1" {
				t.Fatalf("overview after tab increment = %+v", overview)
			}
			widget := call("widget_state", v1.Payload{WidgetType: "counter", WidgetID: "counter", State: plus.State}).View
			if len(widget.Controls) != 3 || widget.Controls[0].Text != tc.count+"  1" {
				t.Fatalf("widget after tab increment = %+v", widget)
			}
			minus := call("event", v1.Payload{TabID: "counter", State: plus.State, Event: &v1.Event{
				Type: "tab.action", TabID: "counter", ControlID: "minus",
			}})
			if string(minus.State) != `{"count":0}` {
				t.Fatalf("counter state after tab decrement = %s", minus.State)
			}
			widgetUpdate := call("event", v1.Payload{State: minus.State, Event: &v1.Event{
				Type: "widget.action", WidgetID: "counter", ControlID: "minus",
			}})
			counter := call("tab_state", v1.Payload{TabID: "counter", State: widgetUpdate.State}).View
			if counter.Title != tc.counter || len(counter.Controls) != 3 || counter.Controls[0].Text != tc.count+"  -1" ||
				counter.Controls[1].ID != "minus" || counter.Controls[2].ID != "plus" {
				t.Fatalf("counter tab after tab and widget decrements = %+v", counter)
			}
		})
	}
}

func TestWallpaperPluginUsesPickerAndHandlesCancel(t *testing.T) {
	runtime := productFixture(t, "plugins", "wallpaper")
	pick := productCall(t, runtime, "command", v1.Payload{CommandID: "choose"})
	if len(pick.Actions) != 1 || pick.Actions[0].Type != "ui.pickPaths" || len(pick.Actions[0].Extensions) != 2 {
		t.Fatalf("picker actions = %+v", pick.Actions)
	}
	for locale, expected := range map[string]string{"en-US": "Set system wallpaper", "zh-CN": "设置系统壁纸"} {
		init := productCall(t, runtime, "init", v1.Payload{Locale: locale})
		if len(init.Commands) != 1 || init.Commands[0].Title != expected {
			t.Fatalf("%s commands = %+v", locale, init.Commands)
		}
	}
	event := v1.Payload{Event: &v1.Event{Type: "action.result", ActionID: pick.Actions[0].ID, Canceled: true}}
	if canceled := productCall(t, runtime, "event", event); len(canceled.Actions) != 0 {
		t.Fatalf("canceled picker actions = %+v", canceled.Actions)
	}
	event.Event.Canceled = false
	event.Event.Paths = []string{`C:\wallpaper.png`}
	set := productCall(t, runtime, "event", event)
	if len(set.Actions) != 1 || set.Actions[0].Type != "wallpaper.set" || set.Actions[0].Path != event.Event.Paths[0] {
		t.Fatalf("wallpaper actions = %+v", set.Actions)
	}
	event.Event = &v1.Event{Type: "action.result", ActionID: set.Actions[0].ID, Error: "File unavailable."}
	failed := productCall(t, runtime, "event", event)
	if len(failed.Actions) != 1 || failed.Actions[0].Type != "ui.message" || failed.Actions[0].Value != "File unavailable." {
		t.Fatalf("failed wallpaper actions = %+v", failed.Actions)
	}
}

func TestCalendarWidgetBindingsAndIndependentInstances(t *testing.T) {
	runtime := productFixture(t, "plugins", "calendar")
	payload := v1.Payload{Date: "2024-02-29", WidgetID: "calendar-one"}
	page := productCall(t, runtime, "widget_state", payload).View
	if err := validateView(page); err != nil {
		t.Fatal(err)
	}
	layout := page.LayoutTree()
	if len(layout.Header) != 3 || layout.WheelUp != "previous" || layout.WheelDown != "next" {
		t.Fatal("missing widget toolbar or wheel bindings")
	}
	if !page.AllowsTabAction("next", "") || page.AllowsTabAction("forward", "") || !page.AllowsTabAction("days", "day-2024-02-29") || page.AllowsTabAction("days", "weekday-1") {
		t.Fatal("action bindings or header hit targets")
	}
	payload.Event = &v1.Event{Type: "widget.action", WidgetID: payload.WidgetID, ControlID: layout.WheelDown}
	productCall(t, runtime, "event", payload)
	if got := productCall(t, runtime, "widget_state", payload).View.Title; got == page.Title {
		t.Fatal("wheel binding did not advance the widget")
	}
	payload.WidgetID = "calendar-two"
	if got := productCall(t, runtime, "widget_state", payload).View.Title; got != page.Title {
		t.Fatal("navigation affected another widget instance")
	}
	copied := page.Clone()
	copied.Root.Children[1].Props.Items[0].Key = "injected"
	copied.Root.Children[0].Children[0].Events["activate"] = "other"
	if page.Root.Children[1].Props.Items[0].Key == "injected" || page.Root.Children[0].Children[0].Events["activate"] == "other" {
		t.Fatal("clone shared plugin-controlled data")
	}
}
