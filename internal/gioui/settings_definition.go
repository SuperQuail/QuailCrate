package gioui

import (
	"image"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"
	v2 "quailcrate/pluginapi/v2"
)

func (f settingsFrame) heading(zh, en string) string {
	if f.cardTitle != "" {
		return f.cardTitle
	}
	return f.tr(zh, en)
}

func (f settingsFrame) sections() []v2.SettingsSection {
	if f.model.Definition != nil {
		return f.model.Definition.Sections
	}
	icons := [...]string{"settings", "palette", "desktop", "command", "code"}
	titles := f.navTitles()
	out := make([]v2.SettingsSection, len(settingsPageIDs))
	for i, id := range settingsPageIDs {
		out[i] = v2.SettingsSection{ID: id, Title: titles[i], Icon: icons[i]}
	}
	return out
}

func (f settingsFrame) navButton(id string) *widget.Clickable {
	for i, known := range settingsPageIDs {
		if known == id {
			return &f.nav[i]
		}
	}
	if f.extraNav[id] == nil {
		f.extraNav[id] = new(widget.Clickable)
	}
	return f.extraNav[id]
}

func (f settingsFrame) sectionForCard(kind, fallback string) string {
	if def := f.model.Definition; def != nil {
		for _, sec := range def.Sections {
			for _, card := range sec.Cards {
				if card.Type == kind {
					return sec.ID
				}
			}
		}
		return f.CurrentPage()
	}
	return fallback
}

// Keep each editor and its input state on the settings window. Plugins choose
// the page and order in which these controls appear.
func (f settingsFrame) declaredCards(gtx layout.Context, section v2.SettingsSection) {
	if f.sectionListPage != section.ID {
		f.sectionList.Position = layout.Position{}
		f.sectionListPage = section.ID
	}
	height := gtx.Constraints.Max.Y
	pad := f.contentPadding(gtx)
	f.list(gtx, &f.sectionList, len(section.Cards), func(gtx layout.Context, index int) layout.Dimensions {
		dims := f.paddedCard(gtx, pad, func(gtx layout.Context) layout.Dimensions {
			card := section.Cards[index]
			f.cardTitle = card.Title
			p, w := gtx.Dp, gtx.Constraints.Max.X
			cardH := 0
			switch card.Type {
			case "hotkey":
				cardH = f.hotkeyCard(gtx)
			case "language":
				f.languageCard(gtx)
				cardH = p(126)
			case "desktopLayout":
				f.desktop(gtx)
				cardH = p(104)
				if w < p(530) {
					cardH = p(138)
				}
			case "commands":
				usable := max(p(120), height)
				settingsArea(gtx, image.Rect(0, 0, w, usable), f.commands)
				cardH = min(usable, p(50)+len(f.model.Commands)*p(58))
				if len(f.model.Commands) == 0 {
					cardH = min(usable, p(156))
				}
			case "target", "plugins":
				// These composites start with their interactive content. Give their
				// plugin-supplied heading a separate row.
				titleH := p(30)
				f.label(gtx, image.Rect(0, 0, w, titleH), card.Title, 15, font.SemiBold, f.p.Text, layout.W)
				usable := max(p(120), height-titleH)
				switch card.Type {
				case "target":
					var d layout.Dimensions
					settingsArea(gtx, image.Rect(0, titleH, w, titleH+p(240)), func(gtx layout.Context) { d = f.appearanceCard(gtx, card.Type) })
					cardH = titleH + d.Size.Y
				case "plugins":
					settingsArea(gtx, image.Rect(0, titleH, w, titleH+usable), f.plugins)
					rows := 0
					for _, plugin := range f.model.Plugins {
						rows += f.pluginHeight(gtx, plugin)
					}
					if rows == 0 {
						rows = p(70)
					}
					cardH = titleH + min(usable, p(87)+rows)
				}
			default:
				return f.appearanceCard(gtx, card.Type)
			}
			return layout.Dimensions{Size: image.Pt(w, cardH+p(12))}
		})
		if index == len(section.Cards)-1 {
			dims.Size.Y += gtx.Dp(12)
		}
		return dims
	})
}
