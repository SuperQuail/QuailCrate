package pluginapps

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"

	v2 "quailcrate/pluginapi/v2"
)

// Document is a web adapter's immutable projection of the active page.
// The adapter keeps the same document across state changes and sends the
// latest revision alongside each state update.
type Document struct {
	URL   string
	State json.RawMessage
}

type PageMessage struct {
	RequestID string          `json:"requestId"`
	Revision  uint64          `json:"revision"`
	Operation string          `json:"operation"`
	Args      json.RawMessage `json:"args"`
}

func ParsePageMessage(raw []byte) (PageMessage, error) {
	var m PageMessage
	if len(raw) > v2.MaxPageArgsBytes+1024 {
		return m, errors.New("page message exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return m, err
	}
	if decoder.Decode(new(any)) != io.EOF || !v2.ValidID(m.RequestID) || !v2.ValidID(m.Operation) || m.Revision == 0 || !v2.PageObject(m.Args, v2.MaxPageArgsBytes) {
		return m, errors.New("invalid page message")
	}
	return m, nil
}

type PageReply struct {
	RequestID string          `json:"requestId"`
	Revision  uint64          `json:"revision"`
	Value     json.RawMessage `json:"value,omitempty"`
}

type PageAsset struct {
	ContentType string
	Body        []byte
}

// Headers for every response from the virtual origin. The adapter must also
// deny other requests, redirects, frames, new windows and native browser
// privileges before allowing the first navigation.
func (a PageAsset) Headers() map[string]string {
	return map[string]string{
		"Content-Type": a.ContentType, "X-Content-Type-Options": "nosniff", "Cache-Control": "no-store",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'none'; frame-src 'none'; frame-ancestors 'none'; object-src 'none'; worker-src 'none'; form-action 'none'; base-uri 'none'",
	}
}

func DocumentURL(origin, name string) string {
	return origin + (&url.URL{Path: "/" + name}).EscapedPath()
}

func AssetPath(origin, requestURL string) (string, error) {
	u, err := url.Parse(requestURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || "https://"+u.Host != origin || !strings.HasPrefix(u.Path, "/") {
		return "", errors.New("invalid application page resource URL")
	}
	asset := strings.TrimPrefix(u.Path, "/")
	if !v2.PageAssetPath(asset) || requestURL != DocumentURL(origin, asset) {
		return "", errors.New("invalid application page asset path")
	}
	return asset, nil
}

// PageService extends the existing tab API for native value commits and
// adapters that display packaged web pages. sourceURL comes from the browser's
// top-level message callback, not the page's JSON.
type PageService interface {
	Service
	ReadPageAsset(Session, string) (PageAsset, error)
	SendPageMessage(PageRef, string, []byte) (PageReply, error)
	// Use only when trusted native/browser input verifies this exact top-level
	// document message. A JS isTrusted flag or a recent unrelated click is
	// insufficient. Without such an attestation, call SendPageMessage.
	SendPageUserMessage(PageRef, string, []byte) (PageReply, error)
	SubmitTabValue(PageRef, string, json.RawMessage) error
}
