package v2

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Tool actions use the existing action envelope with typed arguments in args.
// An empty grant means that the operation is confined to the owning instance.
type toolSpec struct {
	grant  string
	fields string
}

var tools = map[string]toolSpec{
	"http.request":          {"network.http", "url method headers body session timeoutMs responseType scope"},
	"http.cancel":           {"network.http", "target"},
	"http.session.create":   {"network.http", "session"},
	"http.session.clear":    {"network.http", "session"},
	"timer.after":           {"", "delayMs scope"},
	"timer.cancel":          {"", "target"},
	"audio.play":            {"audio.play", "url headers session cacheKey quality label"},
	"audio.playCached":      {"audio.play", "cacheKey quality"},
	"audio.prefetch":        {"audio.play", "url headers session cacheKey quality label"},
	"audio.download":        {"audio.play", "url headers session cacheKey quality label"},
	"audio.downloads":       {"audio.play", ""},
	"audio.removeDownload":  {"audio.play", "cacheKey"},
	"audio.pause":           {"audio.play", "target"},
	"audio.resume":          {"audio.play", "target"},
	"audio.stop":            {"audio.play", "target"},
	"audio.seek":            {"audio.play", "target positionMs"},
	"audio.setVolume":       {"audio.play", "target volume"},
	"audio.getState":        {"audio.play", "target"},
	"files.list":            {"", "root path"},
	"files.read":            {"", "root path"},
	"files.write":           {"", "root path body encoding"},
	"files.mkdir":           {"", "root path"},
	"files.remove":          {"", "root path"},
	"files.pickDirectory":   {"files.directory", "access"},
	"files.directories":     {"files.directory", ""},
	"files.revokeDirectory": {"files.directory", "root"},
}

func ToolGrant(name string) (string, bool) {
	spec, ok := tools[name]
	return spec.grant, ok
}

// These tools may open an authorized source, including when taking over an
// in-flight preload. Retain the network grant on all media acquisition paths.
func RequiresMediaNetwork(name string) bool {
	switch name {
	case "audio.play", "audio.playCached", "audio.prefetch", "audio.download":
		return true
	}
	return false
}

// AvailableTools advertises this host's action vocabulary at init, including
// only actions available with the instance's actual grants.
func AvailableTools(grants []string) []string {
	var names []string
	for name, spec := range tools {
		if spec.grant != "" && !slices.Contains(grants, spec.grant) {
			continue
		}
		if RequiresMediaNetwork(name) && !slices.Contains(grants, "network.http") {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

type ToolArgs struct {
	URL          string            `json:"url"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
	Body         string            `json:"body"`
	Session      string            `json:"session"`
	TimeoutMs    int               `json:"timeoutMs"`
	ResponseType string            `json:"responseType"`
	Scope        string            `json:"scope"`
	Target       string            `json:"target"`
	DelayMs      int               `json:"delayMs"`
	PositionMs   int64             `json:"positionMs"`
	Volume       float64           `json:"volume"`
	Root         string            `json:"root"`
	Path         string            `json:"path"`
	Encoding     string            `json:"encoding"`
	Access       string            `json:"access"`
	CacheKey     string            `json:"cacheKey"`
	Quality      string            `json:"quality"`
	Label        string            `json:"label"`
}

func ParseToolArgs(name string, raw json.RawMessage) (ToolArgs, error) {
	var a ToolArgs
	spec, ok := tools[name]
	if !ok || len(raw) > 1<<20 {
		return a, errors.New("unknown tool or oversized arguments")
	}
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || json.Unmarshal(raw, &a) != nil {
		return a, errors.New("expected an object with typed tool arguments")
	}
	allowed := strings.Fields(spec.fields)
	for key, value := range fields {
		if !slices.Contains(allowed, key) || string(value) == "null" {
			return a, fmt.Errorf("invalid argument %q for %s", key, name)
		}
	}
	if slices.Contains(allowed, "target") && !ValidID(a.Target) {
		return a, errors.New("a target action or session ID is required")
	}
	if a.Scope != "" && a.Scope != "instance" && a.Scope != "page" {
		return a, errors.New("invalid task scope")
	}
	if a.Session != "" && !ValidID(a.Session) {
		return a, errors.New("invalid session ID")
	}
	if len(a.Headers) > 32 || a.TimeoutMs < 0 || a.TimeoutMs > 60000 {
		return a, errors.New("invalid request limits")
	}
	if name == "http.request" || name == "audio.play" || a.URL != "" {
		u, err := url.Parse(a.URL)
		if err != nil || len(a.URL) > 8192 || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
			return a, errors.New("an HTTP(S) URL without embedded credentials is required")
		}
	}
	if name == "audio.playCached" || name == "audio.download" || (name == "audio.prefetch" && len(fields) > 0) ||
		(name == "audio.play" && (a.CacheKey != "" || a.Quality != "" || a.Label != "")) {
		if !ValidID(a.CacheKey) || !ValidID(a.Quality) || len(a.Label) > 80 || strings.ContainsAny(a.Label, "\r\n\x00") {
			return a, errors.New("invalid media key or quality")
		}
		for key := range a.Headers {
			if strings.EqualFold(key, "Range") {
				return a, errors.New("managed audio requires a complete source")
			}
		}
	}
	switch name {
	case "audio.removeDownload":
		if !ValidID(a.CacheKey) {
			return a, errors.New("media key required")
		}
	case "http.session.create", "http.session.clear":
		if a.Session == "" {
			return a, errors.New("session ID required")
		}
	case "timer.after":
		if a.DelayMs < 10 || a.DelayMs > 86400000 {
			return a, errors.New("delayMs must be 10–86400000")
		}
	case "audio.seek":
		if fields["positionMs"] == nil || a.PositionMs < 0 {
			return a, errors.New("nonnegative positionMs required")
		}
	case "audio.setVolume":
		if fields["volume"] == nil || a.Volume < 0 || a.Volume > 1 {
			return a, errors.New("volume must be 0–1")
		}
	case "files.pickDirectory":
		if a.Access != "read" && a.Access != "write" {
			return a, errors.New("access must be read or write")
		}
	case "files.revokeDirectory":
		if a.Root == "" || a.Root == "private" {
			return a, errors.New("directory grant required")
		}
	case "files.read", "files.write", "files.mkdir", "files.remove":
		if a.Path == "" || a.Path == "." {
			return a, errors.New("file path required")
		}
	}
	if a.Encoding != "" && a.Encoding != "utf8" && a.Encoding != "base64" {
		return a, errors.New("invalid file encoding")
	}
	return a, nil
}
