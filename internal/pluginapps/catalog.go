// Package pluginapps models the plugin contributions exposed by the settings
// navigation and independent app windows.
package pluginapps

import (
	"cmp"
	"slices"

	v1 "quailcrate/pluginapi/v1"
)

// Registration is the validated init data for one running plugin instance.
type Registration struct {
	PluginID, PluginName string
	Generation           uint64
	Tabs                 []v1.AppTabDefinition
	Commands             []v1.Command
}

// TabRef addresses one tab in a particular instance of the plugin.
type TabRef struct {
	PluginID, TabID string
	Generation      uint64
}

type Tab struct {
	TabRef
	PluginName, Title, Icon string
	Page                    string
}

type Command struct {
	PluginID, ID, PluginName, Title, Location string
	Generation                                uint64
}

// Snapshot contains detached data in stable plugin ID and declaration order.
// Once published it can be shared read-only. Clone gives a caller its own slices.
type Snapshot struct {
	Tabs     []Tab
	Commands []Command
}

func (s Snapshot) Clone() Snapshot {
	return Snapshot{Tabs: slices.Clone(s.Tabs), Commands: slices.Clone(s.Commands)}
}

// IsPublicCommandLocation reports whether a command belongs on the settings
// command page. The empty location is used for internal plugin commands.
func IsPublicCommandLocation(location string) bool {
	return location == "desktop-edit" || location == "settings"
}

// Build projects the current host records. Search-only and private commands
// keep their existing entry points. Commands from older plugins retain their
// original location so existing callers can still route them.
func Build(registrations []Registration) Snapshot {
	records := slices.Clone(registrations)
	slices.SortFunc(records, func(a, b Registration) int { return cmp.Compare(a.PluginID, b.PluginID) })
	var snapshot Snapshot
	for _, reg := range records {
		if reg.Generation == 0 {
			continue
		}
		for _, tab := range reg.Tabs {
			snapshot.Tabs = append(snapshot.Tabs, Tab{
				TabRef:     TabRef{PluginID: reg.PluginID, TabID: tab.ID, Generation: reg.Generation},
				PluginName: reg.PluginName, Title: tab.Title, Icon: tab.Icon, Page: tab.Page,
			})
		}
		for _, cmd := range reg.Commands {
			if !IsPublicCommandLocation(cmd.Location) {
				continue
			}
			snapshot.Commands = append(snapshot.Commands, Command{
				PluginID: reg.PluginID, ID: cmd.ID, PluginName: reg.PluginName,
				Title: cmd.Title, Location: cmd.Location, Generation: reg.Generation,
			})
		}
	}
	return snapshot
}

func (s Snapshot) ResolveTab(pluginID, tabID string) (Tab, bool) {
	for _, tab := range s.Tabs {
		if tab.PluginID == pluginID && tab.TabID == tabID {
			return tab, true
		}
	}
	return Tab{}, false
}

func (s Snapshot) ResolveCommand(pluginID, commandID string) (Command, bool) {
	for _, cmd := range s.Commands {
		if cmd.PluginID == pluginID && cmd.ID == commandID {
			return cmd, true
		}
	}
	return Command{}, false
}
