package winui

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"quailcrate/internal/autostart"
	"quailcrate/internal/gioui"
	"quailcrate/internal/update"
	v2 "quailcrate/pluginapi/v2"
)

func drainUIQueue(t *testing.T, u *UI, wait func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		u.hostProcedure(wmDispatch, 0, 0)
		if wait == nil || wait() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for queued UI state")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWithHostSectionsKeepsGeneralAndPluginsWhenWorkbenchDisabled(t *testing.T) {
	u := New(Callbacks{})
	u.locale = "zh-CN"

	// 1. Workbench disabled (def == nil): host provides General (autostart + updates) and Plugins recovery.
	u.SetSettingsDefinition(nil)
	drainUIQueue(t, u, nil)
	def := u.settingsDefinition
	if def == nil {
		t.Fatal("expected host-managed settings definition when Workbench is disabled")
	}
	gen := def.Section(gioui.PageGeneral)
	if gen == nil || len(gen.Cards) != 2 || gen.Cards[0].Type != "autostart" || gen.Cards[1].Type != "updates" {
		t.Fatalf("host general section when Workbench disabled = %+v", gen)
	}
	if def.Section(gioui.PagePlugins) == nil {
		t.Fatal("missing host plugin recovery section when Workbench is disabled")
	}

	// Switching language refreshes localized host-managed section titles.
	u.SetLanguage("en-US", "en-US")
	drainUIQueue(t, u, nil)
	if u.settingsDefinition.Section(gioui.PageGeneral).Title != "General" ||
		u.settingsDefinition.Section(gioui.PageGeneral).Cards[0].Title != "Start on sign-in" ||
		u.settingsDefinition.Section(gioui.PageGeneral).Cards[1].Title != "App updates" {
		t.Fatalf("host general section did not relocalize: %+v", u.settingsDefinition.Section(gioui.PageGeneral))
	}

	// 2. Workbench enabled: appends autostart + updates to General without mutating source definition.
	workbenchDef := &v2.Settings{
		UIVersion: 2,
		Title:     "Settings",
		Sections: []v2.SettingsSection{
			{ID: gioui.PageGeneral, Title: "General", Cards: []v2.SettingsCard{{Type: "hotkey", Title: "Shortcut"}, {Type: "language", Title: "Language"}}},
			{ID: gioui.PagePlugins, Title: "Plugins", Cards: []v2.SettingsCard{{Type: "plugins", Title: "Plugins"}}},
		},
	}
	u.SetSettingsDefinition(workbenchDef)
	drainUIQueue(t, u, nil)
	if len(workbenchDef.Sections[0].Cards) != 2 {
		t.Fatal("withHostSections mutated source workbench cards")
	}
	genCards := u.settingsDefinition.Section(gioui.PageGeneral).Cards
	if len(genCards) != 4 || genCards[2].Type != "autostart" || genCards[3].Type != "updates" {
		t.Fatalf("workbench general cards = %+v", genCards)
	}

	// 3. Replacement workbench omitting General and Plugins still gets both host-managed sections.
	customDef := &v2.Settings{
		UIVersion: 2,
		Title:     "Custom",
		Sections:  []v2.SettingsSection{{ID: "custom", Title: "Custom", Cards: []v2.SettingsCard{{Type: "desktopLayout", Title: "Desktop"}}}},
	}
	u.SetSettingsDefinition(customDef)
	drainUIQueue(t, u, nil)
	if u.settingsDefinition.Section(gioui.PageGeneral) == nil || u.settingsDefinition.Section(gioui.PagePlugins) == nil {
		t.Fatalf("replacement workbench missing host sections: %+v", u.settingsDefinition.Sections)
	}
}

func TestWinUIUpdateSubscriptionActionsAndDuplicateGuards(t *testing.T) {
	var (
		listener      func(update.Snapshot)
		unsubscribed  atomic.Bool
		checkCalls    atomic.Int32
		restartCalls  atomic.Int32
		autoCalls     atomic.Int32
		autoEnabled   atomic.Bool
		openedURL     atomic.Value
		currentSnap   atomic.Value
	)
	initial := update.Snapshot{
		CurrentVersion: "0.1.0-alpha.1",
		Channel:        "alpha",
		AutoUpdate:     true,
		Phase:          update.PhaseIdle,
	}
	currentSnap.Store(initial)
	autoEnabled.Store(true)

	u := New(Callbacks{
		UpdateSnapshot: func() update.Snapshot { return currentSnap.Load().(update.Snapshot) },
		SubscribeUpdates: func(deliver func(update.Snapshot)) func() {
			listener = deliver
			go deliver(currentSnap.Load().(update.Snapshot))
			return func() { unsubscribed.Store(true) }
		},
		SetAutoUpdate: func(_ context.Context, enabled bool) error {
			autoCalls.Add(1)
			autoEnabled.Store(enabled)
			snap := currentSnap.Load().(update.Snapshot)
			snap.AutoUpdate = enabled
			currentSnap.Store(snap)
			listener(snap)
			return nil
		},
		CheckUpdateNow: func() bool {
			checkCalls.Add(1)
			return true
		},
		RequestUpdateRestart: func() bool {
			restartCalls.Add(1)
			return true
		},
	})
	u.openURL = func(raw string) error {
		openedURL.Store(raw)
		return nil
	}

	u.startUpdateSubscription()
	drainUIQueue(t, u, func() bool { return u.settingsModel().Update.CurrentVersion == "0.1.0-alpha.1" })

	// Manual check sets CheckPending and ignores duplicate clicks until snapshot updates.
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.CheckUpdateNow})
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.CheckUpdateNow})
	if checkCalls.Load() != 1 || !u.settingsModel().Update.CheckPending {
		t.Fatalf("check calls=%d pending=%v", checkCalls.Load(), u.settingsModel().Update.CheckPending)
	}

	// Background delivery of checking -> available unlocks check and enables restart.
	const releaseURL = "https://github.com/SuperQuail/QuailCrate/releases/tag/v0.1.0-alpha.2"
	listener(update.Snapshot{CurrentVersion: "0.1.0-alpha.1", Channel: "alpha", AutoUpdate: true, Phase: update.PhaseChecking})
	drainUIQueue(t, u, func() bool { return u.settingsModel().Update.Phase == update.PhaseChecking })
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.CheckUpdateNow})
	if checkCalls.Load() != 1 {
		t.Fatalf("expected duplicate check ignored during checking, got %d", checkCalls.Load())
	}

	avail := update.Snapshot{
		CurrentVersion: "0.1.0-alpha.1",
		TargetVersion:  "0.1.0-alpha.2",
		Channel:        "alpha",
		AutoUpdate:     true,
		CanInstall:     true,
		ReleaseURL:     releaseURL,
		Phase:          update.PhaseAvailable,
	}
	currentSnap.Store(avail)
	listener(avail)
	drainUIQueue(t, u, func() bool { return u.settingsModel().Update.Phase == update.PhaseAvailable })

	// Open release URL only allows the snapshot's ReleaseURL.
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.OpenUpdateRelease, Value: "https://example.invalid/evil"})
	time.Sleep(20 * time.Millisecond)
	if openedURL.Load() != nil {
		t.Fatalf("unexpected non-release URL opened: %v", openedURL.Load())
	}
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.OpenUpdateRelease, Value: releaseURL})
	drainUIQueue(t, u, func() bool { return openedURL.Load() == releaseURL })

	// Toggle auto-update runs asynchronously and updates snapshot.
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.ToggleAutoUpdate, Enabled: false})
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.ToggleAutoUpdate, Enabled: true})
	drainUIQueue(t, u, func() bool { return !u.settingsModel().Update.AutoUpdateBusy && !u.settingsModel().Update.AutoUpdate })
	if autoCalls.Load() != 1 || autoEnabled.Load() {
		t.Fatalf("autoCalls=%d autoEnabled=%v", autoCalls.Load(), autoEnabled.Load())
	}

	// RequestUpdateRestart blocks duplicates through downloading -> ready -> installing, and resets on failure.
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.RestartUpdate})
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.RestartUpdate})
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.CheckUpdateNow})
	if restartCalls.Load() != 1 || checkCalls.Load() != 1 || !u.settingsModel().Update.RestartPending {
		t.Fatalf("restart duplicate guard failed: restart=%d check=%d pending=%v", restartCalls.Load(), checkCalls.Load(), u.settingsModel().Update.RestartPending)
	}
	listener(update.Snapshot{CurrentVersion: "0.1.0-alpha.1", TargetVersion: "0.1.0-alpha.2", Channel: "alpha", CanInstall: true, Phase: update.PhaseDownloading, DownloadedBytes: 5, TotalBytes: 10})
	drainUIQueue(t, u, func() bool { return u.settingsModel().Update.Phase == update.PhaseDownloading })
	listener(update.Snapshot{CurrentVersion: "0.1.0-alpha.1", TargetVersion: "0.1.0-alpha.2", Channel: "alpha", CanInstall: true, Phase: update.PhaseReady, DownloadedBytes: 10, TotalBytes: 10})
	drainUIQueue(t, u, func() bool { return u.settingsModel().Update.Phase == update.PhaseReady })
	if !u.settingsModel().Update.RestartPending {
		t.Fatal("RestartPending cleared before installer launched")
	}
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.RestartUpdate})
	if restartCalls.Load() != 1 {
		t.Fatalf("duplicate restart accepted while ready-to-launch: %d", restartCalls.Load())
	}
	listener(update.Snapshot{CurrentVersion: "0.1.0-alpha.1", TargetVersion: "0.1.0-alpha.2", Channel: "alpha", CanInstall: true, Phase: update.PhaseFailed, ErrorCode: "install_failed", LastInstallErrorCode: "install_failed"})
	drainUIQueue(t, u, func() bool { return u.settingsModel().Update.Phase == update.PhaseFailed })
	if u.settingsModel().Update.RestartPending {
		t.Fatal("RestartPending did not clear after failure")
	}

	u.stopUpdateSubscription()
	if !unsubscribed.Load() {
		t.Fatal("unsubscribe callback was not invoked")
	}
}

func TestWinUIAutostartAsyncReadWriteAndSystemTruthOnFailure(t *testing.T) {
	var (
		mu         sync.Mutex
		status     = autostart.Status{Available: true, Enabled: false, Conflict: false}
		setErr     error
		readCalls  atomic.Int32
		writeCalls atomic.Int32
		gate       = make(chan struct{})
	)

	u := New(Callbacks{
		AutostartStatus: func() (autostart.Status, error) {
			readCalls.Add(1)
			mu.Lock()
			defer mu.Unlock()
			return status, nil
		},
		SetAutostart: func(enabled bool) error {
			writeCalls.Add(1)
			mu.Lock()
			g := gate
			mu.Unlock()
			<-g
			mu.Lock()
			defer mu.Unlock()
			if setErr != nil {
				return setErr
			}
			status.Enabled = enabled
			return nil
		},
	})

	// Initial background load populates Available/Enabled/Conflict.
	u.refreshAutostart()
	drainUIQueue(t, u, func() bool { return u.settingsModel().Autostart.Loaded })
	if !u.settingsModel().Autostart.Available || u.settingsModel().Autostart.Enabled || readCalls.Load() != 1 {
		t.Fatalf("initial autostart = %+v reads=%d", u.settingsModel().Autostart, readCalls.Load())
	}

	// Toggle on runs asynchronously, blocks duplicate toggles while saving, and re-reads system status after save.
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.ToggleAutostart, Enabled: true})
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.ToggleAutostart, Enabled: false})
	if !u.settingsModel().Autostart.Saving {
		t.Fatal("expected autostart Saving while write is in flight")
	}
	close(gate)
	drainUIQueue(t, u, func() bool { return !u.settingsModel().Autostart.Saving })
	if writeCalls.Load() != 1 || readCalls.Load() != 2 || !u.settingsModel().Autostart.Enabled || u.settingsModel().Autostart.Error != "" {
		t.Fatalf("after enable: model=%+v reads=%d writes=%d", u.settingsModel().Autostart, readCalls.Load(), writeCalls.Load())
	}

	// Failure (e.g. another copy took ownership -> ErrConflict) re-reads system state and disables toggle.
	mu.Lock()
	setErr = autostart.ErrConflict
	status = autostart.Status{Available: true, Enabled: false, Conflict: true}
	mu.Unlock()
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.ToggleAutostart, Enabled: false})
	drainUIQueue(t, u, func() bool { return !u.settingsModel().Autostart.Saving })
	got := u.settingsModel().Autostart
	if !got.Conflict || got.Enabled || got.Error != "conflict" || got.CanToggle() {
		t.Fatalf("after conflict: got %+v", got)
	}

	// Subsequent toggle while conflicted is ignored.
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.ToggleAutostart, Enabled: true})
	if writeCalls.Load() != 2 {
		t.Fatalf("conflicted toggle should be ignored, writes=%d", writeCalls.Load())
	}

	// Generic write failure keeps system-returned state and reports write_failed.
	mu.Lock()
	status = autostart.Status{Available: true, Enabled: true, Conflict: false}
	setErr = nil
	mu.Unlock()
	u.refreshAutostart()
	drainUIQueue(t, u, func() bool { return !u.settingsModel().Autostart.Loading && u.settingsModel().Autostart.Enabled })
	mu.Lock()
	setErr = errors.New("access denied")
	mu.Unlock()
	u.handleGioSettingsAction(gioui.SettingsAction{Kind: gioui.ToggleAutostart, Enabled: false})
	drainUIQueue(t, u, func() bool { return !u.settingsModel().Autostart.Saving })
	got = u.settingsModel().Autostart
	if !got.Enabled || got.Error != "write_failed" {
		t.Fatalf("after write failure expected system Enabled=true + write_failed, got %+v", got)
	}
}
