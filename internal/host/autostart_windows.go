package host

import "quailcrate/internal/autostart"

type hostAutostart struct {
	controller *autostart.Controller
	err        error
}

// ConfigureAutostart sets the launch identity before the host opens its UI.
func (h *Host) ConfigureAutostart(executable, workingDir, dataDir string) {
	controller, err := autostart.New(executable, workingDir, dataDir)
	h.autostart = hostAutostart{controller: controller, err: err}
}

func (h *Host) AutostartStatus() (autostart.Status, error) {
	if h.autostart.controller == nil {
		return autostart.Status{}, h.autostart.err
	}
	return h.autostart.controller.Status()
}

func (h *Host) SetAutostart(enabled bool) error {
	if h.autostart.controller == nil {
		return autostart.ErrUnavailable
	}
	return h.autostart.controller.SetEnabled(enabled)
}
