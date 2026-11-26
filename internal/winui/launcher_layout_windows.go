package winui

import (
	"slices"
	"unsafe"

	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

type LauncherShell struct {
	PluginID, Name string
	View           *v2.Launcher
}

func ResolveLauncher(shells []LauncherShell, preferred string) LauncherShell {
	var active LauncherShell
	for _, shell := range shells {
		if active.View == nil || shell.PluginID == "quailcrate.launcher" {
			active = shell
		}
		if shell.PluginID == preferred {
			active = shell
			break
		}
	}
	return active
}

func (u *UI) SetLaunchers(shells []LauncherShell, active LauncherShell) {
	u.post(func() {
		u.launchersReady = true
		u.launcherShells = shells
		if u.launcherShell.View != active.View {
			u.launcherShell = active
			u.results = nil
			u.resultIndex, u.resultScroll, u.launcherWheelRemainder = 0, 0, 0
			if active.View == nil {
				u.closeLauncher(true)
				u.launcherLayout = overlayLayout{}
			} else if u.launcher != 0 {
				u.sizeLauncher(false)
				u.refreshLauncherLabels()
				if u.launcherOpen {
					u.emitSearch()
				}
			}
		}
		if u.launcherRequested {
			u.launcherRequested = false
			u.showLauncher()
		}
	})
}

func (u *UI) pickLauncher() {
	options := make([]v1.Option, 0, len(u.launcherShells))
	for _, shell := range u.launcherShells {
		options = append(options, v1.Option{ID: shell.PluginID, Text: shell.Name, Selected: u.launcherShell.PluginID == shell.PluginID})
	}
	u.PickOptions(u.label("选择快速启动界面", "Choose launcher interface"), options, false, func(ids []string, ok bool) {
		if ok && len(ids) == 1 && u.cb.ChooseLauncher != nil {
			u.cb.ChooseLauncher(ids[0])
		}
	})
}

type overlayPart struct {
	node *v2.Node
	box  rect
}

type overlayLayout struct {
	height        int32
	input, list   overlayPart
	rowHeight     int32
	visible       int
	labels, cells []overlayPart
	icon          rect
}

// prepare uses device pixels for hit testing and painting. The row template is
// laid out once and then translated for each visible result during a paint.
func (l *overlayLayout) prepare(root *v2.Node, width, maxHeight, dpi int32, count int) {
	l.labels, l.cells = l.labels[:0], l.cells[:0]
	l.icon = rect{}
	fixedHeight := int32(len(root.Children)-1) * scale(int32(root.Props.Gap), dpi)
	for _, n := range root.Children {
		if n.Type != "virtualList" {
			fixedHeight += scale(int32(n.Props.Height), dpi)
		}
	}
	y := int32(0)
	for i := range root.Children {
		n := &root.Children[i]
		if i > 0 {
			y += scale(int32(root.Props.Gap), dpi)
		}
		height := scale(int32(n.Props.Height), dpi)
		if n.Type == "virtualList" {
			l.visible = n.Props.VisibleRows
			l.rowHeight = scale(int32(n.Children[0].Props.Height), dpi)
			l.visible = min(l.visible, max(1, int((maxHeight-fixedHeight)/l.rowHeight)))
			height = int32(max(1, min(count, l.visible))) * l.rowHeight
		}
		p := overlayPart{n, rect{0, y, width, y + height}}
		switch n.Type {
		case "searchInput":
			l.input = p
		case "text":
			l.labels = append(l.labels, p)
		case "virtualList":
			l.list = p
			pad := scale(24, dpi)
			l.template(&n.Children[0], rect{pad, y + scale(4, dpi), max(pad, width-pad), y + l.rowHeight - scale(4, dpi)}, dpi)
		}
		y += height
	}
	l.height = y
}

func (l *overlayLayout) template(n *v2.Node, box rect, dpi int32) {
	if n.Type != "row" && n.Type != "column" {
		l.cells = append(l.cells, overlayPart{n, box})
		if n.Type == "icon" {
			l.icon = box
		}
		return
	}
	horizontal := n.Type == "row"
	extent := box.height()
	if horizontal {
		extent = box.width()
	}
	gap := scale(int32(n.Props.Gap), dpi)
	space := max(0, extent-int32(len(n.Children)-1)*gap)
	totalGrow := int32(0)
	for i := range n.Children {
		p := n.Children[i].Props
		size := p.Height
		if horizontal {
			size = p.Width
		}
		if size > 0 {
			space -= scale(int32(size), dpi)
		} else {
			totalGrow += int32(max(1, p.Grow))
		}
	}
	space = max(0, space)
	pos := int32(0)
	for i := range n.Children {
		child := &n.Children[i]
		size, cross := child.Props.Height, child.Props.Width
		if horizontal {
			size, cross = cross, size
		}
		dim := scale(int32(size), dpi)
		if size == 0 {
			weight := int32(max(1, child.Props.Grow))
			dim = space * weight / totalGrow
			space -= dim
			totalGrow -= weight
		}
		end := min(extent, pos+dim)
		b := box
		if horizontal {
			b.Left, b.Right = box.Left+pos, box.Left+end
			if cross > 0 {
				h := min(box.height(), scale(int32(cross), dpi))
				b.Top += (box.height() - h) / 2
				b.Bottom = b.Top + h
			}
		} else {
			b.Top, b.Bottom = box.Top+pos, box.Top+end
			if cross > 0 {
				b.Right = b.Left + min(b.width(), scale(int32(cross), dpi))
			}
		}
		if end > pos {
			l.template(child, b, dpi)
		}
		pos = min(extent, end+gap)
	}
}

func (l *overlayLayout) row(client rect, dpi int32, slot int) rect {
	y := l.list.box.Top + int32(slot)*l.rowHeight
	pad := scale(12, dpi)
	return rect{pad, y + scale(2, dpi), client.Right - pad, y + l.rowHeight - scale(2, dpi)}
}

func (u *UI) resultAt(p point) int {
	l := &u.launcherLayout
	if l.rowHeight <= 0 || p.Y < l.list.box.Top || p.Y >= l.list.box.Bottom {
		return -1
	}
	slot := int((p.Y - l.list.box.Top) / l.rowHeight)
	if slot >= l.visible || !l.row(l.list.box, u.launcherDPI, slot).contains(p) {
		return -1
	}
	return u.resultScroll + slot
}

func (u *UI) resultMenu(index int, anchor point) {
	if !u.launcherOpen || index < 0 || index >= len(u.results) || u.launcherLayout.list.node.Events["contextMenu"] != "search.actions" {
		return
	}
	result := u.results[index]
	shell := u.launcherShell.View
	if len(result.Actions) == 0 {
		return
	}
	u.selectResult(index - u.resultIndex)
	menu, _, _ := createPopupMenu.Call()
	if menu == 0 {
		return
	}
	defer destroyMenu.Call(menu)
	for i, action := range result.Actions {
		appendMenu.Call(menu, 0, uintptr(i+1), uintptr(unsafe.Pointer(wstr(action.Title))))
	}
	cmd, _, _ := trackPopupMenu.Call(menu, 0x102, coord(anchor.X), coord(anchor.Y), 0, u.launcher, 0)
	if cmd == 0 || cmd > uintptr(len(result.Actions)) || u.cb.ResultAction == nil {
		return
	}
	// The native menu pumps messages. A replacement query or a disabled shell
	// may have withdrawn the result while the menu was open.
	if !u.launcherOpen || u.launcherShell.View != shell || !slices.ContainsFunc(u.results, func(r Result) bool {
		return r.PluginID == result.PluginID && r.ID == result.ID && r.Revision == result.Revision && r.Generation == result.Generation
	}) {
		return
	}
	u.hideLauncher(false)
	u.cb.ResultAction(result, result.Actions[cmd-1])
}
