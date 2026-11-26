// Package v2 defines the declarative plugin UI shared by Lua and native plugins.
package v2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
)

type Node struct {
	Type     string            `json:"type"`
	ID       string            `json:"id"`
	Props    Props             `json:"props,omitempty"`
	Events   map[string]string `json:"events,omitempty"`
	Children []Node            `json:"children,omitempty"`
}

type Props struct {
	Source      string  `json:"source,omitempty"`
	Field       string  `json:"field,omitempty"`
	VisibleRows int     `json:"visibleRows,omitempty"`
	Text        string  `json:"text,omitempty"`
	Label       string  `json:"label,omitempty"`
	Icon        string  `json:"icon,omitempty"`
	Variant     string  `json:"variant,omitempty"`
	Placement   string  `json:"placement,omitempty"`
	Menu        string  `json:"menu,omitempty"`
	Gap         int     `json:"gap,omitempty"`
	Grow        int     `json:"grow,omitempty"`
	Height      int     `json:"height,omitempty"`
	Width       int     `json:"width,omitempty"`
	Columns     int     `json:"columns,omitempty"`
	Value       float64 `json:"value,omitempty"`
	Items       []Item  `json:"items,omitempty"`
	HoverOnly   bool    `json:"hoverOnly,omitempty"`
	Drop        bool    `json:"drop,omitempty"`
	Reorder     bool    `json:"reorder,omitempty"`
}

type Item struct {
	Key     string `json:"key"`
	Text    string `json:"text"`
	Label   string `json:"label,omitempty"`
	Hint    string `json:"hint,omitempty"`
	Badge   string `json:"badge,omitempty"`
	Icon    string `json:"icon,omitempty"`
	Role    string `json:"role,omitempty"`
	Variant string `json:"variant,omitempty"`
}

func (props *Props) UnmarshalJSON(data []byte) error {
	type fields Props
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode((*fields)(props))
}

var localID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*(\.[a-zA-Z0-9][a-zA-Z0-9_-]*)*$`)

func ValidID(id string) bool { return len(id) <= 128 && localID.MatchString(id) }

func (root *Node) Validate() error {
	ids := map[string]bool{}
	count, items := 0, 0
	var visit func(*Node, string, int) error
	visit = func(n *Node, path string, depth int) error {
		count++
		if n == nil || depth > 32 || count > 512 {
			return fmt.Errorf("%s: tree exceeds bounds or root missing", path)
		}
		if !ValidID(n.ID) || ids[n.ID] {
			return fmt.Errorf("%s: invalid or duplicate id %q", path, n.ID)
		}
		ids[n.ID] = true
		p := n.Props
		collection := n.Type == "grid" || n.Type == "virtualList"
		template := n.Type == "virtualList" && p.Source == "results"
		container := n.Type == "row" || n.Type == "column" || n.Type == "grid" || template
		if p.Source != "" && !template || template && (len(n.Children) != 1 || n.Children[0].Type != "row" || len(p.Items) > 0) || p.VisibleRows < 0 || p.VisibleRows > 100 {
			return fmt.Errorf("%s: invalid list template", path)
		}
		input := n.Type == "slider" || n.Type == "input"
		if !container && !input && !slices.Contains([]string{"virtualList", "text", "icon", "button", "progress", "searchInput"}, n.Type) {
			return fmt.Errorf("%s: unknown type %q", path, n.Type)
		}
		if !container && len(n.Children) > 0 || !collection && len(p.Items) > 0 || len(n.Children) > 0 && len(p.Items) > 0 || n.Type == "grid" && p.Columns < 1 || p.Columns < 0 || p.Columns > 32 || p.Columns > 0 && !collection {
			return fmt.Errorf("%s: invalid children, items or columns", path)
		}
		if p.Gap < 0 || p.Gap > 128 || p.Grow < 0 || p.Grow > 1024 || p.Width < 0 || p.Height < 0 || p.Width > 4096 || p.Height > 4096 || p.Value < 0 || p.Value > 1 || math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			return fmt.Errorf("%s: invalid dimensions or progress", path)
		}
		if p.Placement != "" && (n.Type != "row" || p.Placement != "header" || depth != 2) || p.Menu != "" && (n.Type != "button" || !ValidID(p.Menu)) || (p.Drop || p.Reorder) && n.Type != "virtualList" {
			return fmt.Errorf("%s: invalid placement, menu or drag behavior", path)
		}
		if p.Placement == "header" {
			for _, child := range n.Children {
				if child.Type != "button" {
					return fmt.Errorf("%s: widget header actions must be buttons", path)
				}
			}
		}
		for event, action := range n.Events {
			ok := event == "activate" && (n.Type == "button" || collection) || event == "contextMenu" && template || (event == "wheelUp" || event == "wheelDown") && depth == 1 || n.Type == "searchInput" && (event == "change" || event == "submit")
			if (!ok && !(input && event == "submit")) || !ValidID(action) {
				return fmt.Errorf("%s: invalid binding %q = %q", path, event, action)
			}
		}
		if input && n.Events["submit"] == "" {
			return fmt.Errorf("%s: input requires a submit binding", path)
		}
		if n.Type == "button" && (strings.TrimSpace(p.Label) == "" && strings.TrimSpace(p.Text) == "" || (n.Events["activate"] == "") == (p.Menu == "")) {
			return fmt.Errorf("%s: button requires a name and one activation or menu", path)
		}
		if collection && len(p.Items) > 0 && n.Events["activate"] == "" {
			return fmt.Errorf("%s: collection needs an activation binding", path)
		}
		if p.Value != 0 && n.Type != "progress" && n.Type != "slider" || p.HoverOnly && n.Type != "button" || p.Field != "" && n.Type != "text" && n.Type != "icon" || p.VisibleRows != 0 && !template {
			return fmt.Errorf("%s: property is unsupported by %s", path, n.Type)
		}
		if !slices.Contains([]string{"", "accent", "muted", "caption", "warning"}, p.Variant) {
			return fmt.Errorf("%s: unknown variant %q", path, p.Variant)
		}
		keys := map[string]bool{}
		items += len(p.Items)
		if len(p.Items) > 2048 || items > 8192 {
			return fmt.Errorf("%s: too many items", path)
		}
		for _, item := range p.Items {
			if item.Key == "" || len(item.Key) > 512 || keys[item.Key] || item.Role != "" && item.Role != "columnheader" || !slices.Contains([]string{"", "accent", "selected", "muted", "secondaryAccent", "warning"}, item.Variant) {
				return fmt.Errorf("%s: invalid item %q", path, item.Key)
			}
			keys[item.Key] = true
		}
		for i := range n.Children {
			if err := visit(&n.Children[i], fmt.Sprintf("%s.children[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(root, "root", 1)
}

func (n *Node) Clone() *Node {
	if n == nil {
		return nil
	}
	out := *n
	out.Props.Items = slices.Clone(n.Props.Items)
	if n.Events != nil {
		out.Events = make(map[string]string, len(n.Events))
		for k, v := range n.Events {
			out.Events[k] = v
		}
	}
	out.Children = slices.Clone(n.Children)
	for i := range out.Children {
		out.Children[i] = *n.Children[i].Clone()
	}
	return &out
}

// ValidateView checks the capabilities of widget and application surfaces.
// Query input and externally bound results belong to the launcher surface.
func (n *Node) ValidateView() error {
	if err := n.Validate(); err != nil {
		return err
	}
	var visit func(*Node) error
	visit = func(node *Node) error {
		p := node.Props
		if node.Type == "searchInput" || p.Source != "" || p.Field != "" || p.VisibleRows != 0 {
			return fmt.Errorf("node %q uses a launcher-only binding", node.ID)
		}
		for i := range node.Children {
			if err := visit(&node.Children[i]); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(n)
}
