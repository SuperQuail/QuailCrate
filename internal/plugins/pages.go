package plugins

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

func validatePageDefinition(m v1.Manifest, tab v1.AppTabDefinition) error {
	if tab.Page == "" && len(tab.PageActions) == 0 {
		return nil
	}
	if m.HostAPIVersion != 2 || m.UIVersion != 2 || !v2.PageEntryPath(tab.Page) || len(tab.PageActions) > 128 {
		return errors.New("invalid v2 application page definition")
	}
	seen := make(map[string]bool)
	for _, action := range tab.PageActions {
		if !v2.ValidID(action) || seen[action] {
			return fmt.Errorf("invalid or duplicate page action %q", action)
		}
		seen[action] = true
	}
	return nil
}

func appPage(tabs []v1.AppTabDefinition, id string) (v1.AppTabDefinition, bool) {
	for _, tab := range tabs {
		if tab.ID == id && tab.Page != "" {
			return tab, true
		}
	}
	return v1.AppTabDefinition{}, false
}

func validatePageEvent(event *v1.Event, payload v1.Payload, tabs []v1.AppTabDefinition) error {
	tab, ok := appPage(tabs, payload.TabID)
	if !ok || payload.TabID != event.TabID || !slices.Contains(tab.PageActions, event.ControlID) || !v2.ValidID(event.ActionID) || !v2.PageObject(event.Detail, v2.MaxPageArgsBytes) {
		return errors.New("invalid application page message")
	}
	return nil
}

var pageContentTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp", ".gif": "image/gif", ".ico": "image/x-icon", ".svg": "image/svg+xml",
	".woff": "font/woff", ".woff2": "font/woff2",
}

// ReadPageAsset serves an approved static resource from the entry's directory.
// An os.Root keeps resolution within the opened package even during a rename.
func (pkg Package) ReadPageAsset(entry, asset string) ([]byte, string, error) {
	if !v2.PageEntryPath(entry) || !v2.PageAssetPath(asset) {
		return nil, "", errors.New("invalid application page path")
	}
	contentType := pageContentTypes[strings.ToLower(path.Ext(asset))]
	if contentType == "" || (strings.HasPrefix(contentType, "text/html") && asset != path.Base(entry)) {
		return nil, "", errors.New("unsupported page asset")
	}
	root, err := os.OpenRoot(pkg.Dir)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	dir, err := root.OpenRoot(filepath.FromSlash(path.Dir(entry)))
	if err != nil {
		return nil, "", err
	}
	defer dir.Close()
	f, err := dir.Open(filepath.FromSlash(asset))
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	const maxBytes = 4 << 20
	info, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, "", errors.New("invalid or oversized page asset")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || len(data) > maxBytes {
		return nil, "", errors.New("unable to read bounded page asset")
	}
	return data, contentType, nil
}
