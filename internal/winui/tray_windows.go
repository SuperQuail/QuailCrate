package winui

import (
	"cmp"
	"slices"
	"strconv"

	"quailcrate/internal/gioui"
	apptheme "quailcrate/internal/theme"
	v2 "quailcrate/pluginapi/v2"
)

type TrayEntry struct {
	PluginID   string
	Generation uint64
	Item       v2.TrayItem
}

func (u *UI) SetTray(entries []TrayEntry) {
	slices.SortStableFunc(entries, func(a, b TrayEntry) int {
		return cmp.Or(cmp.Compare(a.Item.Order, b.Item.Order), cmp.Compare(a.PluginID, b.PluginID))
	})
	u.post(func() {
		if slices.Equal(u.tray, entries) {
			return
		}
		u.tray = entries
		if u.menu != nil && u.menu.tray {
			u.closeContextMenu("")
		}
	})
}

func (u *UI) trayItems() ([]gioui.MenuItem, map[string]TrayEntry) {
	var items []gioui.MenuItem
	entries := make(map[string]TrayEntry)
	for _, entry := range u.tray {
		item := entry.Item
		if item.Separator {
			if len(items) > 0 && !items[len(items)-1].Separator {
				items = append(items, gioui.MenuItem{Separator: true})
			}
			continue
		}
		if item.Intent == "launcher.choose" && len(u.launcherShells) < 2 {
			continue
		}
		id := strconv.Itoa(len(items))
		row := gioui.MenuItem{ID: id, Label: item.Title, Icon: item.Icon}
		switch item.Intent {
		case "launcher.open":
			row.Shortcut = u.hotkey
		case "desktop.toggleEdit":
			row.Checked = u.editing
			if row.Checked && item.ActiveTitle != "" {
				row.Label = item.ActiveTitle
			}
		}
		items = append(items, row)
		entries[id] = entry
	}
	if len(items) > 0 && !items[len(items)-1].Separator {
		items = append(items, gioui.MenuItem{Separator: true})
	}
	items = append(items,
		gioui.MenuItem{ID: "settings", Label: u.label("全部设置", "Settings"), Icon: "settings"},
		gioui.MenuItem{ID: "exit", Label: u.label("退出", "Exit"), Icon: "power", Danger: true})
	return items, entries
}

func (u *UI) openTrayMenu() {
	items, entries := u.trayItems()
	pal := u.appearance.Resolve(apptheme.ComponentGlobal).Palette
	u.showContextMenu(cursorPosition(), pal, items, func(id string) {
		switch id {
		case "settings":
			u.showSettings()
		case "exit":
			postQuitMessage.Call(0)
		default:
			entry, ok := entries[id]
			if !ok || u.cb.ActivateTray == nil || !u.cb.ActivateTray(entry) {
				return
			}
			switch entry.Item.Intent {
			case "launcher.open":
				u.showLauncher()
			case "launcher.choose":
				u.pickLauncher()
			case "desktop.toggleEdit":
				u.setEditing(!u.editing)
				if u.editing {
					u.setSettingsPageID(gioui.PageDesktop)
					u.showSettings()
				}
			case "settings.appearance":
				u.showAppearanceSettings()
			}
		}
	})
	if u.menu != nil {
		u.menu.tray = true
	}
}
