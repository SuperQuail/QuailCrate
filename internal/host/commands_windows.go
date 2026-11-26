package host

import (
	"strings"

	"quailcrate/internal/pluginapps"
	v1 "quailcrate/pluginapi/v1"
)

// ErrInvocationUnavailable means that the requested target is no longer available.
var ErrInvocationUnavailable = pluginapps.ErrUnavailable

// InvokeListedCommand runs a public command from the indicated catalog snapshot.
// A successful call may still be waiting for a picker or another user interaction.
func (h *Host) InvokeListedCommand(pluginID, commandID string, generation uint64) error {
	h.mu.Lock()
	cmd, ok := h.appCatalog.ResolveCommand(pluginID, commandID)
	h.mu.Unlock()
	if !ok || cmd.Generation != generation || generation == 0 {
		return ErrInvocationUnavailable
	}
	return h.invoke(pluginID, generation, "command", v1.Payload{CommandID: commandID})
}

func parseCommandQuery(query string) (term string, commandOnly bool) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return "", false
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, ">") {
		return strings.TrimSpace(trimmed[1:]), true
	}
	return trimmed, false
}

func matchCommandScore(cmd pluginapps.Command, term string, commandOnly bool) float64 {
	if term == "" {
		if commandOnly {
			return 0.85
		}
		return 0
	}
	q := strings.ToLower(term)
	title := strings.ToLower(cmd.Title)
	id := strings.ToLower(cmd.ID)
	plugin := strings.ToLower(cmd.PluginName)

	best := 0.0
	set := func(v float64) {
		if v > best {
			best = v
		}
	}
	switch {
	case title == q:
		set(0.95)
	case strings.HasPrefix(title, q):
		set(0.88)
	case strings.Contains(title, q):
		set(0.78)
	}
	switch {
	case id == q:
		set(0.85)
	case strings.HasPrefix(id, q):
		set(0.76)
	case strings.Contains(id, q):
		set(0.66)
	}
	switch {
	case plugin != "" && plugin == q:
		set(0.74)
	case plugin != "" && strings.HasPrefix(plugin, q):
		set(0.70)
	case plugin != "" && strings.Contains(plugin, q):
		set(0.62)
	}
	if best == 0 {
		tokens := strings.Fields(q)
		if len(tokens) > 1 {
			haystack := title + " " + plugin + " " + id
			allMatch := true
			for _, tok := range tokens {
				if !strings.Contains(haystack, tok) {
					allMatch = false
					break
				}
			}
			if allMatch {
				set(0.60)
			}
		}
	}
	return best
}

func (h *Host) searchPublicCommands(term string, commandOnly bool) []searchResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.appCatalog.Commands) == 0 {
		return nil
	}
	fallbackSubtitle := text(h.locale, "Command", "指令")
	byPlugin := make(map[string]int)
	var responses []searchResponse
	for _, cmd := range h.appCatalog.Commands {
		rec := h.records[cmd.PluginID]
		if rec == nil || rec.generation == 0 || rec.generation != cmd.Generation {
			continue
		}
		score := matchCommandScore(cmd, term, commandOnly)
		if score <= 0 {
			continue
		}
		subtitle := cmd.PluginName
		if subtitle == "" {
			subtitle = fallbackSubtitle
		}
		icon := ""
		if v1.IsIconName(rec.info.Icon) {
			icon = rec.info.Icon
		}
		res := v1.Result{
			ID:       "command:" + cmd.PluginID + ":" + cmd.ID,
			Title:    cmd.Title,
			Subtitle: subtitle,
			Icon:     icon,
			Score:    score,
			Command:  v1.CommandRef{ID: cmd.ID},
		}
		idx, ok := byPlugin[cmd.PluginID]
		if !ok {
			idx = len(responses)
			byPlugin[cmd.PluginID] = idx
			responses = append(responses, searchResponse{id: cmd.PluginID, generation: cmd.Generation})
		}
		responses[idx].results = append(responses[idx].results, res)
	}
	return responses
}
