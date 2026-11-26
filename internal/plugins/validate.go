package plugins

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

var eventTypes = []string{"date.changed", "apps.changed", "widget.action", "widget.drop", "widget.reorder", "widget.transfer", "widget.tabSelect", "tab.action", "action.result"}

func validateRequest(request v1.Request, grants []string, init v1.Data) error {
	payload := request.Payload
	switch request.Op {
	case "init", "stop":
		return nil
	case "service":
		if payload.Caller == "" || payload.Method == "" || payload.ServiceVersion == "" || !validArgs(payload.Args) {
			return errors.New("invalid service invocation")
		}
	case "search":
		if !init.Search || payload.Limit < 1 || payload.Limit > v1.MaxResults {
			return errors.New("invalid or unregistered search request")
		}
	case "command":
		if !hasCommand(init.Commands, payload.CommandID) {
			return fmt.Errorf("unregistered command %q", payload.CommandID)
		}
	case "widget_state":
		if payload.WidgetID == "" || !hasWidget(init.Widgets, payload.WidgetType) {
			return fmt.Errorf("invalid or unregistered widget %q", payload.WidgetType)
		}
	case "tab_state":
		if !hasAppTab(init.AppTabs, payload.TabID) {
			return fmt.Errorf("unregistered app tab %q", payload.TabID)
		}
	case "event":
		event := payload.Event
		if event == nil || (!contains(eventTypes, event.Type) && event.Type != "page.message" && event.Type != "service.result" && event.Type != "plugin.event" && !toolEvent(event.Type)) {
			return errors.New("invalid event")
		}
		if event.Type == "page.message" {
			if err := validatePageEvent(event, payload, init.AppTabs); err != nil {
				return err
			}
		}
		if len(event.InputValue) != 0 && ((event.Type != "tab.action" && event.Type != "widget.action") || event.ItemID != "" || !v2.ValidID(event.ControlID) || v2.InputValueType(event.InputValue) == "") {
			return errors.New("invalid committed native control value")
		}
		if event.Type == "tab.action" && (!hasAppTab(init.AppTabs, event.TabID) ||
			(payload.TabID != "" && payload.TabID != event.TabID) || !v2.ValidID(event.ControlID)) {
			return errors.New("tab.action requires a registered tab and a valid control ID")
		}
		if event.Type == "date.changed" || event.Type == "apps.changed" {
			if !contains(init.Subscribe, event.Type) {
				return fmt.Errorf("plugin did not subscribe to %q", event.Type)
			}
		}
		if event.Type == "apps.changed" && !contains(grants, "apps.read") {
			return apiError("permission_denied", "apps.read is required for apps.changed")
		}
		if strings.HasPrefix(event.Type, "widget.") && (event.WidgetID == "" && payload.WidgetID == "" || len(init.Widgets) == 0) {
			return errors.New("widget event requires a registered widget instance")
		}
	default:
		return fmt.Errorf("unknown operation %q", request.Op)
	}
	return nil
}

func validateResponse(manifest v1.Manifest, grants []string, request v1.Request, response v1.Response, init v1.Data) error {
	if request.RequestID != response.RequestID {
		return errors.New("response request ID does not match")
	}
	data := response.Data
	_, page := appPage(init.AppTabs, request.Payload.TabID)
	pageQuery := page && request.Op == "tab_state"
	if len(data.PageState) != 0 && (!pageQuery || !response.OK || !v2.PageObject(data.PageState, v2.MaxPageStateBytes)) {
		return errors.New("invalid application page snapshot")
	}
	if len(data.PageReply) != 0 && (request.Op != "event" || request.Payload.Event == nil || request.Payload.Event.Type != "page.message" || !response.OK || !v2.PageObject(data.PageReply, v2.MaxPageArgsBytes)) {
		return errors.New("invalid application page reply")
	}
	if !response.OK && len(data.Tray) != 0 {
		return errors.New("a failed response cannot register tray items")
	}
	if data.Settings != nil {
		if !response.OK || request.Op != "init" || manifest.UIVersion != 2 || manifest.HostAPIVersion != 2 || !contains(manifest.Contributes, "settings") {
			return errors.New("settings requires a declared v2 init contribution")
		}
		if err := data.Settings.Validate(); err != nil {
			return err
		}
	}
	if data.Launcher != nil {
		if !response.OK || request.Op != "init" || manifest.HostAPIVersion != 2 || manifest.UIVersion != 2 || !contains(manifest.Contributes, "launcher") {
			return errors.New("launcher requires a declared v2 init contribution")
		}
		if err := data.Launcher.Validate(); err != nil {
			return err
		}
	}
	if err := checkProtocol(manifest, grants, request, data); err != nil {
		return err
	}
	if !response.OK {
		if response.Error == nil || response.Error.Code == "" || response.Error.Message == "" {
			return errors.New("unsuccessful response must contain an error code and message")
		}
		if len(data.Actions) != 0 || len(data.State) != 0 {
			return errors.New("unsuccessful response must not contain effects")
		}
		return nil
	}
	if response.Error != nil {
		return errors.New("successful response must not contain an error")
	}
	if request.Op == "init" {
		if len(data.Actions) != 0 {
			return errors.New("init can register contributions and initial state")
		}
		if err := validateContributions(manifest, grants, data); err != nil {
			return err
		}
		init = data
	} else if data.Search || len(data.Commands) != 0 || len(data.Widgets) != 0 || len(data.AppTabs) != 0 || len(data.Subscribe) != 0 {
		return errors.New("only init can register contributions")
	}
	if request.Op == "search" {
		if err := validateResults(data.Results, request.Payload.Limit, init.Commands); err != nil {
			return err
		}
	} else if len(data.Results) != 0 {
		return errors.New("only search can return results")
	}
	viewRequest := request.Op == "widget_state" || request.Op == "tab_state"
	if pageQuery {
		if data.View != nil || len(data.PageState) == 0 {
			return errors.New("HTML app tab requires a pageState snapshot")
		}
	} else if viewRequest {
		if data.View != nil && data.View.UIVersion == 2 && manifest.UIVersion != 2 {
			return errors.New("v2 view requires a uiVersion 2 manifest")
		}
		if err := validateView(data.View); err != nil {
			return err
		}
		if request.Op == "tab_state" {
			if err := validateTabControls(data.View); err != nil {
				return err
			}
		}
	} else if data.View != nil {
		return errors.New("only widget_state and tab_state can return a view")
	}
	if request.Op == "search" || viewRequest || request.Op == "stop" {
		if len(data.Actions) != 0 || len(data.State) != 0 {
			return errors.New("read-only operation cannot return effects")
		}
		return nil
	}
	return validateActions(data.Actions, request, init.Widgets, grants)
}

func validateContributions(manifest v1.Manifest, grants []string, data v1.Data) error {
	if (data.Search && !contains(manifest.Contributes, "search") && !contains(manifest.Contributes, "quailcrate.search.provider")) ||
		(len(data.Commands) != 0 && !contains(manifest.Contributes, "commands")) ||
		(len(data.Widgets) != 0 && !contains(manifest.Contributes, "widgets")) ||
		(len(data.AppTabs) != 0 && !contains(manifest.Contributes, "appTabs")) {
		return errors.New("init registered an undeclared contribution")
	}
	if len(data.Commands) > 100 || len(data.Widgets) > 100 || len(data.AppTabs) > 100 {
		return errors.New("too many contributions")
	}
	ids := make(map[string]bool)
	for _, command := range data.Commands {
		if !validLocalID(command.ID) || ids[command.ID] || command.Title == "" || !contains([]string{"", "desktop-edit", "settings", "search"}, command.Location) {
			return fmt.Errorf("invalid or duplicate command %q", command.ID)
		}
		ids[command.ID] = true
	}
	clear(ids)
	for _, tab := range data.AppTabs {
		if err := validatePageDefinition(manifest, tab); err != nil {
			return err
		}
		if !validLocalID(tab.ID) || ids[tab.ID] || strings.TrimSpace(tab.Title) == "" || len(tab.Title) > 256 || strings.ContainsAny(tab.Title, "\r\n\x00") {
			return fmt.Errorf("invalid or duplicate app tab %q", tab.ID)
		}
		ids[tab.ID] = true
	}
	clear(ids)
	for _, widget := range data.Widgets {
		if !validLocalID(widget.ID) || ids[widget.ID] || widget.Title == "" || widget.Width < 1 || widget.Width > 4096 || widget.Height < 1 || widget.Height > 4096 || !contains([]string{"", "left", "right"}, widget.Anchor) {
			return fmt.Errorf("invalid or duplicate widget %q", widget.ID)
		}
		ids[widget.ID] = true
	}
	if err := validateList(data.Subscribe, "event subscription", eventTypes); err != nil {
		return err
	}
	if contains(data.Subscribe, "apps.changed") && !contains(grants, "apps.read") {
		return apiError("permission_denied", "apps.read is required for apps.changed")
	}
	return nil
}

func validateResults(results []v1.Result, limit int, commands []v1.Command) error {
	if len(results) > limit {
		return errors.New("search results exceed requested limit")
	}
	ids := make(map[string]bool)
	for _, result := range results {
		if result.ID == "" || ids[result.ID] || result.Title == "" || math.IsNaN(result.Score) || math.IsInf(result.Score, 0) || !hasCommand(commands, result.Command.ID) {
			return fmt.Errorf("invalid or duplicate search result %q", result.ID)
		}
		ids[result.ID] = true
	}
	return nil
}

func validateView(view *v1.View) error {
	if view == nil {
		return errors.New("view request must return a view")
	}
	if view.UIVersion == 2 {
		if len(view.Controls) != 0 || view.Root == nil || view.Progress != 0 {
			return errors.New("UI v2 expects a root and progress nodes")
		}
		if err := view.Root.ValidateView(); err != nil {
			return err
		}
	} else if view.Root != nil || view.UIVersion != 0 && view.UIVersion != 1 {
		return fmt.Errorf("unsupported view version %d", view.UIVersion)
	}
	if len(view.Controls) > 128 {
		return errors.New("too many view controls")
	}
	ids := make(map[string]bool)
	for _, control := range view.Controls {
		if !validLocalID(control.ID) || ids[control.ID] || !contains([]string{"text", "button", "list", "grid"}, control.Type) {
			return fmt.Errorf("invalid or duplicate control %q", control.ID)
		}
		ids[control.ID] = true
		if control.Type == "grid" && (control.Columns < 1 || control.Columns > 32) || len(control.Items) > 2048 {
			return fmt.Errorf("invalid dimensions for control %q", control.ID)
		}
		items := make(map[string]bool)
		for _, item := range control.Items {
			if item.ID == "" || items[item.ID] {
				return fmt.Errorf("invalid or duplicate item %q", item.ID)
			}
			items[item.ID] = true
		}
	}
	if len(view.Tabs) > 16 {
		return errors.New("too many widget tabs")
	}
	tabIDs := make(map[string]bool)
	for _, tab := range view.Tabs {
		if !validLocalID(tab.ID) || tabIDs[tab.ID] || strings.TrimSpace(tab.Title) == "" || len(tab.Title) > 128 {
			return fmt.Errorf("invalid or duplicate widget tab %q", tab.ID)
		}
		tabIDs[tab.ID] = true
	}
	if len(view.Menu) > 48 {
		return errors.New("too many widget menu items")
	}
	menuIDs := make(map[string]bool)
	for _, entry := range view.Menu {
		if entry.Type == "separator" {
			continue
		}
		if !validLocalID(entry.ID) || menuIDs[entry.ID] || entry.Text == "" || (entry.Type != "" && entry.Type != "button") {
			return fmt.Errorf("invalid or duplicate menu item %q", entry.ID)
		}
		menuIDs[entry.ID] = true
	}
	view.PrepareLayout()
	return nil
}

func validateTabControls(view *v1.View) error {
	if len(view.Menu) != 0 || len(view.Tabs) != 0 {
		return errors.New("app tabs do not support widget menus or widget tabs")
	}
	if len(view.LayoutTree().Header) != 0 {
		return errors.New("app tabs do not support widget header placement")
	}
	for _, control := range view.DisplayControls() {
		if control.Drop || control.Reorder || control.ItemOnly || control.HoverOnly || control.Danger || control.Placement != "" || control.Menu != "" {
			return fmt.Errorf("app tab control %q uses widget-specific options", control.ID)
		}
	}
	return nil
}

func validateActions(actions []v1.Action, request v1.Request, widgets []v1.WidgetDefinition, grants []string) error {
	if len(actions) > 100 {
		return errors.New("too many plugin actions")
	}
	ids := make(map[string]bool)
	for _, action := range actions {
		if action.ID != "" {
			if !validLocalID(action.ID) || ids[action.ID] {
				return fmt.Errorf("invalid or duplicate action id %q", action.ID)
			}
			ids[action.ID] = true
		}
		if permission := ActionPermission(action.Type); permission != "" && !contains(grants, permission) {
			return apiError("permission_denied", fmt.Sprintf("%s is required for %s", permission, action.Type))
		}
		if _, ok := v2.ToolGrant(action.Type); ok {
			args, err := v2.ParseToolArgs(action.Type, action.Args)
			if err != nil {
				return err
			}
			if action.ID == "" {
				return errors.New("tool actions require an action ID")
			}
			if args.Scope == "page" && request.Payload.TabID == "" {
				return errors.New("page scope requires a tab")
			}
			if args.Root != "" && args.Root != "private" && !contains(grants, "files.directory") {
				return apiError("permission_denied", "directory access requires files.directory")
			}
			if action.Type == "files.pickDirectory" && !canPrompt(request) {
				return errors.New("directory selection requires a user interaction")
			}
			continue
		}
		switch action.Type {
		case "service.call", "event.publish":
			// The manifest, method grant and bounded JSON are checked together in checkProtocol.
		case "widget.create":
			if !validLocalID(action.WidgetID) || !hasWidget(widgets, action.WidgetType) {
				return errors.New("cannot create an invalid or unregistered widget")
			}
		case "widget.delete", "widget.refresh":
			if action.WidgetID != "" && !validLocalID(action.WidgetID) {
				return fmt.Errorf("invalid widget instance %q", action.WidgetID)
			}
			if len(widgets) == 0 || action.Type == "widget.delete" && action.WidgetID == "" {
				return errors.New("widget action requires a registered widget")
			}
		case "ui.promptText", "ui.pickPaths", "ui.confirm", "ui.pickOptions":
			if action.ID == "" || (!canPrompt(request) && !(request.Op == "service" && action.Type == "ui.pickOptions")) {
				return errors.New("prompt requires an action ID and a user interaction")
			}
		case "ui.message":
			if (!canPrompt(request) && request.Op != "service") || action.Value == "" {
				return errors.New("message requires text and a user interaction")
			}
		case "apps.launch":
			if action.AppID == "" {
				return errors.New("apps.launch requires appId")
			}
		case "paths.open", "paths.reveal", "wallpaper.set":
			if action.Path == "" {
				return fmt.Errorf("%s requires path", action.Type)
			}
		case "paths.openWith":
			if action.Path == "" || (action.ToolID == "" && action.ToolPath == "") {
				return errors.New("paths.openWith requires path and toolId or toolPath")
			}
		case "urls.open":
			if action.URL == "" {
				return errors.New("urls.open requires url")
			}
		case "icons.refresh":
			if action.IconKey == "" && action.URL == "" {
				return errors.New("icons.refresh requires iconKey or url")
			}
		default:
			return fmt.Errorf("unknown action %q", action.Type)
		}
	}
	return nil
}

// ActionPermission is the grant required for a capability action.
func ActionPermission(action string) string {
	if grant, ok := v2.ToolGrant(action); ok {
		return grant
	}
	switch action {
	case "apps.launch", "paths.open", "urls.open", "wallpaper.set":
		return action
	case "paths.reveal", "paths.openWith":
		return "paths.open"
	case "icons.refresh":
		return "urls.open"
	default:
		return ""
	}
}

func canPrompt(request v1.Request) bool {
	if request.Op == "command" {
		return true
	}
	if request.Op != "event" {
		return false
	}
	event := request.Payload.Event
	for event != nil && event.Type == "action.result" {
		if event.Status != "" {
			return false
		}
		if event.Previous == nil {
			return true
		}
		event = event.Previous
	}
	return event != nil && (strings.HasPrefix(event.Type, "widget.") || event.Type == "tab.action" || event.Type == "service.result" || (event.Type == "page.message" && event.PageUserGesture))
}

func toolEvent(name string) bool {
	switch name {
	case "audio.stateChanged", "audio.ended", "audio.error", "timer.fired":
		return true
	}
	return false
}

func hasAppTab(tabs []v1.AppTabDefinition, id string) bool {
	return slices.ContainsFunc(tabs, func(tab v1.AppTabDefinition) bool { return tab.ID == id })
}

func hasWidget(widgets []v1.WidgetDefinition, id string) bool {
	for _, widget := range widgets {
		if widget.ID == id {
			return true
		}
	}
	return false
}

func hasCommand(commands []v1.Command, id string) bool {
	for _, command := range commands {
		if command.ID == id {
			return true
		}
	}
	return false
}

func validLocalID(id string) bool {
	return len(id) <= 128 && pluginIDPattern.MatchString(id)
}

func apiError(code, message string) error {
	return &v1.APIError{Code: code, Message: message}
}
