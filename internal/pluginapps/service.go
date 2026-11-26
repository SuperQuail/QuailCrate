package pluginapps

import (
	"context"
	"errors"

	v1 "quailcrate/pluginapi/v1"
)

// ErrUnavailable lets the UI discard a click or response for an obsolete
// command, window or rendered page and refresh its current snapshot.
var ErrUnavailable = errors.New("plugin target is unavailable")

// Session identifies one activation of a tab. Switching tabs or reopening a
// window gives it a new ID even when the same plugin instance is still running.
type Session struct {
	TabRef
	ID uint64
}

type PageRef struct {
	Session
	Revision uint64
}

type Page struct {
	PageRef
	View     v1.View
	Document *Document
}

// DialogResult contains only user-provided data. The host supplies the action
// ID, tab, generation and previous event when it resumes the plugin.
type DialogResult struct {
	Value    string
	Paths    []string
	Items    []string
	Canceled bool
	Error    string
}

// DialogHandler posts a prompt, picker or message to the originating window.
// The handler returns promptly and skips queued dialogs if ctx is canceled.
// Complete a prompt or picker once. A message needs no result.
type DialogHandler func(ctx context.Context, action v1.Action, complete func(DialogResult))

// Service is the settings and independent-window API. ObserveApps supplies a
// snapshot and a broadcast channel which closes on the next catalog or view
// invalidation. Fetch both together again after each notification.
type Service interface {
	ObserveApps() (Snapshot, <-chan struct{})
	InvokeListedCommand(pluginID, commandID string, generation uint64) error
	OpenTab(TabRef, DialogHandler) (Session, error)
	LoadTab(Session) (Page, error)
	ActOnTab(PageRef, string, string) error
	CloseTab(Session)
}
