package v2

import (
	"fmt"
	"strings"
)

// Settings describes a workbench built from host-owned interactive controls.
type Settings struct {
	UIVersion int               `json:"uiVersion"`
	Title     string            `json:"title"`
	AppsTitle string            `json:"appsTitle,omitempty"`
	Sections  []SettingsSection `json:"sections"`
}
type SettingsSection struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Icon        string         `json:"icon,omitempty"`
	Cards       []SettingsCard `json:"cards"`
}
type SettingsCard struct {
	Type  string `json:"type"`
	Title string `json:"title"`
}

func (s *Settings) Section(id string) *SettingsSection {
	if s != nil {
		for i := range s.Sections {
			if s.Sections[i].ID == id {
				return &s.Sections[i]
			}
		}
	}
	return nil
}

func (s *Settings) Validate() error {
	label := func(value string) bool {
		return strings.TrimSpace(value) != "" && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n")
	}
	if s.UIVersion != 2 || !label(s.Title) || s.AppsTitle != "" && !label(s.AppsTitle) || len(s.Sections) < 1 || len(s.Sections) > 16 {
		return fmt.Errorf("settings requires uiVersion 2, a title and 1..16 sections")
	}
	sections := make(map[string]bool)
	cards := make(map[string]bool)
	for _, section := range s.Sections {
		if !ValidID(section.ID) || sections[section.ID] || !label(section.Title) || len(section.Description) > 512 || strings.ContainsAny(section.Description, "\x00\r\n") || section.Icon != "" && !ValidID(section.Icon) || len(section.Cards) == 0 {
			return fmt.Errorf("invalid settings section %q", section.ID)
		}
		sections[section.ID] = true
		for _, card := range section.Cards {
			switch card.Type {
			case "hotkey", "language", "target", "presets", "opacity", "radius", "iconSize", "colors", "desktopLayout", "commands", "plugins":
			default:
				return fmt.Errorf("unknown settings control %q", card.Type)
			}
			if cards[card.Type] || !label(card.Title) {
				return fmt.Errorf("duplicate or unnamed settings control %q", card.Type)
			}
			cards[card.Type] = true
		}
	}
	return nil
}
