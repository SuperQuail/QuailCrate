package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

func ownsContract(owner, id string) bool { return validLocalID(id) && strings.HasPrefix(id, owner+".") }
func knownService(owner, id string) bool {
	return ownsContract(owner, id) || id == v2.GroupItems || id == v2.Items
}
func validMethods(methods []string) bool {
	seen := map[string]bool{}
	if len(methods) == 0 || len(methods) > 64 {
		return false
	}
	for _, m := range methods {
		if !validLocalID(m) || seen[m] {
			return false
		}
		seen[m] = true
	}
	return true
}
func validateContracts(m v1.Manifest) error {
	if len(m.Provides)+len(m.Uses)+len(m.Publishes)+len(m.Listens) > 128 {
		return errors.New("too many contracts")
	}
	if m.SchemaVersion == 1 && (m.UIVersion != 0 || len(m.Provides)+len(m.Uses)+len(m.Publishes)+len(m.Listens) > 0) {
		return errors.New("v2 contracts require schemaVersion 2")
	}
	if m.UIVersion != 0 && m.UIVersion != 2 {
		return errors.New("unsupported UI version")
	}
	seen := map[string]bool{}
	for _, p := range m.Provides {
		key := p.ID + "@" + p.Version
		if !knownService(m.ID, p.ID) || !v2.ValidVersion(p.Version) || !validMethods(p.Methods) || seen[key] {
			return fmt.Errorf("invalid service export %q", key)
		}
		seen[key] = true
	}
	clear(seen)
	for _, d := range m.Uses {
		_, err := v2.Matches("1.0.0", d.VersionRange)
		if !validLocalID(d.ID) || !validMethods(d.Methods) || err != nil || seen[d.ID] {
			return fmt.Errorf("invalid service dependency %q", d.ID)
		}
		seen[d.ID] = true
	}
	for _, topics := range [][]string{m.Publishes, m.Listens} {
		clear(seen)
		for _, topic := range topics {
			if !validLocalID(topic) || seen[topic] {
				return fmt.Errorf("invalid topic %q", topic)
			}
			seen[topic] = true
		}
	}
	for _, topic := range m.Publishes {
		if !ownsContract(m.ID, topic) {
			return fmt.Errorf("cannot publish foreign topic %q", topic)
		}
	}
	return nil
}

// RequestedGrants includes all inter-plugin authorities so dependency changes
// participate in the same persisted consent check as system capabilities.
func RequestedGrants(m v1.Manifest) []string {
	out := slices.Clone(m.Permissions)
	for _, d := range m.Uses {
		for _, method := range d.Methods {
			out = append(out, v2.Grant(d.ID, method))
		}
	}
	for _, topic := range m.Listens {
		out = append(out, "listen:"+topic)
	}
	return out
}
func provides(m v1.Manifest, id, version, method string) bool {
	for _, p := range m.Provides {
		if p.ID == id && p.Version == version && slices.Contains(p.Methods, method) {
			return true
		}
	}
	return false
}
func checkProtocol(m v1.Manifest, grants []string, req v1.Request, data v1.Data) error {
	if len(data.Tray) > 0 {
		if req.Op != "init" || m.HostAPIVersion != 2 || m.UIVersion != 2 || !slices.Contains(m.Contributes, "tray") || len(data.Tray) > 32 {
			return errors.New("invalid or undeclared tray contribution")
		}
		seen := make(map[string]bool)
		for _, item := range data.Tray {
			if err := item.Validate(); err != nil {
				return err
			}
			if item.Separator {
				continue
			}
			if seen[item.ID] || (item.Intent == "command" && !hasCommand(data.Commands, item.Command)) {
				return errors.New("duplicate tray id or unregistered tray command")
			}
			seen[item.ID] = true
		}
	}
	if m.HostAPIVersion != 2 && (req.Op == "service" || data.Reply != nil || len(data.ResultActions) > 0) {
		return errors.New("operation requires host API v2")
	}
	if req.Op == "service" && !provides(m, req.Payload.Service, req.Payload.ServiceVersion, req.Payload.Method) {
		return errors.New("unregistered service method")
	}
	if len(data.ResultActions) > 48 {
		return errors.New("too many result actions")
	}
	ids := map[string]bool{}
	for _, a := range data.ResultActions {
		if req.Op != "init" || !slices.Contains(m.Contributes, v2.ResultActions) || !validLocalID(a.ID) || ids[a.ID] || strings.TrimSpace(a.Title) == "" || len(a.Title) > 256 || !validMethods(a.Kinds) || !provides(m, a.Service, a.Version, a.Method) {
			return errors.New("invalid result menu contribution")
		}
		ids[a.ID] = true
	}
	if data.Reply != nil {
		if req.Payload.Service == v2.GroupItems && req.Payload.Method == "add" && strings.HasPrefix(req.Payload.ServiceVersion, "1.") && data.Reply.Status == "ok" {
			var result struct {
				GroupID string `json:"groupId"`
				ItemID  string `json:"itemId"`
				Added   *bool  `json:"added"`
			}
			if json.Unmarshal(data.Reply.Value, &result) != nil || result.GroupID == "" || result.ItemID == "" || result.Added == nil {
				return errors.New("groups add requires groupId, itemId and added in a successful reply")
			}
		}
		if req.Op != "service" || !slices.Contains([]string{"ok", "canceled", "error"}, data.Reply.Status) || len(data.Reply.Value) > 64<<10 || (data.Reply.Status == "error" && !validLocalID(data.Reply.Code)) {
			return errors.New("invalid service reply")
		}
		if data.Reply.Status != "ok" && (len(data.State) != 0 || len(data.Actions) != 0) {
			return errors.New("canceled or failed service reply cannot commit effects")
		}
		if slices.ContainsFunc(data.Actions, func(a v1.Action) bool { return a.Type == "ui.pickOptions" }) {
			return errors.New("a service picker must precede the terminal reply")
		}
	}
	for _, r := range data.Results {
		if r.Item == nil {
			continue
		}
		i := r.Item
		if m.HostAPIVersion != 2 || !validLocalID(i.Kind) || i.ID == "" || len(i.ID) > 2048 || !(i.Source == m.ID || (i.Source == "quailcrate.apps" && i.Kind == "app" && slices.Contains(grants, "apps.read"))) {
			return errors.New("invalid search item reference")
		}
	}
	for _, a := range data.Actions {
		if _, ok := v2.ToolGrant(a.Type); ok {
			if m.HostAPIVersion != 2 {
				return errors.New("tool actions require host API v2")
			}
			if v2.RequiresMediaNetwork(a.Type) && !slices.Contains(grants, "network.http") {
				return apiError("permission_denied", "remote playback requires network.http")
			}
		}
		switch a.Type {
		case "service.call":
			if m.HostAPIVersion != 2 || !slices.Contains(grants, v2.Grant(a.Service, a.Method)) {
				return apiError("permission_denied", "service method is not granted")
			}
			if a.ID == "" || (a.Provider != "" && !validLocalID(a.Provider)) || !validArgs(a.Args) {
				return errors.New("invalid service call")
			}
		case "event.publish":
			if !slices.Contains(m.Publishes, a.Topic) || !validArgs(a.Args) {
				return errors.New("undeclared topic or invalid event payload")
			}
		case "ui.pickOptions":
			if req.Op != "service" {
				continue
			}
			if len(a.Options) > 4096 {
				return errors.New("too many picker options")
			}
			seen := make(map[string]bool, len(a.Options))
			for _, o := range a.Options {
				if o.ID == "" || len(o.ID) > 2048 || seen[o.ID] || strings.TrimSpace(o.Text) == "" {
					return errors.New("invalid or duplicate picker option")
				}
				seen[o.ID] = true
			}
		}
	}
	return nil
}
func validArgs(data json.RawMessage) bool {
	return len(data) <= 64<<10 && (len(data) == 0 || json.Valid(data))
}

func prepareDelivery(m v1.Manifest, grants []string, req *v1.Request) error {
	p := &req.Payload
	if m.HostAPIVersion != 2 {
		p.UnixTime = 0
	} else if req.Op == "init" {
		p.ToolActions = v2.AvailableTools(grants)
	}
	if req.Op == "service" && !provides(m, p.Service, p.ServiceVersion, p.Method) {
		return errors.New("unknown service method or version")
	}
	if req.Op == "service" && strings.HasPrefix(p.ServiceVersion, "1.") && ((p.Service == v2.GroupItems && p.Method == "add") || (p.Service == v2.Items && p.Method == "open")) {
		var args struct {
			Item v2.ItemRef `json:"item"`
		}
		if json.Unmarshal(p.Args, &args) != nil || !validLocalID(args.Item.Source) || !validLocalID(args.Item.Kind) || args.Item.ID == "" || len(args.Item.ID) > 2048 || (p.Service == v2.Items && args.Item.Source != m.ID) {
			return apiError("invalid_argument", "invalid stable item reference")
		}
	}
	if e := p.Event; e != nil && e.Type == "plugin.event" && (!slices.Contains(m.Listens, e.Topic) || !slices.Contains(grants, "listen:"+e.Topic)) {
		return apiError("permission_denied", "topic was not granted")
	}
	var visible []v2.Available
	for _, service := range p.Services {
		for _, d := range m.Uses {
			match, _ := v2.Matches(service.Version, d.VersionRange)
			if service.ID != d.ID || !match {
				continue
			}
			var methods []string
			for _, method := range d.Methods {
				if slices.Contains(service.Methods, method) && slices.Contains(grants, v2.Grant(d.ID, method)) {
					methods = append(methods, method)
				}
			}
			if len(methods) > 0 {
				service.Methods = methods
				visible = append(visible, service)
			}
		}
	}
	p.Services = visible
	return nil
}
