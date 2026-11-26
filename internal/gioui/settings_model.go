package gioui

import (
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
