package v1

import v2 "quailcrate/pluginapi/v2"

// ViewLayout is shared by rendering, hit testing and action validation. A validated
// response prepares it once, before publishing the immutable view to UI threads.
type ViewLayout struct {
	Body               Control
	Header             []Control
	Leaves             []Control
	WheelUp, WheelDown string
}

func (c Control) ActionID() string {
	if c.Action != "" {
		return c.Action
	}
	return c.ID
}
func (i Item) Header() bool {
	return i.Role == "columnheader" || i.State == "header" || i.State == "header-weekend"
}

func (v *View) DisplayControls() []Control {
	if v.Root == nil {
		return v.Controls
	}
	return v.LayoutTree().Leaves
}
func (v *View) PrepareLayout() { v.layout = nil; v.layout = v.LayoutTree() }

func (v *View) LayoutTree() *ViewLayout {
	if v.layout != nil {
		return v.layout
	}
	l := &ViewLayout{}
	if v.Root != nil {
		l.WheelUp = v.Root.Events["wheelUp"]
		l.WheelDown = v.Root.Events["wheelDown"]
		l.Body = l.compile(v.Root)
		return l
	}
	l.Body = Control{Type: "column", Gap: 6}
	var row Control
	flush := func() {
		if len(row.Children) > 0 {
			l.Body.Children = append(l.Body.Children, row)
			row = Control{}
		}
	}
	for _, c := range v.Controls {
		if c.Type == "button" && c.ID == "today" {
			c.Variant = "accent"
		}
		if c.Type == "button" && c.ID == "add_menu" {
			c.Menu = "add"
		}
		if c.Type == "button" && c.ID == "open_menu" {
			c.Menu = "context"
		}
		if c.Type == "button" && c.ID == "previous" {
			l.WheelUp = c.ID
		}
		if c.Type == "button" && c.ID == "next" {
			l.WheelDown = c.ID
		}
		if c.Type == "text" && c.ID == "telemetry" {
			c.Variant = "caption"
		}
		l.Leaves = append(l.Leaves, c)
		if c.Type == "button" && c.Placement == "header" {
			l.Header = append(l.Header, c)
			continue
		}
		if c.Type == "button" {
			if len(row.Children) == 4 {
				flush()
			}
			row.Type = "row"
			row.Gap = 6
			c.Grow = 1
			row.Children = append(row.Children, c)
			continue
		}
		flush()
		if c.Type == "list" {
			c.Grow = 1
		}
		l.Body.Children = append(l.Body.Children, c)
		if c.Variant == "caption" && v.Progress > 0 {
			l.Body.Children = append(l.Body.Children, Control{Type: "progress", Height: 2, Value: v.Progress})
		}
	}
	flush()
	if l.WheelUp == "" || l.WheelDown == "" {
		l.WheelUp = ""
		l.WheelDown = ""
	}
	return l
}

func (l *ViewLayout) compile(n *v2.Node) Control {
	p := n.Props
	c := Control{ID: n.ID, Type: n.Type, Text: p.Text, Label: p.Label, Icon: p.Icon, Action: n.Events["activate"], Variant: p.Variant, Menu: p.Menu, Width: p.Width, Height: p.Height, Gap: p.Gap, Grow: p.Grow, Columns: p.Columns, Drop: p.Drop, Reorder: p.Reorder, HoverOnly: p.HoverOnly, Value: p.Value}
	if c.Type == "virtualList" {
		c.Type = "list"
	}
	if c.Type == "input" || c.Type == "slider" {
		c.Action = n.Events["submit"]
	}
	if c.Type == "button" && c.Text == "" {
		c.Text = c.Label
	}
	if len(p.Items) > 0 {
		c.Items = make([]Item, 0, len(p.Items))
		for _, i := range p.Items {
			state := i.Variant
			switch state {
			case "accent":
				state = "current"
			case "secondaryAccent":
				state = "weekend"
			case "warning":
				state = "error"
			}
			c.Items = append(c.Items, Item{ID: i.Key, Text: i.Text, Hint: i.Hint, Badge: i.Badge, Icon: i.Icon, State: state, Role: i.Role, Label: i.Label})
		}
	}
	if c.Type != "row" && c.Type != "column" && !(c.Type == "grid" && len(n.Children) > 0) {
		l.Leaves = append(l.Leaves, c)
	}
	for i := range n.Children {
		child := l.compile(&n.Children[i])
		if n.Children[i].Props.Placement == "header" {
			l.Header = append(l.Header, child.Children...)
		} else {
			c.Children = append(c.Children, child)
		}
	}
	return c
}

// Clone returns independent data for a surface. The model used to authorize a
// tab action must never share mutable controls with the UI.
func (v View) Clone() View {
	v.Root = v.Root.Clone()
	v.Controls = append([]Control(nil), v.Controls...)
	for i := range v.Controls {
		v.Controls[i].Items = append([]Item(nil), v.Controls[i].Items...)
	}
	v.Menu = append([]Control(nil), v.Menu...)
	v.Tabs = append([]WidgetTab(nil), v.Tabs...)
	v.PrepareLayout()
	return v
}
