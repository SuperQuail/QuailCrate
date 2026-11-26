package host

import (
	"context"
	"crypto/rand"
	"errors"
	"path"
	"slices"
	"strings"
	"sync"

	"quailcrate/internal/pluginapps"
	v1 "quailcrate/pluginapi/v1"
)

type tabSession struct {
	ref          pluginapps.Session
	ctx          context.Context
	cancel       context.CancelFunc
	dialogs      pluginapps.DialogHandler
	entry        string
	origin       string
	pageReady    bool
	entryChecked bool // protected by the plugin instance's serial lock
	// Protected by Host.mu. A revision identifies exactly what the user saw.
	revision uint64
	view     *v1.View
}

func (h *Host) AppCatalog() pluginapps.Snapshot {
	snapshot, _ := h.ObserveApps()
	return snapshot
}

func (h *Host) ObserveApps() (pluginapps.Snapshot, <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.appCatalog.Clone(), h.appChanged
}

func (h *Host) notifyAppsLocked() {
	close(h.appChanged)
	h.appChanged = make(chan struct{})
}

func (h *Host) OpenTab(ref pluginapps.TabRef, dialogs pluginapps.DialogHandler) (pluginapps.Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	def, ok := h.appCatalog.ResolveTab(ref.PluginID, ref.TabID)
	rec := h.records[ref.PluginID]
	if !ok || def.TabRef != ref || rec == nil || rec.generation != ref.Generation || h.ctx.Err() != nil {
		return pluginapps.Session{}, ErrInvocationUnavailable
	}
	if previous := h.tabs[ref.PluginID]; previous != nil {
		if previous.ref.TabRef == ref {
			return previous.ref, nil
		}
		previous.cancel()
	}
	h.tabSequence++
	session := pluginapps.Session{TabRef: ref, ID: h.tabSequence}
	ctx, cancel := context.WithCancel(h.ctx)
	tab := &tabSession{ref: session, ctx: ctx, cancel: cancel, dialogs: dialogs, entry: def.Page}
	if tab.entry != "" {
		tab.origin = "https://" + strings.ToLower(rand.Text()) + ".quailcrate.invalid"
	}
	h.tabs[ref.PluginID] = tab
	return session, nil
}

func (h *Host) CloseTab(session pluginapps.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if tab := h.tabs[session.PluginID]; tab != nil && tab.ref == session {
		tab.cancel()
		delete(h.tabs, session.PluginID)
	}
}

func (h *Host) currentTabLocked(tab *tabSession) bool {
	return tab == nil || (h.tabs[tab.ref.PluginID] == tab && tab.ctx.Err() == nil)
}

func (h *Host) LoadTab(session pluginapps.Session) (pluginapps.Page, error) {
	h.mu.Lock()
	tab, rec := h.tabs[session.PluginID], h.records[session.PluginID]
	h.mu.Unlock()
	if tab == nil || rec == nil || tab.ref != session {
		return pluginapps.Page{}, ErrInvocationUnavailable
	}
	rec.serial.Lock()
	defer rec.serial.Unlock()
	if tab.ctx.Err() != nil {
		return pluginapps.Page{}, ErrInvocationUnavailable
	}
	if tab.entry != "" && !tab.entryChecked {
		if _, _, err := rec.pkg.ReadPageAsset(tab.entry, path.Base(tab.entry)); err != nil {
			return pluginapps.Page{}, err
		}
		tab.entryChecked = true
	}
	snapshot, err := h.db.Snapshot(tab.ctx, session.PluginID)
	if err != nil {
		return pluginapps.Page{}, err
	}
	payload := h.payload(snapshot)
	payload.TabID = session.TabID
	response, err := h.manager.CallGeneration(tab.ctx, session.PluginID, session.Generation, "tab_state", payload)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.currentTabLocked(tab) || rec.generation != session.Generation {
		return pluginapps.Page{}, ErrInvocationUnavailable
	}
	tab.revision++
	tab.view = nil
	tab.pageReady = false
	if err != nil {
		return pluginapps.Page{}, err
	}
	page := pluginapps.Page{PageRef: pluginapps.PageRef{Session: session, Revision: tab.revision}}
	if tab.entry != "" {
		tab.pageReady = true
		page.Document = &pluginapps.Document{URL: pluginapps.DocumentURL(tab.origin, path.Base(tab.entry)), State: slices.Clone(response.Data.PageState)}
		return page, nil
	}
	tab.view = response.Data.View
	page.View = tab.view.Clone()
	return page, nil
}

func (h *Host) ActOnTab(page pluginapps.PageRef, controlID, itemID string) error {
	h.mu.Lock()
	tab := h.tabs[page.PluginID]
	h.mu.Unlock()
	if tab == nil || tab.ref != page.Session || page.Revision == 0 {
		return ErrInvocationUnavailable
	}
	event := &v1.Event{Type: "tab.action", TabID: page.TabID, ControlID: controlID, ItemID: itemID}
	return h.invokeFrom(page.PluginID, page.Generation, "event", v1.Payload{TabID: page.TabID, Event: event}, tab, page.Revision)
}

// resumeTab keeps result routing and identity under host control. A late or
// duplicate completion must never reach a new page or a restarted plugin.
func (h *Host) resumeTab(tab *tabSession, event v1.Event) {
	err := h.invokeFrom(tab.ref.PluginID, tab.ref.Generation, "event", v1.Payload{TabID: tab.ref.TabID, Event: &event}, tab, 0)
	if err != nil && !errors.Is(err, ErrInvocationUnavailable) && tab.ctx.Err() == nil {
		h.log.Warn("plugin tab result", "plugin", tab.ref.PluginID, "tab", tab.ref.TabID, "error", err)
		if tab.dialogs != nil {
			tab.dialogs(tab.ctx, v1.Action{Type: "ui.message", Title: "Plugin operation failed", Value: err.Error()}, nil)
		}
	}
}

func (h *Host) presentTabDialog(tab *tabSession, action v1.Action, event v1.Event) {
	if action.Type == "ui.message" {
		tab.dialogs(tab.ctx, action, nil)
		return
	}
	var once sync.Once
	tab.dialogs(tab.ctx, action, func(result pluginapps.DialogResult) {
		once.Do(func() {
			event.Canceled, event.Error = result.Canceled, result.Error
			event.Value, event.Paths = result.Value, slices.Clone(result.Paths)
			event.Items = slices.Clone(result.Items)
			go h.resumeTab(tab, event)
		})
	})
}
