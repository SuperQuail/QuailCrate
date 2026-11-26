package plugins

import (
	"net/url"
	"slices"
	"strings"

	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

// Resolve external image keys before a response can reach an asynchronous
// loader or the shared cache. The app list here is the permission-filtered
// snapshot actually delivered to this plugin invocation.
func authorizeIcons(grants []string, apps []v1.App, data *v1.Data) error {
	var catalog map[string]string
	resolve := func(key *string) error {
		if *key == "" {
			return nil
		}
		if slices.Contains(grants, "apps.read") {
			if catalog == nil {
				catalog = make(map[string]string, len(apps)*2)
				for _, a := range apps {
					catalog[a.ID] = a.Icon
					if a.Icon != "" {
						catalog[a.Icon] = a.Icon
					}
				}
			}
			if icon, ok := catalog[*key]; ok {
				*key = icon
				return nil
			}
		}
		if v1.IsIconName(*key) {
			return nil
		}
		if address, ok := strings.CutPrefix(*key, "favicon:"); ok {
			u, err := url.Parse(address)
			if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
				return apiError("invalid_argument", "expected an HTTP or HTTPS favicon")
			}
			if slices.Contains(grants, "urls.open") {
				return nil
			}
			return apiError("permission_denied", "URL icons require urls.open")
		}
		path := strings.TrimPrefix(*key, "imgfile:")
		if !localIconPath(path) {
			return apiError("permission_denied", "expected an issued catalog icon or an absolute local drive path")
		}
		if !slices.Contains(grants, "paths.open") {
			return apiError("permission_denied", "file icons require paths.open")
		}
		return nil
	}
	for i := range data.Results {
		if err := resolve(&data.Results[i].Icon); err != nil {
			return err
		}
	}
	for i := range data.Actions {
		a := &data.Actions[i]
		for j := range a.Options {
			if err := resolve(&a.Options[j].Icon); err != nil {
				return err
			}
		}
		if a.Type == "icons.refresh" {
			if a.IconKey == "" && a.URL != "" {
				a.IconKey = "favicon:" + a.URL
			}
			if err := resolve(&a.IconKey); err != nil {
				return err
			}
		}
	}
	if view := data.View; view != nil {
		for i := range view.Controls {
			for j := range view.Controls[i].Items {
				if err := resolve(&view.Controls[i].Items[j].Icon); err != nil {
					return err
				}
			}
		}
		if view.Root != nil {
			// The caller has already validated the tree bounds.
			var walk func(*v2.Node) error
			walk = func(n *v2.Node) error {
				for i := range n.Props.Items {
					if err := resolve(&n.Props.Items[i].Icon); err != nil {
						return err
					}
				}
				for i := range n.Children {
					if err := walk(&n.Children[i]); err != nil {
						return err
					}
				}
				return nil
			}
			if err := walk(view.Root); err != nil {
				return err
			}
		}
		view.PrepareLayout()
	}
	return nil
}

func localIconPath(path string) bool {
	if len(path) < 3 || path[1] != ':' || path[2] != '\\' && path[2] != '/' {
		return false
	}
	if c := path[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
		return false
	}
	return !strings.ContainsAny(path[2:], "\x00\r\n:")
}
