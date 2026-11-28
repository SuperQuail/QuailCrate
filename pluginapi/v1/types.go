// Package v1 defines the public QuailCrate plugin protocol.
package v1

import (
	"encoding/json"
	"strings"

	v2 "quailcrate/pluginapi/v2"
)

const (
	Version        = 1
	SchemaVersion  = 1
	ABIVersion     = 1
	ABIVersionV2   = 2
	MaxMessageSize = 4 << 20
	MaxResults     = 100
)

type Manifest struct {
	UIVersion      int                          `json:"uiVersion,omitempty"`
	Provides       []v2.Export                  `json:"provides,omitempty"`
	Uses           []v2.Dependency              `json:"uses,omitempty"`
	Publishes      []string                     `json:"publishes,omitempty"`
	Listens        []string                     `json:"listens,omitempty"`
	SchemaVersion  int                          `json:"schemaVersion"`
	ID             string                       `json:"id"`
	Name           string                       `json:"name"`
	Description    string                       `json:"description,omitempty"`
	Icon           string                       `json:"icon,omitempty"`
	Version        string                       `json:"version"`
	Homepage       string                       `json:"homepage,omitempty"`
	MinHostVersion string                       `json:"minHostVersion,omitempty"`
	Runtime        string                       `json:"runtime"`
	Entry          string                       `json:"entry"`
	HostAPIVersion int                          `json:"hostApiVersion"`
	Platform       string                       `json:"platform,omitempty"`
	ABIVersion     int                          `json:"abiVersion,omitempty"`
	Contributes    []string                     `json:"contributes"`
	Permissions    []string                     `json:"permissions"`
	Localizations  map[string]LocalizedManifest `json:"localizations,omitempty"`
}

type LocalizedManifest struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// NormalizeLocale returns the supported UI locale for a preference.
func NormalizeLocale(locale string) string {
	if strings.EqualFold(strings.TrimSpace(locale), "zh-CN") {
		return "zh-CN"
	}
	return "en-US"
}

// DisplayName returns the name in the chosen UI locale, with the manifest name as fallback.
func (manifest Manifest) DisplayName(locale string) string {
	if localized := manifest.Localizations[NormalizeLocale(locale)].Name; localized != "" {
		return localized
	}
	return manifest.Name
}

// DisplayDescription returns the localized description with the manifest description as fallback.
func (manifest Manifest) DisplayDescription(locale string) string {
	if localized := manifest.Localizations[NormalizeLocale(locale)].Description; localized != "" {
		return localized
	}
	return manifest.Description
}

type Request struct {
	RequestID string  `json:"requestId"`
	Op        string  `json:"op"`
	Payload   Payload `json:"payload"`
}

type Payload struct {
	ToolActions    []string          `json:"toolActions,omitempty"`
	Services       []v2.Available    `json:"services,omitempty"`
	Service        string            `json:"service,omitempty"`
	Method         string            `json:"method,omitempty"`
	Caller         string            `json:"caller,omitempty"`
	ServiceVersion string            `json:"serviceVersion,omitempty"`
	HostAPIVersion int               `json:"hostApiVersion,omitempty"`
	Locale         string            `json:"locale,omitempty"`
	Strings        map[string]string `json:"strings"`
	State          json.RawMessage   `json:"state,omitempty"`
	Args           json.RawMessage   `json:"args,omitempty"`
	Text           string            `json:"text,omitempty"`
	Limit          int               `json:"limit,omitempty"`
	CommandID      string            `json:"commandId,omitempty"`
	TabID          string            `json:"tabId,omitempty"`
	WidgetID       string            `json:"widgetId,omitempty"`
	WidgetType     string            `json:"widgetType,omitempty"`
	Date           string            `json:"date,omitempty"`
	UnixTime       int64             `json:"unixTime,omitempty"`
	Apps           []App             `json:"apps,omitempty"`
	Tools          []Tool            `json:"tools,omitempty"`
	Widgets        []WidgetInstance  `json:"widgets,omitempty"`
	Event          *Event            `json:"event,omitempty"`
}

type App struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"`
	Icon   string `json:"icon,omitempty"`
}

type Tool struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type ImportedShortcut struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	CachedIcon string `json:"cachedIcon,omitempty"`
	SourcePath string `json:"sourcePath,omitempty"`
}

type WidgetInstance struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type Event struct {
	// Only the trusted window adapter can attest native input for a page message.
	PageUserGesture bool               `json:"-"`
	Generation      uint64             `json:"-"`
	ViewRevision    uint64             `json:"-"`
	Route           []string           `json:"-"`
	Source          string             `json:"source,omitempty"`
	Topic           string             `json:"topic,omitempty"`
	Revision        uint64             `json:"revision,omitempty"`
	Status          string             `json:"status,omitempty"`
	Detail          json.RawMessage    `json:"detail,omitempty"`
	Type            string             `json:"type"`
	TabID           string             `json:"tabId,omitempty"`
	WidgetID        string             `json:"widgetId,omitempty"`
	TargetWidgetID  string             `json:"targetWidgetId,omitempty"`
	TargetTabID     string             `json:"targetTabId,omitempty"`
	ControlID       string             `json:"controlId,omitempty"`
	InputValue      json.RawMessage    `json:"inputValue,omitempty"`
	ItemID          string             `json:"itemId,omitempty"`
	From            int                `json:"from"`
	To              int                `json:"to"`
	Paths           []string           `json:"paths,omitempty"`
	Urls            []string           `json:"urls,omitempty"`
	Imported        []ImportedShortcut `json:"imported,omitempty"`
	Items           []string           `json:"items,omitempty"`
	ActionID        string             `json:"actionId,omitempty"`
	Value           string             `json:"value,omitempty"`
	Canceled        bool               `json:"canceled,omitempty"`
	Code            string             `json:"code,omitempty"`
	Error           string             `json:"error,omitempty"`
	Previous        *Event             `json:"previous,omitempty"`
}

type Response struct {
	RequestID  string    `json:"requestId"`
	OK         bool      `json:"ok"`
	Data       Data      `json:"data,omitempty"`
	Error      *APIError `json:"error,omitempty"`
	Generation uint64    `json:"-"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (err *APIError) Error() string {
	return err.Code + ": " + err.Message
}

type Data struct {
	Tray          []v2.TrayItem      `json:"tray,omitempty"`
	Settings      *v2.Settings       `json:"settings,omitempty"`
	Launcher      *v2.Launcher       `json:"launcher,omitempty"`
	ResultActions []v2.ResultAction  `json:"resultActions,omitempty"`
	Reply         *v2.Reply          `json:"reply,omitempty"`
	Commands      []Command          `json:"commands,omitempty"`
	AppTabs       []AppTabDefinition `json:"appTabs,omitempty"`
	PageState     json.RawMessage    `json:"pageState,omitempty"`
	PageReply     json.RawMessage    `json:"pageReply,omitempty"`
	Search        bool               `json:"search,omitempty"`
	Widgets       []WidgetDefinition `json:"widgets,omitempty"`
	Subscribe     []string           `json:"subscribe,omitempty"`
	Results       []Result           `json:"results,omitempty"`
	View          *View              `json:"view,omitempty"`
	State         json.RawMessage    `json:"state,omitempty"`
	Actions       []Action           `json:"actions,omitempty"`
}

type Command struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Location string `json:"location,omitempty"`
}

type AppTabDefinition struct {
	Page        string   `json:"page,omitempty"`
	PageActions []string `json:"pageActions,omitempty"`
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Icon        string   `json:"icon,omitempty"`
}

type WidgetDefinition struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Default bool   `json:"default,omitempty"`
	Anchor  string `json:"anchor,omitempty"`
}

type Result struct {
	Item     *v2.ItemRef `json:"item,omitempty"`
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Subtitle string      `json:"subtitle,omitempty"`
	Icon     string      `json:"icon,omitempty"`
	Score    float64     `json:"score"`
	Command  CommandRef  `json:"command"`
}

type CommandRef struct {
	ID   string          `json:"id"`
	Args json.RawMessage `json:"args,omitempty"`
}

type WidgetTab struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`
}

type View struct {
	UIVersion int      `json:"uiVersion,omitempty"`
	Root      *v2.Node `json:"root,omitempty"`
	layout    *ViewLayout
	Title     string      `json:"title"`
	Summary   string      `json:"summary,omitempty"`
	Progress  float64     `json:"progress,omitempty"`
	Tabs      []WidgetTab `json:"tabs,omitempty"`
	ActiveTab string      `json:"activeTab,omitempty"`
	Controls  []Control   `json:"controls,omitempty"`
	Menu      []Control   `json:"menu,omitempty"`
}

// AllowsTabAction reports whether the displayed tab contains this click target.
func (view *View) AllowsTabAction(controlID, itemID string) bool {
	if view == nil {
		return false
	}
	for _, control := range view.DisplayControls() {
		if control.ActionID() != controlID {
			continue
		}
		switch control.Type {
		case "button":
			if itemID == "" && control.Menu == "" {
				return true
			}
		case "list", "grid":
			for _, item := range control.Items {
				if item.ID == itemID && itemID != "" && !item.Header() {
					return true
				}
			}
		}
	}
	return false
}

type Control struct {
	Children                 []Control `json:"-"`
	Action                   string    `json:"-"`
	Label                    string    `json:"-"`
	Variant                  string    `json:"-"`
	Menu                     string    `json:"-"`
	Gap, Grow, Width, Height int       `json:"-"`
	Value                    float64   `json:"-"`
	ID                       string    `json:"id"`
	Type                     string    `json:"type"`
	Text                     string    `json:"text,omitempty"`
	Icon                     string    `json:"icon,omitempty"`
	Placement                string    `json:"placement,omitempty"`
	Items                    []Item    `json:"items,omitempty"`
	Columns                  int       `json:"columns,omitempty"`
	Drop                     bool      `json:"drop,omitempty"`
	Reorder                  bool      `json:"reorder,omitempty"`
	ItemOnly                 bool      `json:"itemOnly,omitempty"`
	HoverOnly                bool      `json:"hoverOnly,omitempty"`
	Danger                   bool      `json:"danger,omitempty"`
	Checked                  bool      `json:"checked,omitempty"`
}

type Item struct {
	Role  string `json:"-"`
	Label string `json:"-"`
	ID    string `json:"id"`
	Text  string `json:"text"`
	Hint  string `json:"hint,omitempty"`
	Badge string `json:"badge,omitempty"`
	Icon  string `json:"icon,omitempty"`
	State string `json:"state,omitempty"`
}

type Option struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Subtitle string `json:"subtitle,omitempty"`
	Icon     string `json:"icon,omitempty"`
	Selected bool   `json:"selected,omitempty"`
}

type Action struct {
	Service          string          `json:"service,omitempty"`
	Method           string          `json:"method,omitempty"`
	Provider         string          `json:"provider,omitempty"`
	Topic            string          `json:"topic,omitempty"`
	Args             json.RawMessage `json:"args,omitempty"`
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	WidgetID         string          `json:"widgetId,omitempty"`
	WidgetType       string          `json:"widgetType,omitempty"`
	Value            string          `json:"value,omitempty"`
	Title            string          `json:"title,omitempty"`
	Body             string          `json:"body,omitempty"`
	Path             string          `json:"path,omitempty"`
	URL              string          `json:"url,omitempty"`
	AppID            string          `json:"appId,omitempty"`
	ToolID           string          `json:"toolId,omitempty"`
	ToolPath         string          `json:"toolPath,omitempty"`
	IconKey          string          `json:"iconKey,omitempty"`
	RunAsAdmin       bool            `json:"runAsAdmin,omitempty"`
	Extensions       []string        `json:"extensions,omitempty"`
	Multiple         bool            `json:"multiple,omitempty"`
	AllowDirectories bool            `json:"allowDirectories,omitempty"`
	DirectoriesOnly  bool            `json:"directoriesOnly,omitempty"`
	Options          []Option        `json:"options,omitempty"`
}

// Notification is an asynchronous message pushed by a native plugin to the host
// via quailcrate_bind_host (e.g., widget.refresh, page.notify, event.publish, state.set).
type Notification struct {
	Type     string          `json:"type"`
	WidgetID string          `json:"widgetId,omitempty"`
	TabID    string          `json:"tabId,omitempty"`
	Topic    string          `json:"topic,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
	Detail   json.RawMessage `json:"detail,omitempty"`
	State    json.RawMessage `json:"state,omitempty"`
}
