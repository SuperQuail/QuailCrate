package v2

import (
	"errors"
	"strings"
)

// TrayItem contributes a user-activated entry to the notification-area menu.
type TrayItem struct {
	ID          string `json:"id,omitempty"`
	Title       string `json:"title,omitempty"`
	ActiveTitle string `json:"activeTitle,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Intent      string `json:"intent,omitempty"`
	Command     string `json:"command,omitempty"`
	Order       int    `json:"order,omitempty"`
	Separator   bool   `json:"separator,omitempty"`
}

func (item TrayItem) Validate() error {
	if item.Order < -10000 || item.Order > 10000 {
		return errors.New("tray order is out of bounds")
	}
	if item.Separator {
		if item != (TrayItem{Separator: true, Order: item.Order}) {
			return errors.New("tray separators accept only an order")
		}
		return nil
	}
	if !ValidID(item.ID) || strings.TrimSpace(item.Title) == "" || len(item.Title) > 256 ||
		len(item.ActiveTitle) > 256 || strings.ContainsAny(item.Title+item.ActiveTitle, "\r\n\t\x00") ||
		(item.Icon != "" && !ValidID(item.Icon)) {
		return errors.New("invalid tray entry id, label or icon")
	}
	if item.Intent == "command" {
		if !ValidID(item.Command) || item.ActiveTitle != "" {
			return errors.New("tray command requires a local command id")
		}
		return nil
	}
	if item.Command != "" || (item.ActiveTitle != "" && item.Intent != "desktop.toggleEdit") {
		return errors.New("unexpected tray command or active title")
	}
	switch item.Intent {
	case "launcher.open", "launcher.choose", "desktop.toggleEdit", "settings.appearance":
		return nil
	default:
		return errors.New("unknown tray intent")
	}
}
