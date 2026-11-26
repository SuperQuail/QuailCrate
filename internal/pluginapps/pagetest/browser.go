// Package pagetest provides a headless browser double for application-window
// and SDK integration tests. It never runs page scripts or opens a window.
package pagetest

import (
	"encoding/json"
	"errors"

	"quailcrate/internal/pluginapps"
)

var ErrRuntimeUnavailable = errors.New("packaged-page runtime unavailable")

type Browser struct {
	Service     pluginapps.PageService
	Unavailable bool
	Page        pluginapps.Page
}

func (b *Browser) Open(tab pluginapps.TabRef, dialogs pluginapps.DialogHandler) error {
	b.Close()
	if b.Unavailable {
		return ErrRuntimeUnavailable
	}
	session, err := b.Service.OpenTab(tab, dialogs)
	if err != nil {
		return err
	}
	page, err := b.Service.LoadTab(session)
	if err == nil {
		if page.Document == nil {
			err = errors.New("expected a packaged page")
		} else {
			_, err = b.Service.ReadPageAsset(session, page.Document.URL)
		}
	}
	if err != nil {
		b.Service.CloseTab(session)
		return err
	}
	b.Page = page
	return nil
}

func (b *Browser) Send(id, op string, args json.RawMessage) (pluginapps.PageReply, error) {
	if b.Page.Document == nil {
		return pluginapps.PageReply{}, pluginapps.ErrUnavailable
	}
	payload, err := json.Marshal(pluginapps.PageMessage{RequestID: id, Operation: op, Args: args, Revision: b.Page.Revision})
	if err != nil {
		return pluginapps.PageReply{}, err
	}
	reply, err := b.Service.SendPageMessage(b.Page.PageRef, b.Page.Document.URL, payload)
	if err != nil {
		return reply, err
	}
	page, err := b.Service.LoadTab(b.Page.Session)
	if err == nil {
		b.Page = page
	}
	return reply, err
}

func (b *Browser) Close() {
	if b.Page.Session.ID != 0 {
		b.Service.CloseTab(b.Page.Session)
	}
	b.Page = pluginapps.Page{}
}
