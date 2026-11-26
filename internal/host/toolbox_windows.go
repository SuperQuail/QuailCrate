package host

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"quailcrate/internal/musicmedia"
	"quailcrate/internal/pluginapps"
	"quailcrate/internal/pluginaudio"
	"quailcrate/internal/pluginfiles"
	"quailcrate/internal/pluginio"
	"quailcrate/internal/plugins"
	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

type toolInstance struct {
	ctx    context.Context
	net    *pluginio.Instance
	audio  *pluginaudio.Engine
	media  *musicmedia.Store
	grants []string
	closed chan struct{}
	// An immediate decoder error must arrive after the play receipt that
	// tells the plugin which session the event belongs to.
	starting     sync.Map // session ID -> chan struct{} until the receipt is delivered
	mediaMu      sync.Mutex
	mediaSession string
	mediaKey     musicmedia.Key
	failedLocal  musicmedia.Key
	// Updated while holding the plugin's serial invocation lock. Dispatch
	// registration (not completion of a download or timer) follows commit order.
	lastDispatch <-chan struct{}
}

func (h *Host) newToolInstance(ctx context.Context, id string, generation uint64, grants []string) *toolInstance {
	t := &toolInstance{ctx: ctx, grants: slices.Clone(grants), net: h.network.Instance(ctx, id, slices.Contains(grants, "network.local")), closed: make(chan struct{})}
	if h.cacheDir != "" && slices.Contains(grants, "audio.play") {
		owner := fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
		t.media = musicmedia.New(ctx, filepath.Join(h.cacheDir, "audio", owner),
			filepath.Join(filepath.Dir(h.userDir), "media-downloads", owner))
	}
	t.audio = pluginaudio.New(func(e pluginaudio.Event) {
		if gate, ok := t.starting.Load(e.State.SessionID); ok {
			select {
			case <-gate.(chan struct{}):
			case <-ctx.Done():
				return
			}
		}
		data, _ := json.Marshal(e.State)
		event := v1.Event{Type: e.Type, ActionID: e.State.SessionID, Detail: data, Error: e.Error}
		if e.Error != "" {
			event.Code = "playback_failed"
			t.mediaMu.Lock()
			if t.media != nil && t.mediaSession == e.State.SessionID && t.mediaKey.Track != "" {
				if entry, exists := t.media.Lookup(t.mediaKey); exists && entry.Downloaded {
					t.failedLocal = t.mediaKey
				}
				t.media.RejectPlayback(t.mediaKey)
			}
			t.mediaMu.Unlock()
		}
		h.deliverTool(id, generation, nil, event)
	})
	context.AfterFunc(ctx, func() {
		t.audio.Close()
		if t.media != nil {
			t.media.Close()
		}
		close(t.closed)
	})
	return t
}

// Background completions route to this generation even when its UI is closed.
// Page-scoped callbacks additionally require the exact originating tab session.
func (h *Host) deliverTool(id string, generation uint64, tab *tabSession, event v1.Event) {
	if tab != nil {
		if tab.ctx.Err() != nil {
			return
		}
		event.TabID = tab.ref.TabID
	}
	err := h.invokeFrom(id, generation, "event", v1.Payload{Event: &event, TabID: event.TabID}, tab, 0)
	if err != nil && !errors.Is(err, ErrInvocationUnavailable) && h.ctx.Err() == nil {
		h.log.Warn("plugin tool event rejected", "plugin", id, "event", event.Type)
	}
}

func toolResult(id string, value any, err error) v1.Event {
	e := v1.Event{Type: "action.result", ActionID: id, Status: "ok"}
	if err == nil {
		if value != nil {
			e.Detail, err = json.Marshal(value)
		}
		if err == nil {
			return e
		}
	}
	e.Status = "error"
	switch {
	case errors.Is(err, context.Canceled):
		e.Status, e.Code, e.Canceled = "canceled", "canceled", true
	case errors.Is(err, context.DeadlineExceeded):
		e.Code = "timeout"
	case errors.Is(err, pluginio.ErrDenied), errors.Is(err, pluginfiles.ErrDenied), errors.Is(err, fs.ErrPermission):
		e.Code = "permission_denied"
	case errors.Is(err, pluginio.ErrBusy):
		e.Code = "busy"
	case errors.Is(err, windows.ERROR_DISK_FULL), errors.Is(err, windows.ERROR_HANDLE_DISK_FULL):
		e.Code = "disk_full"
	case errors.Is(err, musicmedia.ErrStorage):
		e.Code = "storage_unavailable"
	case errors.Is(err, pluginio.ErrInvalid):
		e.Code = "invalid_argument"
	case errors.Is(err, fs.ErrNotExist):
		e.Code = "not_found"
	default:
		e.Code = "operation_failed"
	}
	// OS and HTTP errors can contain credential-bearing URLs or private paths.
	e.Error = e.Code
	return e
}

type downloadItem struct {
	CacheKey string `json:"cacheKey"`
	Quality  string `json:"quality"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	Bytes    int64  `json:"bytes"`
	Percent  *int   `json:"percent,omitempty"`
}

func (t *toolInstance) downloadItems() []downloadItem {
	items := []downloadItem{}
	if t.media == nil {
		return items
	}
	for _, entry := range t.media.Downloads() {
		item := downloadItem{CacheKey: entry.Track, Quality: entry.Quality, Label: entry.Label, Bytes: entry.Bytes, Status: entry.Status}
		if entry.Downloaded {
			item.Status = "downloaded"
			percent := 100
			item.Percent = &percent
		} else if entry.Total > 0 {
			percent := int(min(entry.Bytes*100/entry.Total, 99))
			item.Percent = &percent
		}
		items = append(items, item)
	}
	return items
}

func (h *Host) performTool(id string, generation uint64, action v1.Action, origin *tabSession, interactive bool) {
	h.mu.Lock()
	rec := h.records[id]
	if rec == nil || rec.generation != generation || rec.toolbox == nil {
		h.mu.Unlock()
		return
	}
	t := rec.toolbox
	locale := h.locale
	h.mu.Unlock()
	if t.ctx.Err() != nil {
		return
	}
	scope := t.ctx
	var receiver *tabSession
	reply := func(value any, err error) {
		h.deliverTool(id, generation, receiver, toolResult(action.ID, value, err))
	}
	a, err := v2.ParseToolArgs(action.Type, action.Args)
	if err != nil {
		reply(nil, pluginio.ErrInvalid)
		return
	}
	grant, _ := v2.ToolGrant(action.Type)
	if (grant != "" && !slices.Contains(t.grants, grant)) ||
		(v2.RequiresMediaNetwork(action.Type) && !slices.Contains(t.grants, "network.http")) ||
		(a.Root != "" && a.Root != "private" && !slices.Contains(t.grants, "files.directory")) {
		reply(nil, pluginio.ErrDenied)
		return
	}
	if a.Scope == "page" {
		if origin == nil {
			reply(nil, pluginio.ErrInvalid)
			return
		}
		scope, receiver = origin.ctx, origin
	}
	if origin != nil && (a.Scope == "page" || action.Type == "files.pickDirectory") && origin.ctx.Err() != nil {
		return
	}
	request := pluginio.Request{URL: a.URL, Method: a.Method, Body: a.Body, Headers: a.Headers, Session: a.Session, TimeoutMs: a.TimeoutMs, ResponseType: a.ResponseType}
	key := musicmedia.Key{Track: a.CacheKey, Quality: a.Quality}
	var source musicmedia.Source
	if a.URL != "" {
		source = func(ctx context.Context) (io.ReadCloser, error) { return t.net.OpenMedia(ctx, request) }
	}
	var result any
	switch action.Type {
	case "http.request":
		err = t.net.RequestContext(scope, action.ID, request, func(r pluginio.Result, err error) { reply(r, err) })
		if err == nil {
			return
		}
	case "http.cancel":
		result = t.net.Cancel(a.Target)
	case "http.session.create":
		err = t.net.CreateSession(a.Session)
	case "http.session.clear":
		err = t.net.ClearSession(a.Session)
	case "timer.after":
		err = t.net.AfterContext(scope, action.ID, time.Duration(a.DelayMs)*time.Millisecond, func() {
			h.deliverTool(id, generation, receiver, v1.Event{Type: "timer.fired", ActionID: action.ID})
		})
		if err == nil {
			return
		}
	case "timer.cancel":
		result = t.net.CancelTimer(a.Target)
	case "audio.play", "audio.playCached":
		label := a.Label
		open := source
		t.mediaMu.Lock()
		if action.Type == "audio.playCached" || key != t.failedLocal {
			t.failedLocal = musicmedia.Key{}
		}
		bypassLocal := key.Track != "" && key == t.failedLocal
		t.mediaMu.Unlock()
		if action.Type == "audio.playCached" {
			if t.media == nil {
				err = fs.ErrNotExist
				break
			}
			entry, found := t.media.Lookup(key)
			if !found {
				err = fs.ErrNotExist
				break
			}
			label = entry.Label
		}
		if key.Track != "" && t.media != nil && !bypassLocal {
			open = func(ctx context.Context) (io.ReadCloser, error) { return t.media.Open(ctx, key, label, source) }
		}
		sessionID := rand.Text()
		accepted := make(chan struct{})
		t.starting.Store(sessionID, accepted)
		defer func() { close(accepted); t.starting.Delete(sessionID) }()
		err = t.audio.Play(t.ctx, sessionID, open)
		if err == nil {
			t.mediaMu.Lock()
			t.mediaSession, t.mediaKey = sessionID, key
			t.mediaMu.Unlock()
		}
		result = map[string]string{"sessionId": sessionID, "label": label}
	case "audio.prefetch":
		if t.media == nil {
			err = fs.ErrNotExist
		} else if key.Track == "" {
			t.media.CancelPrefetch()
		} else {
			var wait func() error
			wait, err = t.media.BeginPrefetch(key, a.Label, source)
			if err == nil && wait != nil {
				go func() { reply(nil, wait()) }()
				return
			}
		}
	case "audio.download":
		if t.media == nil {
			err = fs.ErrNotExist
			break
		}
		// Register a separate job so the plugin can poll progress and control
		// playback while the transfer is running.
		var wait func() error
		wait, err = t.media.BeginDownload(t.ctx, key, a.Label, source)
		if err == nil && wait != nil {
			go func() { reply(nil, wait()) }()
			return
		}
	case "audio.downloads":
		result = map[string]any{"items": t.downloadItems()}
	case "audio.removeDownload":
		if t.media != nil {
			err = t.media.RemoveDownload(key.Track)
		}
	case "files.pickDirectory":
		if !interactive {
			err = pluginio.ErrDenied
			break
		}
		accessName := text(locale, "read", "读取")
		if a.Access == "write" {
			accessName = text(locale, "read and write", "读写")
		}
		title := text(locale, "Allow "+id+" to "+accessName+" the selected directory", "允许 "+id+" "+accessName+"所选目录")
		ctx := t.ctx
		if origin != nil {
			ctx, receiver = origin.ctx, origin
		}
		var once sync.Once
		done := func(paths []string, ok bool) {
			once.Do(func() {
				if ctx.Err() != nil || t.ctx.Err() != nil {
					return
				}
				if !ok || len(paths) != 1 {
					reply(nil, context.Canceled)
					return
				}
				d, err := h.files.Grant(ctx, id, paths[0], a.Access)
				reply(d, err)
			})
		}
		if origin != nil {
			if origin.dialogs == nil {
				err = pluginio.ErrDenied
				break
			}
			origin.dialogs(ctx, v1.Action{Type: "ui.pickPaths", ID: action.ID, Title: title, AllowDirectories: true, DirectoriesOnly: true}, func(r pluginapps.DialogResult) { go done(r.Paths, !r.Canceled && r.Error == "") })
		} else {
			h.ui.PickPaths(title, nil, true, true, false, func(paths []string, ok bool) { go done(paths, ok) })
		}
		return
	default:
		if strings.HasPrefix(action.Type, "audio.") {
			result, err = t.audio.Control(a.Target, action.Type, a.PositionMs, a.Volume)
		} else {
			result, err = h.files.Execute(scope, id, action.Type, a)
		}
	}
	reply(result, err)
}

// RevokePluginPermission is a trusted settings operation. Disable the instance
// before returning so outstanding work cannot continue under its former grants.
func (h *Host) RevokePluginPermission(id, permission string) error {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	h.mu.Lock()
	rec := h.records[id]
	h.mu.Unlock()
	if rec == nil {
		return ErrInvocationUnavailable
	}
	p, err := h.db.Preference(h.ctx, id)
	if err != nil {
		return err
	}
	if !p.Exists {
		p.Grants = slices.Clone(rec.grants)
	}
	if !slices.Contains(p.Grants, permission) {
		return nil
	}
	grants := slices.DeleteFunc(p.Grants, func(g string) bool { return g == permission })
	if err = h.db.SetPreference(h.ctx, id, false, grants); err != nil {
		return err
	}
	h.mu.Lock()
	rec.grants = slices.Clone(grants)
	rec.info.RequestedPermissions = plugins.RequestedGrants(rec.pkg.Manifest)
	rec.info.GrantedPermissions = slices.Clone(grants)
	h.mu.Unlock()
	h.disable(rec)
	h.reconcileServices()
	h.publish()
	h.repeatSearch()
	return nil
}

// PluginPermissions returns the manifest request and the user's saved decision
// separately so settings can explain a new or revoked grant while disabled.
func (h *Host) PluginPermissions(id string) (requested, granted []string, err error) {
	h.lifecycle.Lock()
	defer h.lifecycle.Unlock()
	h.mu.Lock()
	rec := h.records[id]
	h.mu.Unlock()
	if rec == nil {
		return nil, nil, ErrInvocationUnavailable
	}
	p, err := h.db.Preference(h.ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if !p.Exists {
		p.Grants = slices.Clone(rec.grants)
	}
	return plugins.RequestedGrants(rec.pkg.Manifest), p.Grants, nil
}

// DirectoryGrants and RevokeDirectory are available to the trusted settings UI
// even after the plugin has been disabled.
func (h *Host) DirectoryGrants(id string) ([]pluginfiles.Directory, error) {
	v, err := h.files.Execute(h.ctx, id, "files.directories", v2.ToolArgs{})
	if err != nil {
		return nil, err
	}
	return v.([]pluginfiles.Directory), nil
}
func (h *Host) RevokeDirectory(id, ref string) error {
	_, err := h.files.Execute(h.ctx, id, "files.revokeDirectory", v2.ToolArgs{Root: ref})
	return err
}
