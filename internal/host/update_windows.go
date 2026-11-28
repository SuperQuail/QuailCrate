package host

import (
	"context"
	"sync/atomic"

	"quailcrate/internal/update"
)

type hostUpdates struct {
	service  *update.Service
	launch   func(update.Prepared, bool) error
	trayExit atomic.Bool
	restart  atomic.Bool
}

func (h *Host) ConfigureUpdates(config update.Config, launch func(update.Prepared, bool) error) {
	h.releaseVersion = config.Version
	service := update.New(h.ctx, config)
	if code, err := update.ConsumeInstallFailure(config.CacheDir); err != nil {
		h.log.Warn("previous update", "code", code, "error", err)
		if code != "" {
			service.ReportFailure(code)
		}
	}
	h.updates.service = service
	h.updates.launch = launch
}

func (h *Host) UpdateSnapshot() update.Snapshot {
	if h.updates.service == nil {
		return update.Snapshot{}
	}
	return h.updates.service.Snapshot()
}

func (h *Host) SubscribeUpdates(deliver func(update.Snapshot)) func() {
	if h.updates.service == nil {
		return func() {}
	}
	return h.updates.service.Subscribe(deliver)
}

func (h *Host) SetAutoUpdate(ctx context.Context, enabled bool) error {
	if h.updates.service == nil {
		return nil
	}
	return h.updates.service.SetAutoUpdate(ctx, enabled)
}

func (h *Host) CheckUpdateNow() bool {
	return h.updates.service != nil && h.updates.service.CheckNow()
}

func (h *Host) RequestUpdateRestart() bool {
	u := &h.updates
	if u.service == nil || u.launch == nil || h.ui == nil || !u.restart.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		prepared, err := u.service.PrepareForInstall(h.ctx)
		if err != nil || h.ctx.Err() != nil {
			u.restart.Store(false)
			return
		}
		if err := u.launch(prepared, true); err != nil {
			h.log.Warn("launch updater", "error", err)
			u.service.ReportFailure("install_failed")
			u.restart.Store(false)
			return
		}
		u.service.MarkInstalling()
		h.ui.Close()
	}()
	return true
}

func (h *Host) installUpdateOnExit() {
	u := &h.updates
	if u.service == nil || u.launch == nil || !u.trayExit.Load() || u.restart.Load() {
		return
	}
	if prepared, ready := u.service.ReadyForAutomaticInstall(); ready {
		if err := u.launch(prepared, false); err != nil {
			h.log.Warn("launch updater on tray exit", "error", err)
			u.service.ReportFailure("install_failed")
		}
	}
}
