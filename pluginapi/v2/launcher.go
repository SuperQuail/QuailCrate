package v2

import (
	"errors"
	"fmt"
	"strings"
)

// Launcher registers a hotkey surface. Events bind to the host's search broker.
// The shell never receives another provider's command arguments.
type Launcher struct {
	UIVersion int    `json:"uiVersion"`
	Title     string `json:"title"`
	Root      *Node  `json:"root"`
	Indexing  string `json:"indexing,omitempty"`
	NoResults string `json:"noResults,omitempty"`
}

func (l *Launcher) Validate() error {
	if l.UIVersion != 2 || strings.TrimSpace(l.Title) == "" || len(l.Title) > 256 || strings.ContainsAny(l.Title, "\x00\r\n") {
		return errors.New("launcher requires uiVersion 2 and a window title")
	}
	if err := l.Root.Validate(); err != nil {
		return err
	}
	if l.Root.Type != "column" || l.Root.Props.Width < 240 || l.Root.Props.Width > 2048 || len(l.Root.Children) > 12 || len(l.Root.Events) != 0 {
		return errors.New("launcher root must be a column with width 240..2048")
	}
	inputs, lists := 0, 0
	for _, child := range l.Root.Children {
		switch child.Type {
		case "searchInput":
			inputs++
			if child.Events["change"] != "search.query" || child.Events["submit"] != "" && child.Events["submit"] != "search.open" || child.Props.Height < 40 || child.Props.Height > 128 {
				return fmt.Errorf("launcher input %q requires search.query, a height 40..128 and optional search.open", child.ID)
			}
		case "virtualList":
			lists++
			if child.Props.Source != "results" || child.Props.VisibleRows < 1 || child.Props.VisibleRows > 16 || child.Events["activate"] != "search.open" || child.Events["contextMenu"] != "" && child.Events["contextMenu"] != "search.actions" {
				return fmt.Errorf("launcher list %q requires results, visibleRows 1..16 and search.open", child.ID)
			}
			row := child.Children[0]
			if row.Props.Height < 24 || row.Props.Height > 128 {
				return fmt.Errorf("launcher row %q requires height 24..128", row.ID)
			}
			icons := 0
			if err := validateResultTemplate(row, &icons); err != nil {
				return err
			}
		case "text":
			if child.Props.Height < 12 || child.Props.Height > 128 {
				return fmt.Errorf("launcher text %q requires height 12..128", child.ID)
			}
		default:
			return fmt.Errorf("unsupported launcher child %q", child.Type)
		}
	}
	if inputs != 1 || lists != 1 {
		return errors.New("launcher requires exactly one searchInput and one results list")
	}
	return nil
}

func validateResultTemplate(n Node, icons *int) error {
	switch n.Type {
	case "row", "column":
		if len(n.Children) == 0 {
			return fmt.Errorf("empty launcher template container %q", n.ID)
		}
		for _, child := range n.Children {
			if err := validateResultTemplate(child, icons); err != nil {
				return err
			}
		}
	case "text":
		switch n.Props.Field {
		case "", "title", "subtitle", "badge", "selection":
		default:
			return fmt.Errorf("unknown result field %q", n.Props.Field)
		}
	case "icon":
		*icons++
		if n.Props.Field != "icon" || *icons > 1 {
			return errors.New("a result template supports one result icon")
		}
	default:
		return fmt.Errorf("unsupported result template control %q", n.Type)
	}
	return nil
}
