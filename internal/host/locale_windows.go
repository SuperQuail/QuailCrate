package host

import (
	"fmt"

	"golang.org/x/sys/windows"

	v1 "quailcrate/pluginapi/v1"
)

const languageSetting = "interface.language"

func configuredLanguage(preference string) string {
	switch preference {
	case "en-US", "zh-CN":
		return preference
	default:
		return "system"
	}
}

func effectiveLanguage(preference string) string {
	if preference != "system" {
		return v1.NormalizeLocale(preference)
	}
	getLanguage := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")
	languageID, _, _ := getLanguage.Call()
	const primaryLanguageMask = 0x3FF
	const chinese = 0x04
	if languageID&primaryLanguageMask == chinese {
		return "zh-CN"
	}
	return "en-US"
}

func text(locale, english, chinese string) string {
	if locale == "zh-CN" {
		return chinese
	}
	return english
}

func localizedStatus(locale, state string) string {
	switch state {
	case "disabled":
		return text(locale, "Disabled", "已停用")
	case "loading":
		return text(locale, "Loading", "加载中")
	case "running":
		return text(locale, "Running", "运行中")
	case "stopping":
		return text(locale, "Stopping", "停止中")
	case "authorization":
		return "Authorization required"
	case "failed":
		return "Load failed"
	default:
		return state
	}
}

func (h *Host) changeLanguage(preference string) {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	preference = configuredLanguage(preference)
	locale := effectiveLanguage(preference)
	if err := h.db.SetSetting(h.ctx, languageSetting, preference); err != nil {
		h.showError("Could not save the interface language", fmt.Errorf("Save language: %w", err))
		return
	}
	h.mu.Lock()
	changed := locale != h.locale
	h.locale = locale
	var active []*record
	if changed {
		h.relocalizing = true
		for _, rec := range h.records {
			if rec.generation != 0 {
				active = append(active, rec)
			}
		}
	}
	h.mu.Unlock()
	h.ui.SetLanguage(preference, locale)
	if changed {
		for _, rec := range active {
			h.disable(rec)
		}
		h.scanLocked()
		h.mu.Lock()
		h.relocalizing = false
		h.mu.Unlock()
		h.publish()
	}
}
