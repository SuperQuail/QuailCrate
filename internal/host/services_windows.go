package host

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"quailcrate/internal/pluginapps"
	"quailcrate/internal/winui"
	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

type callScope struct {
	pageReply   json.RawMessage
	ctx         context.Context
	chain       []string
	interactive bool
	picker      *v1.Action
	reply       *v2.Reply
}
type endpoint struct {
	id, version, name string
	generation        uint64
	lifetime          context.Context
}

func compatible(p v2.Export, d v2.Dependency) bool {
	if p.ID != d.ID {
		return false
	}
	match, _ := v2.Matches(p.Version, d.VersionRange)
	return match && !slices.ContainsFunc(d.Methods, func(method string) bool { return !slices.Contains(p.Methods, method) })
}
func (h *Host) candidatesLocked(d v2.Dependency, provider string) ([]endpoint, bool) {
	var out []endpoint
	found := false
	for id, r := range h.records {
		if (provider != "" && id != provider) || r.generation == 0 {
			continue
		}
		var best string
		for _, p := range r.pkg.Manifest.Provides {
			if p.ID != d.ID {
				continue
			}
			found = true
			if compatible(p, d) {
				if best != "" {
					newer, _ := v2.Matches(p.Version, ">"+best)
					if !newer {
						continue
					}
				}
				best = p.Version
			}
		}
		if best != "" {
			out = append(out, endpoint{id, best, r.info.Name, r.generation, r.lifetime})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, found
}
func (h *Host) resultActionsLocked(ref *v2.ItemRef) []winui.ResultMenuAction {
	if ref == nil {
		return nil
	}
	var out []winui.ResultMenuAction
	for id, r := range h.records {
		if r.generation == 0 {
			continue
		}
		for _, a := range r.registration.ResultActions {
			if slices.Contains(a.Kinds, ref.Kind) {
				out = append(out, winui.ResultMenuAction{ID: a.ID, Title: a.Title, Provider: id, Generation: r.generation, Service: a.Service, Version: a.Version, Method: a.Method})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider == out[j].Provider {
			return out[i].ID < out[j].ID
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

// Only commands from the host's current result set are executable.
func (h *Host) activateResult(selected winui.Result, menu *winui.ResultMenuAction) {
	h.mu.Lock()
	var r *winui.Result
	if selected.Revision == h.searchID {
		for i := range h.visibleResults {
			x := &h.visibleResults[i]
			if x.ID == selected.ID && x.PluginID == selected.PluginID && x.Generation == selected.Generation && x.Revision == selected.Revision {
				r = x
				break
			}
		}
	}
	if r == nil || h.records[r.PluginID] == nil || h.records[r.PluginID].generation != r.Generation {
		h.mu.Unlock()
		return
	}
	result := *r
	if menu == nil {
		h.consumeSearchLocked()
		h.mu.Unlock()
		h.invokeAndReport(result.PluginID, result.Generation, "command", v1.Payload{CommandID: result.CommandID, Args: result.Args})
		return
	}
	if !slices.Contains(result.Actions, *menu) || !slices.Contains(h.resultActionsLocked(result.Item), *menu) {
		h.mu.Unlock()
		return
	}
	target := h.records[menu.Provider]
	ep := endpoint{menu.Provider, menu.Version, target.info.Name, target.generation, target.lifetime}
	ctx := h.records[r.PluginID].lifetime
	h.consumeSearchLocked()
	h.mu.Unlock()
	args, _ := json.Marshal(map[string]any{"item": result.Item, "title": result.Title})
	reply := h.executeService(ctx, result.PluginID, ep, menu.Service, menu.Method, args, true, nil, h.optionDialog)
	if reply.Status == "error" && h.ui != nil {
		h.showError(menu.Title, errors.New(reply.Code))
	}
}

func (h *Host) consumeSearchLocked() {
	if h.cancelSearch != nil {
		h.cancelSearch()
		h.cancelSearch = nil
	}
	h.searchID++
	h.query = ""
	h.visibleResults = nil
}

func failure(code string) v2.Reply { return v2.Reply{Status: "error", Code: code} }
func pick(ctx context.Context, dialog pluginapps.DialogHandler, title string, opts []v1.Option) (string, bool) {
	if dialog == nil || ctx.Err() != nil {
		return "", false
	}
	result := make(chan string, 1)
	dialog(ctx, v1.Action{Type: "ui.pickOptions", Title: title, Options: opts}, func(res pluginapps.DialogResult) {
		id := ""
		if !res.Canceled && res.Error == "" && len(res.Items) == 1 && slices.ContainsFunc(opts, func(o v1.Option) bool { return o.ID == res.Items[0] }) {
			id = res.Items[0]
		}
		select {
		case result <- id:
		default:
		}
	})
	select {
	case id := <-result:
		return id, id != ""
	case <-ctx.Done():
		return "", false
	}
}
func (h *Host) resolveService(ctx context.Context, caller string, gen uint64, action v1.Action, interactive bool, dialog pluginapps.DialogHandler) (endpoint, v2.Reply) {
	h.mu.Lock()
	r := h.records[caller]
	if ctx.Err() != nil || r == nil || gen == 0 || r.generation != gen {
		h.mu.Unlock()
		return endpoint{}, failure("unavailable")
	}
	if !slices.Contains(r.grants, v2.Grant(action.Service, action.Method)) {
		h.mu.Unlock()
		return endpoint{}, failure("permission_denied")
	}
	var dep v2.Dependency
	for _, d := range r.pkg.Manifest.Uses {
		if d.ID == action.Service && slices.Contains(d.Methods, action.Method) {
			dep = d
			break
		}
	}
	options, found := h.candidatesLocked(dep, action.Provider)
	h.mu.Unlock()
	if dep.ID == "" {
		return endpoint{}, failure("permission_denied")
	}
	if len(options) == 0 {
		if found {
			return endpoint{}, failure("incompatible_version")
		}
		return endpoint{}, failure("unavailable")
	}
	if len(options) == 1 {
		return options[0], v2.Reply{}
	}
	key := "binding." + caller + "/" + action.Service + "/" + action.Method
	saved, err := h.db.Setting(ctx, key, "")
	if err != nil {
		return endpoint{}, failure("storage_error")
	}
	for _, o := range options {
		if o.id == saved {
			return o, v2.Reply{}
		}
	}
	if !interactive {
		return endpoint{}, failure("ambiguous_provider")
	}
	var choices []v1.Option
	for _, o := range options {
		choices = append(choices, v1.Option{ID: o.id, Text: o.name, Subtitle: o.id + " " + o.version})
	}
	choice, ok := pick(ctx, dialog, action.Service, choices)
	if !ok {
		return endpoint{}, v2.Reply{Status: "canceled"}
	}
	for _, o := range options {
		if o.id == choice {
			// The chooser can outlive either generation. Record the binding
			// only if the provider and caller the user saw are both current.
			h.mu.Lock()
			defer h.mu.Unlock()
			provider := h.records[o.id]
			if ctx.Err() != nil || h.records[caller] != r || r.generation != gen || provider == nil || provider.generation != o.generation {
				return endpoint{}, failure("unavailable")
			}
			if err := h.db.SetSetting(ctx, key, choice); err != nil {
				return endpoint{}, failure("storage_error")
			}
			return o, v2.Reply{}
		}
	}
	return endpoint{}, failure("unavailable")
}
func (h *Host) callService(caller string, gen uint64, a v1.Action, origin *tabSession, widget string, interactive bool, chain []string) {
	h.mu.Lock()
	r := h.records[caller]
	if r == nil || r.generation != gen {
		h.mu.Unlock()
		return
	}
	life := r.lifetime
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(life, 2*time.Minute)
	defer cancel()
	if origin != nil {
		stop := context.AfterFunc(origin.ctx, cancel)
		defer stop()
	}
	dialog := h.optionDialog
	if origin != nil {
		dialog = origin.dialogs
	}
	ep, reply := h.resolveService(ctx, caller, gen, a, interactive, dialog)
	if len(chain) == 0 {
		chain = []string{caller}
	}
	if ep.id != "" {
		reply = h.executeService(ctx, caller, ep, a.Service, a.Method, a.Args, interactive, chain, dialog)
	}
	if !h.usable(caller, gen) || origin != nil && origin.ctx.Err() != nil {
		return
	}
	event := v1.Event{Type: "service.result", ActionID: a.ID, WidgetID: widget, Source: ep.id, Status: reply.Status, Detail: reply.Value, Error: reply.Code, Canceled: reply.Status == "canceled"}
	if origin != nil {
		event.TabID = origin.ref.TabID
	}
	// Include the completed hop in any calls issued by this result handler.
	next := slices.Clone(chain)
	if ep.id != "" {
		next = append(next, ep.id)
	} else {
		next = append(next, a.Service)
	}
	scope := &callScope{ctx: life, chain: next, interactive: interactive}
	if origin != nil {
		scope.ctx = origin.ctx
	}
	if err := h.invokeScoped(caller, gen, "event", v1.Payload{WidgetID: widget, TabID: event.TabID, Event: &event}, origin, 0, scope); err != nil && h.ctx.Err() == nil {
		h.log.Warn("service result delivery", "plugin", caller, "error", err)
	}
}
func (h *Host) executeService(parent context.Context, caller string, ep endpoint, service, method string, args json.RawMessage, interactive bool, chain []string, dialog pluginapps.DialogHandler) v2.Reply {
	if !interactive && (service == v2.GroupItems || service == v2.Items) {
		return failure("permission_denied")
	}
	if slices.Contains(chain, ep.id) || len(chain) >= 8 {
		return failure("call_cycle")
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	if ep.lifetime == nil {
		return failure("unavailable")
	}
	stop := context.AfterFunc(ep.lifetime, cancel)
	defer stop()
	payload := v1.Payload{Service: service, Method: method, ServiceVersion: ep.version, Caller: caller, Args: args}
	scope := &callScope{ctx: ctx, chain: append(slices.Clone(chain), ep.id), interactive: interactive}
	for step := 0; step < 2; step++ {
		if err := h.invokeScoped(ep.id, ep.generation, "service", payload, nil, 0, scope); err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return failure("timeout")
			}
			var api *v1.APIError
			if errors.As(err, &api) {
				return failure(api.Code)
			}
			if !h.usable(ep.id, ep.generation) || ctx.Err() != nil {
				return failure("unavailable")
			}
			h.log.Warn("service provider failed", "plugin", ep.id, "service", service, "error", err)
			return failure("provider_error")
		}
		if scope.reply != nil {
			return *scope.reply
		}
		if step != 0 || scope.picker == nil {
			return failure("invalid_reply")
		}
		a := scope.picker
		scope.picker = nil
		id, ok := pick(ctx, dialog, a.Title, a.Options)
		if !ok {
			if ep.lifetime.Err() != nil {
				return failure("unavailable")
			}
			if ctx.Err() != nil {
				return failure("timeout")
			}
			return v2.Reply{Status: "canceled"}
		}
		payload.Event = &v1.Event{Type: "action.result", ActionID: a.ID, Items: []string{id}}
	}
	return failure("invalid_reply")
}
func (h *Host) publishEvent(source string, gen uint64, a v1.Action, previous *v1.Event) {
	var route []string
	if previous != nil {
		route = slices.Clone(previous.Route)
	}
	if slices.Contains(route, source) || len(route) >= 8 {
		return
	}
	route = append(route, source)
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.records[source]; r == nil || r.generation != gen || !slices.Contains(r.pkg.Manifest.Publishes, a.Topic) {
		return
	}
	h.eventRevision++
	for id, r := range h.records {
		if r.generation != 0 && slices.Contains(r.pkg.Manifest.Listens, a.Topic) && !slices.Contains(route, id) {
			go h.event(id, v1.Event{Type: "plugin.event", Topic: a.Topic, Source: source, Detail: a.Args, Revision: h.eventRevision, Route: route}, r.generation)
		}
	}
}
