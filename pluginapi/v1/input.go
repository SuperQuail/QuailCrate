package v1

import (
	"encoding/json"

	v2 "quailcrate/pluginapi/v2"
)

// AllowsInput checks a typed submission against its displayed control binding.
func (view *View) AllowsInput(action string, value json.RawMessage) bool {
	if view == nil || view.UIVersion != 2 {
		return false
	}
	kind := v2.InputValueType(value)
	if kind == "" {
		return false
	}
	for _, c := range view.DisplayControls() {
		if c.Type == kind && c.Action == action {
			return true
		}
	}
	return false
}
