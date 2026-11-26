package host

import (
	"encoding/json"
	"path"
	"slices"

	"quailcrate/internal/pluginapps"
	v1 "quailcrate/pluginapi/v1"
)

var _ pluginapps.PageService = (*Host)(nil)

// Called under Host.mu after acquiring the instance's serial execution slot.
func (tab *tabSession) allowsEvent(revision uint64, event *v1.Event) bool {
	switch event.Type {
	case "page.message":
		return tab.entry != "" && tab.pageReady && revision == tab.revision
	case "tab.action":
		if revision == 0 || revision != tab.revision || tab.view == nil {
			return false
		}
		if len(event.InputValue) != 0 {
			return event.ItemID == "" && tab.view.AllowsInput(event.ControlID, event.InputValue)
		}
		return tab.view.AllowsTabAction(event.ControlID, event.ItemID)
	default:
		return true
	}
}

func (h *Host) SubmitTabValue(page pluginapps.PageRef, action string, value json.RawMessage) error {
	h.mu.Lock()
	tab := h.tabs[page.PluginID]
	h.mu.Unlock()
	if tab == nil || tab.ref != page.Session || len(value) == 0 {
		return ErrInvocationUnavailable
	}
	event := &v1.Event{Type: "tab.action", TabID: page.TabID, ControlID: action, InputValue: slices.Clone(value)}
	return h.invokeFrom(page.PluginID, page.Generation, "event", v1.Payload{TabID: page.TabID, Event: event}, tab, page.Revision)
}

func (h *Host) ReadPageAsset(session pluginapps.Session, requestURL string) (pluginapps.PageAsset, error) {
	h.mu.Lock()
	tab, rec := h.tabs[session.PluginID], h.records[session.PluginID]
	if tab == nil || tab.ref != session || tab.entry == "" || rec == nil || rec.generation != session.Generation || !h.currentTabLocked(tab) {
		h.mu.Unlock()
		return pluginapps.PageAsset{}, ErrInvocationUnavailable
	}
	pkg := rec.pkg
	h.mu.Unlock()
	asset, err := pluginapps.AssetPath(tab.origin, requestURL)
	if err != nil {
		return pluginapps.PageAsset{}, err
	}
	data, mime, err := pkg.ReadPageAsset(tab.entry, asset)
	if err != nil {
		return pluginapps.PageAsset{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.currentTabLocked(tab) || rec.generation != session.Generation || h.records[session.PluginID] != rec {
		return pluginapps.PageAsset{}, ErrInvocationUnavailable
	}
	return pluginapps.PageAsset{ContentType: mime, Body: data}, nil
}

func (h *Host) SendPageMessage(page pluginapps.PageRef, sourceURL string, raw []byte) (pluginapps.PageReply, error) {
	return h.sendPageMessage(page, sourceURL, raw, false)
}

func (h *Host) SendPageUserMessage(page pluginapps.PageRef, sourceURL string, raw []byte) (pluginapps.PageReply, error) {
	return h.sendPageMessage(page, sourceURL, raw, true)
}

func (h *Host) sendPageMessage(page pluginapps.PageRef, sourceURL string, raw []byte, userGesture bool) (pluginapps.PageReply, error) {
	m, err := pluginapps.ParsePageMessage(raw)
	if err != nil {
		return pluginapps.PageReply{}, err
	}
	h.mu.Lock()
	tab := h.tabs[page.PluginID]
	h.mu.Unlock()
	if tab == nil || tab.ref != page.Session || tab.entry == "" || m.Revision != page.Revision || sourceURL != pluginapps.DocumentURL(tab.origin, path.Base(tab.entry)) {
		return pluginapps.PageReply{}, ErrInvocationUnavailable
	}
	reply := pluginapps.PageReply{RequestID: m.RequestID, Revision: page.Revision}
	scope := &callScope{ctx: tab.ctx, interactive: userGesture}
	event := &v1.Event{Type: "page.message", TabID: page.TabID, ActionID: m.RequestID, ControlID: m.Operation, Detail: m.Args, PageUserGesture: userGesture}
	if err := h.invokeScoped(page.PluginID, page.Generation, "event", v1.Payload{TabID: page.TabID, Event: event}, tab, page.Revision, scope); err != nil {
		return reply, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.currentTabLocked(tab) {
		return pluginapps.PageReply{}, ErrInvocationUnavailable
	}
	reply.Value = scope.pageReply
	return reply, nil
}
