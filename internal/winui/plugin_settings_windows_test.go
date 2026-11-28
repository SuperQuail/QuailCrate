package winui

import (
	"slices"
	"testing"

	"quailcrate/internal/gioui"
)

func TestWinUIPluginInstallSnapshotAndActions(t *testing.T) {
	var calls []string
	u := New(Callbacks{
		InstallPlugin:      func() { calls = append(calls, "install") },
		UninstallPlugin:    func(id string) { calls = append(calls, "uninstall "+id) },
		OpenPluginHomepage: func(id string) { calls = append(calls, "homepage "+id) },
	})
	u.SetPlugins([]Plugin{
		{ID: "example.counter", CanUninstall: true, HomepageURL: "https://github.com/owner/counter"},
		{ID: "quailcrate.calendar", Bundled: true},
	})
	status := PluginInstall{Busy: true, Message: "Downloading…"}
	u.SetPluginInstall(status)
	drainUIQueue(t, u, nil)
	if got := u.settingsModel().PluginInstall; got != status {
		t.Fatalf("plugin installation snapshot = %+v, want %+v", got, status)
	}
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.InstallPluginFromGitHub})
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.UninstallPlugin, PluginID: "example.counter"})
	if len(calls) != 0 {
		t.Fatalf("dispatched actions while busy: %v", calls)
	}

	u.SetPluginInstall(PluginInstall{})
	drainUIQueue(t, u, nil)
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.InstallPluginFromGitHub})
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.UninstallPlugin, PluginID: "quailcrate.calendar"})
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.UninstallPlugin, PluginID: "example.counter"})
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.OpenPluginHomepage, PluginID: "quailcrate.calendar"})
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.OpenPluginHomepage, PluginID: "example.counter"})
	if want := []string{"install", "uninstall example.counter", "homepage example.counter"}; !slices.Equal(calls, want) {
		t.Fatalf("dispatched actions = %v, want %v", calls, want)
	}
}
