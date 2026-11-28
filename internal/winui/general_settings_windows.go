package winui

import (
	"context"
	"errors"
	"strings"
	"time"

	"quailcrate/internal/autostart"
	"quailcrate/internal/gioui"
	"quailcrate/internal/update"
	"quailcrate/internal/winshell"
)

func (u *UI) backgroundContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	if stop := u.stopCh; stop != nil {
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}

func (u *UI) startUpdateSubscription() {
	if u.updateSubscribed {
		return
	}
	if u.cb.UpdateSnapshot != nil {
		u.updateSnapshot = u.cb.UpdateSnapshot()
	}
	if u.cb.SubscribeUpdates != nil {
		u.updateSubscribed = true
		u.updateUnsubscribe = u.cb.SubscribeUpdates(func(snap update.Snapshot) {
			u.post(func() {
				u.applyUpdateSnapshot(snap)
			})
		})
	}
}

func (u *UI) stopUpdateSubscription() {
	if u.updateUnsubscribe != nil {
		unsub := u.updateUnsubscribe
		u.updateUnsubscribe = nil
		u.updateSubscribed = false
		unsub()
	}
}

func (u *UI) applyUpdateSnapshot(snap update.Snapshot) {
	u.updateSnapshot = snap
	if u.updateCheckPending {
		if snap.Phase == update.PhaseChecking ||
			snap.Phase == update.PhaseDownloading ||
			snap.Phase == update.PhaseInstalling ||
			snap.Phase == update.PhaseFailed ||
			snap.Phase == update.PhaseAvailable ||
			snap.Phase == update.PhaseReady ||
			!snap.LastCheckedAt.Equal(u.updateCheckBefore) {
			u.updateCheckPending = false
		}
	}
	if u.updateRestartPending {
		if snap.Phase == update.PhaseFailed ||
			snap.Phase == update.PhaseIdle ||
			(snap.Phase == update.PhaseAvailable && !snap.CanInstall) {
			u.updateRestartPending = false
		}
	}
	if snap.Phase == update.PhaseChecking ||
		snap.Phase == update.PhaseDownloading ||
		snap.Phase == update.PhaseReady ||
		snap.Phase == update.PhaseInstalling {
		u.updateActionError = ""
		u.updateUnavailable = false
	}
	u.refreshSettings()
}

func (u *UI) settingsUpdateModel() gioui.SettingsUpdate {
	s := u.updateSnapshot
	return gioui.SettingsUpdate{
		CurrentVersion:       s.CurrentVersion,
		TargetVersion:        s.TargetVersion,
		Channel:              s.Channel,
		AutoUpdate:           s.AutoUpdate,
		LastCheckedAt:        s.LastCheckedAt,
		DownloadedBytes:      s.DownloadedBytes,
		TotalBytes:           s.TotalBytes,
		CanInstall:           s.CanInstall,
		ReleaseURL:           s.ReleaseURL,
		Phase:                s.Phase,
		ErrorCode:            s.ErrorCode,
		LastInstallErrorCode: s.LastInstallErrorCode,
		Unavailable:          u.updateUnavailable,
		AutoUpdateBusy:       u.updateAutoBusy,
		CheckPending:         u.updateCheckPending,
		RestartPending:       u.updateRestartPending,
		ActionError:          u.updateActionError,
	}
}

func (u *UI) setAutoUpdate(enabled bool) {
	if u.cb.SetAutoUpdate == nil || !u.settingsUpdateModel().CanToggleAutoUpdate() {
		return
	}
	u.updateAutoBusy = true
	u.updateActionError = ""
	u.refreshSettings()
	setFn, snapFn := u.cb.SetAutoUpdate, u.cb.UpdateSnapshot
	go func() {
		ctx, cancel := u.backgroundContext(10 * time.Second)
		defer cancel()
		err := setFn(ctx, enabled)
		var fresh *update.Snapshot
		if snapFn != nil {
			s := snapFn()
			fresh = &s
		}
		u.post(func() {
			u.updateAutoBusy = false
			if fresh != nil {
				if !u.updateSubscribed {
					u.updateSnapshot = *fresh
				} else if err != nil {
					u.updateSnapshot.AutoUpdate = fresh.AutoUpdate
				}
			}
			if err != nil {
				if errors.Is(err, update.ErrUnavailable) {
					u.updateUnavailable = true
					u.updateActionError = "unavailable"
				} else {
					u.updateActionError = update.ErrorSettings
				}
			} else {
				u.updateActionError = ""
			}
			u.refreshSettings()
		})
	}()
}

func (u *UI) checkUpdateNow() {
	if u.cb.CheckUpdateNow == nil || !u.settingsUpdateModel().CanCheck() {
		return
	}
	u.updateActionError = ""
	before := u.updateSnapshot.LastCheckedAt
	if u.cb.CheckUpdateNow() {
		u.updateCheckPending = true
		u.updateCheckBefore = before
		u.refreshSettings()
		return
	}
	if u.cb.UpdateSnapshot != nil {
		u.updateSnapshot = u.cb.UpdateSnapshot()
	}
	if !u.settingsUpdateModel().Busy() {
		u.updateActionError = "unavailable"
	}
	u.refreshSettings()
}

func (u *UI) requestUpdateRestart() {
	if u.cb.RequestUpdateRestart == nil || !u.settingsUpdateModel().CanRestartToUpdate() {
		return
	}
	u.updateActionError = ""
	if u.cb.RequestUpdateRestart() {
		u.updateRestartPending = true
		u.refreshSettings()
		return
	}
	if u.cb.UpdateSnapshot != nil {
		u.updateSnapshot = u.cb.UpdateSnapshot()
	}
	if !u.settingsUpdateModel().Busy() {
		u.updateActionError = "install_failed"
	}
	u.refreshSettings()
}

func (u *UI) openUpdateRelease(rawURL string) {
	target := strings.TrimSpace(rawURL)
	if target == "" || target != u.updateSnapshot.ReleaseURL {
		return
	}
	opener := u.openURL
	if opener == nil {
		opener = winshell.OpenURL
	}
	errTitle := u.label("无法打开发布页", "Could not open release page")
	go func() {
		if err := opener(target); err != nil {
			u.Message(errTitle, err.Error())
		}
	}()
}

func (u *UI) settingsAutostartModel() gioui.SettingsAutostart {
	return gioui.SettingsAutostart{
		Loaded:    u.autostartLoaded,
		Loading:   u.autostartLoading,
		Saving:    u.autostartSaving,
		Available: u.autostartStatus.Available,
		Enabled:   u.autostartStatus.Enabled,
		Conflict:  u.autostartStatus.Conflict,
		Error:     u.autostartError,
	}
}

func (u *UI) refreshAutostart() {
	if u.cb.AutostartStatus == nil {
		u.autostartLoaded = true
		u.autostartLoading = false
		u.autostartStatus = autostart.Status{}
		u.autostartError = ""
		u.refreshSettings()
		return
	}
	if u.autostartSaving || u.autostartLoading {
		return
	}
	u.autostartLoading = true
	u.autostartSeq++
	seq := u.autostartSeq
	readFn := u.cb.AutostartStatus
	u.refreshSettings()
	go func() {
		status, err := readFn()
		u.post(func() {
			if seq != u.autostartSeq {
				return
			}
			u.autostartLoading = false
			u.autostartLoaded = true
			u.autostartStatus = status
			u.autostartError = classifyAutostartReadError(err)
			u.refreshSettings()
		})
	}()
}

func (u *UI) setAutostart(enabled bool) {
	if u.cb.SetAutostart == nil || u.cb.AutostartStatus == nil || !u.settingsAutostartModel().CanToggle() {
		return
	}
	u.autostartSaving = true
	u.autostartError = ""
	u.autostartSeq++
	seq := u.autostartSeq
	setFn, readFn := u.cb.SetAutostart, u.cb.AutostartStatus
	u.refreshSettings()
	go func() {
		setErr := setFn(enabled)
		status, readErr := readFn()
		u.post(func() {
			if seq != u.autostartSeq {
				return
			}
			u.autostartSaving = false
			u.autostartLoaded = true
			u.autostartStatus = status
			u.autostartError = classifyAutostartWriteError(setErr, readErr)
			u.refreshSettings()
		})
	}()
}

func classifyAutostartReadError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, autostart.ErrConflict):
		return "conflict"
	case errors.Is(err, autostart.ErrUnavailable):
		return "unavailable"
	default:
		return "read_failed"
	}
}

func classifyAutostartWriteError(setErr, readErr error) string {
	if setErr != nil {
		switch {
		case errors.Is(setErr, autostart.ErrConflict):
			return "conflict"
		case errors.Is(setErr, autostart.ErrUnavailable):
			return "unavailable"
		default:
			return "write_failed"
		}
	}
	return classifyAutostartReadError(readErr)
}
