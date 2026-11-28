package gioui

import (
	"time"

	"quailcrate/internal/pluginapps"
	"quailcrate/internal/theme"
	v2 "quailcrate/pluginapi/v2"
)

const (
	PageGeneral    = "general"
	PageAppearance = "appearance"
	PageDesktop    = "desktop"
	PageCommands   = "commands"
	PagePlugins    = "plugins"
)

// SettingsModel is an immutable snapshot supplied by the host.
type SettingsModel struct {
	Definition                                       *v2.Settings
	Locale, LanguagePreference, Hotkey, HotkeyStatus string
	Editing, HotkeyRegistered, HighContrast          bool
	CustomTitleBar, Maximized                        bool
	AppsExpanded                                     bool
	// Palette optionally overrides the default theme colors.
	Palette           *SettingsPalette
	Appearance        theme.Config
	AppearanceTargets []SettingsAppearanceTarget
	Plugins           []SettingsPlugin
	Commands          []SettingsCommand
	AppTabs           []pluginapps.Tab
	Update            SettingsUpdate
	Autostart         SettingsAutostart
}

// SettingsUpdate mirrors update.Snapshot plus UI-managed transient action state.
type SettingsUpdate struct {
	CurrentVersion       string
	TargetVersion        string
	Channel              string
	AutoUpdate           bool
	LastCheckedAt        time.Time
	DownloadedBytes      int64
	TotalBytes           int64
	CanInstall           bool
	ReleaseURL           string
	Phase                string
	ErrorCode            string
	LastInstallErrorCode string
	Unavailable          bool
	AutoUpdateBusy       bool
	CheckPending         bool
	RestartPending       bool
	ActionError          string
}

func (u SettingsUpdate) Supported() bool {
	return u.Channel != "" && u.Channel != "dev" && !u.Unavailable
}

func (u SettingsUpdate) Busy() bool {
	return u.CheckPending || u.RestartPending ||
		u.Phase == "checking" || u.Phase == "downloading" || u.Phase == "installing"
}

func (u SettingsUpdate) CanCheck() bool {
	return u.Supported() && !u.Busy()
}

func (u SettingsUpdate) CanRestartToUpdate() bool {
	return u.CanInstall && u.TargetVersion != "" && !u.Busy()
}

func (u SettingsUpdate) CanToggleAutoUpdate() bool {
	return u.Supported() && !u.AutoUpdateBusy && !u.RestartPending && u.Phase != "installing"
}

// SettingsAutostart mirrors autostart.Status plus UI-managed async load/save state.
type SettingsAutostart struct {
	Loaded    bool
	Loading   bool
	Saving    bool
	Available bool
	Enabled   bool
	Conflict  bool
	Error     string
}

func (a SettingsAutostart) CanToggle() bool {
	return a.Loaded && !a.Loading && !a.Saving && a.Available && !a.Conflict
}

type SettingsAppearanceTarget struct {
	ID              string
	LabelZH         string
	LabelEN         string
	SupportsOpacity bool
}

func (m *SettingsModel) hasAppsMenu() bool {
	return len(m.AppTabs) > 0 && (m.Definition == nil || m.Definition.AppsTitle != "")
}

type SettingsPlugin struct {
	ID, Name, Description, Icon, Runtime, State, Status, Detail string
	Enabled, CanToggle, Bundled, FullSystemAccess               bool
	RequestedPermissions                                        []string
	GrantedPermissions                                          []string
}

type SettingsCommand struct {
	PluginID, ID, PluginName, Title, Location string
	Generation                                uint64
}

type SettingsActionKind string

const (
	ChangeLanguage       SettingsActionKind = "language"
	ChangeHotkey         SettingsActionKind = "hotkey"
	PreviewAppearance    SettingsActionKind = "preview-appearance"
	SaveAppearance       SettingsActionKind = "save-appearance"
	ToggleEditing        SettingsActionKind = "editing"
	TogglePlugin         SettingsActionKind = "toggle"
	RevokePermission     SettingsActionKind = "revoke-permission"
	RunCommand           SettingsActionKind = "command"
	OpenAppTab           SettingsActionKind = "open-app-tab"
	ToggleAppsExpanded   SettingsActionKind = "toggle-apps-expanded"
	OpenAppsMenu         SettingsActionKind = "open-apps-menu"
	Rescan               SettingsActionKind = "rescan"
	OpenPluginDir        SettingsActionKind = "open-directory"
	CopyDiagnostics      SettingsActionKind = "copy-diagnostics"
	ToggleAutoUpdate     SettingsActionKind = "toggle-auto-update"
	CheckUpdateNow       SettingsActionKind = "check-update-now"
	RestartUpdate        SettingsActionKind = "restart-update"
	OpenUpdateRelease    SettingsActionKind = "open-update-release"
	ToggleAutostart      SettingsActionKind = "toggle-autostart"
	RefreshAutostart     SettingsActionKind = "refresh-autostart"
	WindowMinimize       SettingsActionKind = "window-minimize"
	WindowToggleMaximize SettingsActionKind = "window-toggle-maximize"
	WindowClose          SettingsActionKind = "window-close"
)

type SettingsAction struct {
	Kind                       SettingsActionKind
	PluginID, CommandID, Value string
	Generation                 uint64
	Enabled                    bool
}
