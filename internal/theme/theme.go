// Package theme defines QuailCrate's geek color presets, per-component overrides,
// and opacity configuration shared by host, Gio UI, and Win32 rendering.
package theme

import (
	"encoding/json"
	"fmt"
	"image/color"
	"strings"
)

const (
	ComponentGlobal   = "global"
	ComponentCalendar = "quailcrate.calendar"
	ComponentGroups   = "quailcrate.desktop-groups"
	ComponentLauncher = "launcher"
	ComponentSettings = "settings"

	DefaultPresetID        = "catppuccin-mocha"
	DefaultWidgetOpacity   = 78
	DefaultLauncherOpacity = 94
	MinLauncherOpacity     = 35

	DefaultCornerRadius = 12
	MinCornerRadius     = 0
	MaxCornerRadius     = 20

	DefaultIconSize = 40
	MinIconSize     = 22
	MaxIconSize     = 72

	GlassSmoked    = "smoked"
	GlassWireframe = "wireframe"
	GlassMinimal   = "minimal"
)

// ColorSlot identifies an editable semantic color token.
type ColorSlot string

const (
	SlotBackground ColorSlot = "background"
	SlotSurface    ColorSlot = "surface"
	SlotBorder     ColorSlot = "border"
	SlotText       ColorSlot = "text"
	SlotSecondary  ColorSlot = "secondary"
	SlotAccent     ColorSlot = "accent"
	SlotWeekend    ColorSlot = "weekend"
)

// EditableSlots defines the presentation order of editable color tokens.
var EditableSlots = []ColorSlot{
	SlotBackground,
	SlotSurface,
	SlotBorder,
	SlotText,
	SlotSecondary,
	SlotAccent,
	SlotWeekend,
}

// Palette holds the fully resolved semantic colors for a UI surface.
type Palette struct {
	Background color.NRGBA
	Surface    color.NRGBA
	Sidebar    color.NRGBA
	Border     color.NRGBA
	Text       color.NRGBA
	Secondary  color.NRGBA
	Accent     color.NRGBA
	Selected   color.NRGBA
	Hover      color.NRGBA
	OnAccent   color.NRGBA
	Weekend    color.NRGBA
	Warning    color.NRGBA
	WarningBg  color.NRGBA
}

// IsDark reports whether the palette's surface has dark luminance.
func (p Palette) IsDark() bool {
	return Luminance(p.Surface) < 0.45
}

// SlotColor returns the resolved color for a given editable slot.
func (p Palette) SlotColor(slot ColorSlot) color.NRGBA {
	switch slot {
	case SlotBackground:
		return p.Background
	case SlotSurface:
		return p.Surface
	case SlotBorder:
		return p.Border
	case SlotText:
		return p.Text
	case SlotSecondary:
		return p.Secondary
	case SlotAccent:
		return p.Accent
	case SlotWeekend:
		return p.Weekend
	default:
		return p.Surface
	}
}

// Preset describes a built-in theme preset.
type Preset struct {
	ID      string
	Name    string
	NameZH  string
	Tag     string
	Group   string // "neutral", "mood", or "light"
	Palette Palette
}

// PartialPalette stores optional per-token #RRGGBB hex overrides.
type PartialPalette struct {
	Background string `json:"background,omitempty"`
	Surface    string `json:"surface,omitempty"`
	Border     string `json:"border,omitempty"`
	Text       string `json:"text,omitempty"`
	Secondary  string `json:"secondary,omitempty"`
	Accent     string `json:"accent,omitempty"`
	Weekend    string `json:"weekend,omitempty"`
}

func (p PartialPalette) IsEmpty() bool {
	return p == (PartialPalette{})
}

func (p PartialPalette) Get(slot ColorSlot) string {
	switch slot {
	case SlotBackground:
		return p.Background
	case SlotSurface:
		return p.Surface
	case SlotBorder:
		return p.Border
	case SlotText:
		return p.Text
	case SlotSecondary:
		return p.Secondary
	case SlotAccent:
		return p.Accent
	case SlotWeekend:
		return p.Weekend
	default:
		return ""
	}
}

func (p *PartialPalette) Set(slot ColorSlot, hex string) {
	switch slot {
	case SlotBackground:
		p.Background = hex
	case SlotSurface:
		p.Surface = hex
	case SlotBorder:
		p.Border = hex
	case SlotText:
		p.Text = hex
	case SlotSecondary:
		p.Secondary = hex
	case SlotAccent:
		p.Accent = hex
	case SlotWeekend:
		p.Weekend = hex
	}
}

// ComponentOverride holds optional per-component palette and opacity customization.
type ComponentOverride struct {
	Custom  bool           `json:"custom,omitempty"`
	Preset  string         `json:"preset,omitempty"`
	Colors  PartialPalette `json:"colors,omitempty"`
	Opacity *int           `json:"opacity,omitempty"`
}

// Config is the persisted appearance configuration stored under appearance.config.
type Config struct {
	Preset         string                       `json:"preset"`
	GlassStyle     string                       `json:"glassStyle,omitempty"`
	GlobalColors   PartialPalette               `json:"globalColors,omitempty"`
	WidgetOpacity  int                          `json:"widgetOpacity"`
	CornerRadius   *int                         `json:"cornerRadius,omitempty"`
	IconSize       *int                         `json:"iconSize,omitempty"`
	TitleAccentBar bool                         `json:"titleAccentBar,omitempty"`
	Components     map[string]ComponentOverride `json:"components,omitempty"`
}

// Resolved contains the effective palette, opacity, glass style, corner radius, icon size, and mode for a component.
type Resolved struct {
	Palette        Palette
	Opacity        int
	GlassStyle     string
	CornerRadius   int
	IconSize       int
	TitleAccentBar bool
	IsDark         bool
	Custom         bool
}

// ScaleRadius scales a design-time reference radius (for DefaultCornerRadius = 12dp)
// proportionally to the user's configured global corner radius (0..20dp).
// A corner radius of 0 collapses every derived radius cleanly to 0 (sharp corners).
func ScaleRadius(cornerRadius, baseDp int) int {
	r := clampInt(cornerRadius, MinCornerRadius, MaxCornerRadius)
	if r == 0 || baseDp <= 0 {
		return 0
	}
	scaled := (baseDp*r + DefaultCornerRadius/2) / DefaultCornerRadius
	if scaled < 1 {
		return 1
	}
	return scaled
}

// NormalizeGlassStyle returns a valid glass style ID, defaulting to GlassSmoked.
func NormalizeGlassStyle(style string) string {
	switch style {
	case GlassWireframe, GlassMinimal:
		return style
	default:
		return GlassSmoked
	}
}

var builtInPresets = []Preset{
	// === A. 旗舰深色 · 高级中性 (Modern Neutral Dark) ===
	{
		ID:     "catppuccin-mocha",
		Name:   "Catppuccin Mocha",
		NameZH: "摩卡暗夜",
		Tag:    "MOCHA",
		Group:  "neutral",
		Palette: buildPalette(
			mustHex("#11111B"), // Background
			mustHex("#1E1E2E"), // Surface
			mustHex("#171723"), // Sidebar
			mustHex("#2C2D40"), // Border
			mustHex("#D8E0F8"), // Text
			mustHex("#9399B2"), // Secondary
			mustHex("#CBA6F7"), // Accent
			mustHex("#FAB387"), // Weekend
			mustHex("#F38BA8"), // Warning
		),
	},
	{
		ID:     "linear-obsidian",
		Name:   "Linear Obsidian",
		NameZH: "线性曜石",
		Tag:    "OBSID",
		Group:  "neutral",
		Palette: buildPalette(
			mustHex("#0D0E12"), // Background
			mustHex("#181A22"), // Surface
			mustHex("#121319"), // Sidebar
			mustHex("#252935"), // Border
			mustHex("#ECEEF4"), // Text
			mustHex("#8A90A0"), // Secondary
			mustHex("#8E9BF7"), // Accent
			mustHex("#B9A7F5"), // Weekend
			mustHex("#E86E7D"), // Warning
		),
	},
	{
		ID:     "graphite-mono",
		Name:   "Graphite Mono",
		NameZH: "石墨炭影",
		Tag:    "MONO",
		Group:  "neutral",
		Palette: buildPalette(
			mustHex("#121214"), // Background
			mustHex("#1E1E22"), // Surface
			mustHex("#17171A"), // Sidebar
			mustHex("#2C2C33"), // Border
			mustHex("#F2F2F4"), // Text
			mustHex("#8F8F99"), // Secondary
			mustHex("#A8BACB"), // Accent
			mustHex("#D6C6B2"), // Weekend
			mustHex("#E06C75"), // Warning
		),
	},
	{
		ID:     "midnight-slate",
		Name:   "Midnight Slate",
		NameZH: "午夜青岚",
		Tag:    "SLATE",
		Group:  "neutral",
		Palette: buildPalette(
			mustHex("#10141B"), // Background
			mustHex("#1B222E"), // Surface
			mustHex("#151A23"), // Sidebar
			mustHex("#293345"), // Border
			mustHex("#E7EEF8"), // Text
			mustHex("#8B9BB2"), // Secondary
			mustHex("#7BB5E8"), // Accent
			mustHex("#9ED6C8"), // Weekend
			mustHex("#EB7B8B"), // Warning
		),
	},

	// === B. 氛围深色 · 艺术与自然 (Atmospheric Dark) ===
	{
		ID:     "rose-pine",
		Name:   "Rosé Pine",
		NameZH: "玫瑰松影",
		Tag:    "ROSE",
		Group:  "mood",
		Palette: buildPalette(
			mustHex("#14121D"), // Background
			mustHex("#211E30"), // Surface
			mustHex("#1A1726"), // Sidebar
			mustHex("#312D45"), // Border
			mustHex("#EAE6F6"), // Text
			mustHex("#9893B0"), // Secondary
			mustHex("#EBBCBA"), // Accent
			mustHex("#C4A7E7"), // Weekend
			mustHex("#EB6F92"), // Warning
		),
	},
	{
		ID:     "kanagawa-wave",
		Name:   "Kanagawa Wave",
		NameZH: "神奈川浪",
		Tag:    "WAVE",
		Group:  "mood",
		Palette: buildPalette(
			mustHex("#16161D"), // Background
			mustHex("#22222E"), // Surface
			mustHex("#1B1B24"), // Sidebar
			mustHex("#313143"), // Border
			mustHex("#E0DBBE"), // Text
			mustHex("#96927F"), // Secondary
			mustHex("#7E9CD8"), // Accent
			mustHex("#D27E99"), // Weekend
			mustHex("#E46876"), // Warning
		),
	},
	{
		ID:     "everforest-dusk",
		Name:   "Everforest Dusk",
		NameZH: "深林苔雾",
		Tag:    "PINE",
		Group:  "mood",
		Palette: buildPalette(
			mustHex("#171C19"), // Background
			mustHex("#232B27"), // Surface
			mustHex("#1C221F"), // Sidebar
			mustHex("#323E38"), // Border
			mustHex("#E4E0CC"), // Text
			mustHex("#96A297"), // Secondary
			mustHex("#A7C080"), // Accent
			mustHex("#DBBC7F"), // Weekend
			mustHex("#E67E80"), // Warning
		),
	},
	{
		ID:     "velvet-espresso",
		Name:   "Velvet Espresso",
		NameZH: "浓缩可可",
		Tag:    "WARM",
		Group:  "mood",
		Palette: buildPalette(
			mustHex("#141210"), // Background
			mustHex("#231F1C"), // Surface
			mustHex("#1B1815"), // Sidebar
			mustHex("#342E29"), // Border
			mustHex("#F0E7DB"), // Text
			mustHex("#A4988A"), // Secondary
			mustHex("#E0A96D"), // Accent
			mustHex("#DC9176"), // Weekend
			mustHex("#E06C6C"), // Warning
		),
	},
	{
		ID:     "nebula-velvet",
		Name:   "Nebula Velvet",
		NameZH: "星云紫雾",
		Tag:    "DUSK",
		Group:  "mood",
		Palette: buildPalette(
			mustHex("#12101A"), // Background
			mustHex("#201C2E"), // Surface
			mustHex("#181523"), // Sidebar
			mustHex("#312B46"), // Border
			mustHex("#ECE8FA"), // Text
			mustHex("#968FAF"), // Secondary
			mustHex("#B095F9"), // Accent
			mustHex("#E8A5C8"), // Weekend
			mustHex("#EE7490"), // Warning
		),
	},
	{
		ID:     "abyssal-teal",
		Name:   "Abyssal Teal",
		NameZH: "松石海渊",
		Tag:    "TEAL",
		Group:  "mood",
		Palette: buildPalette(
			mustHex("#0F1618"), // Background
			mustHex("#1A262A"), // Surface
			mustHex("#141D20"), // Sidebar
			mustHex("#28393F"), // Border
			mustHex("#E4F1F3"), // Text
			mustHex("#88A1A8"), // Secondary
			mustHex("#66C7BC"), // Accent
			mustHex("#8ABCE6"), // Weekend
			mustHex("#E87A82"), // Warning
		),
	},
	{
		ID:     "crimson-dusk",
		Name:   "Crimson Dusk",
		NameZH: "暮樱珊瑚",
		Tag:    "RUBY",
		Group:  "mood",
		Palette: buildPalette(
			mustHex("#131114"), // Background
			mustHex("#211D24"), // Surface
			mustHex("#19161B"), // Sidebar
			mustHex("#332D38"), // Border
			mustHex("#F3EDF1"), // Text
			mustHex("#9F949E"), // Secondary
			mustHex("#E25D75"), // Accent
			mustHex("#E8B084"), // Weekend
			mustHex("#F06478"), // Warning
		),
	},

	// === C. 通透浅色 · 纸感与瓷白 (Crisp Modern Light) ===
	{
		ID:     "alabaster-mist",
		Name:   "Alabaster Mist",
		NameZH: "云石霜白",
		Tag:    "MIST",
		Group:  "light",
		Palette: buildPalette(
			mustHex("#F4F6FA"), // Background
			mustHex("#FFFFFF"), // Surface
			mustHex("#ECEFF6"), // Sidebar
			mustHex("#E2E7F0"), // Border
			mustHex("#1A1F2E"), // Text
			mustHex("#646F84"), // Secondary
			mustHex("#4F5FD9"), // Accent
			mustHex("#8D5CE2"), // Weekend
			mustHex("#D6455D"), // Warning
		),
	},
	{
		ID:     "catppuccin-latte",
		Name:   "Catppuccin Latte",
		NameZH: "拿铁晨光",
		Tag:    "LATTE",
		Group:  "light",
		Palette: buildPalette(
			mustHex("#EFF1F5"), // Background
			mustHex("#FAFBFD"), // Surface
			mustHex("#E6E9EF"), // Sidebar
			mustHex("#DCE0E8"), // Border
			mustHex("#393C52"), // Text
			mustHex("#6B6E84"), // Secondary
			mustHex("#8839EF"), // Accent
			mustHex("#D96B38"), // Weekend
			mustHex("#D20F39"), // Warning
		),
	},
	{
		ID:     "warm-paper",
		Name:   "Warm Paper",
		NameZH: "和纸暖阳",
		Tag:    "PAPER",
		Group:  "light",
		Palette: buildPalette(
			mustHex("#F6F3EC"), // Background
			mustHex("#FDFBF7"), // Surface
			mustHex("#EEE9DF"), // Sidebar
			mustHex("#E4DDD0"), // Border
			mustHex("#28241F"), // Text
			mustHex("#787064"), // Secondary
			mustHex("#B85D30"), // Accent
			mustHex("#538266"), // Weekend
			mustHex("#C84848"), // Warning
		),
	},
	{
		ID:     "nordic-porcelain",
		Name:   "Nordic Porcelain",
		NameZH: "雾岚瓷青",
		Tag:    "FROST",
		Group:  "light",
		Palette: buildPalette(
			mustHex("#F1F5F8"), // Background
			mustHex("#FAFCFE"), // Surface
			mustHex("#E7EEF4"), // Sidebar
			mustHex("#D9E4EE"), // Border
			mustHex("#1D2A37"), // Text
			mustHex("#5E7286"), // Secondary
			mustHex("#3678A3"), // Accent
			mustHex("#BE843B"), // Weekend
			mustHex("#C74B59"), // Warning
		),
	},
}

// legacyPresetAliases maps retired preset IDs from earlier versions to their
// refined replacements so existing user configurations upgrade smoothly.
var legacyPresetAliases = map[string]string{
	"catppuccin":  "catppuccin-mocha",
	"tokyo-night": "midnight-slate",
	"nord-mono":   "midnight-slate",
	"matrix":      "everforest-dusk",
	"cyber-cyan":  "abyssal-teal",
	"amber-crt":   "velvet-espresso",
}

func buildPalette(bg, surface, sidebar, border, text, secondary, accent, weekend, warning color.NRGBA) Palette {
	p := Palette{
		Background: bg,
		Surface:    surface,
		Sidebar:    sidebar,
		Border:     border,
		Text:       text,
		Secondary:  secondary,
		Accent:     accent,
		Weekend:    weekend,
		Warning:    warning,
	}
	return derivePalette(p, true)
}

func derivePalette(p Palette, keepSidebar bool) Palette {
	if !keepSidebar {
		if p.IsDark() {
			p.Sidebar = Blend(p.Background, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, 0.03)
		} else {
			p.Sidebar = Blend(p.Background, color.NRGBA{R: 15, G: 23, B: 42, A: 255}, 0.035)
		}
	}
	if p.IsDark() {
		// Neutral subtle lift on hover (avoids muddy accent smear); soft accent wash on selection.
		p.Hover = Blend(p.Surface, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, 0.06)
		p.Selected = Blend(p.Surface, p.Accent, 0.15)
		p.WarningBg = Blend(p.Surface, p.Warning, 0.15)
	} else {
		p.Hover = Blend(p.Surface, color.NRGBA{R: 15, G: 23, B: 42, A: 255}, 0.045)
		p.Selected = Blend(p.Surface, p.Accent, 0.11)
		p.WarningBg = Blend(p.Surface, p.Warning, 0.10)
	}
	p.OnAccent = ContrastInk(p.Accent)
	return p
}

// Presets returns a copy of all built-in theme presets.
func Presets() []Preset {
	out := make([]Preset, len(builtInPresets))
	copy(out, builtInPresets)
	return out
}

// PresetByID returns the matching built-in preset (supporting legacy aliases)
// or the default Catppuccin Mocha preset.
func PresetByID(id string) Preset {
	if mapped, ok := legacyPresetAliases[id]; ok {
		id = mapped
	}
	for _, p := range builtInPresets {
		if p.ID == id {
			return p
		}
	}
	return builtInPresets[0]
}

// DefaultPalette returns the default Catppuccin Mocha palette.
func DefaultPalette() Palette {
	return PresetByID(DefaultPresetID).Palette
}

// DefaultConfig returns the default appearance configuration.
func DefaultConfig() Config {
	r := DefaultCornerRadius
	icon := DefaultIconSize
	return Config{
		Preset:        DefaultPresetID,
		GlassStyle:    GlassSmoked,
		WidgetOpacity: DefaultWidgetOpacity,
		CornerRadius:  &r,
		IconSize:      &icon,
		Components:    make(map[string]ComponentOverride),
	}
}

// EffectiveCornerRadius returns the clamped global corner radius in dp, defaulting to DefaultCornerRadius.
func (c Config) EffectiveCornerRadius() int {
	if c.CornerRadius == nil {
		return DefaultCornerRadius
	}
	return clampInt(*c.CornerRadius, MinCornerRadius, MaxCornerRadius)
}

// SetCornerRadius updates the global corner radius, clamped to [MinCornerRadius, MaxCornerRadius].
func (c *Config) SetCornerRadius(radius int) {
	v := clampInt(radius, MinCornerRadius, MaxCornerRadius)
	c.CornerRadius = &v
}

// EffectiveIconSize returns the clamped desktop widget icon size in dp, defaulting to DefaultIconSize.
func (c Config) EffectiveIconSize() int {
	if c.IconSize == nil {
		return DefaultIconSize
	}
	return clampInt(*c.IconSize, MinIconSize, MaxIconSize)
}

// SetIconSize updates the desktop widget icon size, clamped to [MinIconSize, MaxIconSize].
func (c *Config) SetIconSize(size int) {
	v := clampInt(size, MinIconSize, MaxIconSize)
	c.IconSize = &v
}

// Normalize clamps and sanitizes a Config without losing valid user choices.
func (c Config) Normalize() Config {
	out := c.Clone()
	out.Preset = PresetByID(out.Preset).ID
	out.GlassStyle = NormalizeGlassStyle(out.GlassStyle)
	out.WidgetOpacity = clampInt(out.WidgetOpacity, 0, 100)
	r := out.EffectiveCornerRadius()
	out.CornerRadius = &r
	icon := out.EffectiveIconSize()
	out.IconSize = &icon
	out.GlobalColors = sanitizePartial(out.GlobalColors)
	if out.Components == nil {
		out.Components = make(map[string]ComponentOverride)
	}
	for id, ov := range out.Components {
		if strings.TrimSpace(id) == "" || id == ComponentGlobal {
			delete(out.Components, id)
			continue
		}
		if ov.Preset != "" {
			ov.Preset = PresetByID(ov.Preset).ID
		}
		ov.Colors = sanitizePartial(ov.Colors)
		if ov.Opacity != nil {
			v := clampInt(*ov.Opacity, 0, 100)
			if id == ComponentLauncher {
				v = clampInt(v, MinLauncherOpacity, 100)
			}
			ov.Opacity = &v
		}
		out.Components[id] = ov
	}
	return out
}

// Clone returns a deep copy of Config safe for mutation.
func (c Config) Clone() Config {
	out := c
	if c.CornerRadius != nil {
		v := *c.CornerRadius
		out.CornerRadius = &v
	}
	if c.IconSize != nil {
		v := *c.IconSize
		out.IconSize = &v
	}
	if len(c.Components) > 0 {
		out.Components = make(map[string]ComponentOverride, len(c.Components))
		for k, v := range c.Components {
			if v.Opacity != nil {
				val := *v.Opacity
				v.Opacity = &val
			}
			out.Components[k] = v
		}
	} else {
		out.Components = make(map[string]ComponentOverride)
	}
	return out
}

// ParseConfig decodes a persisted JSON config, falling back cleanly to DefaultConfig.
func ParseConfig(raw string) Config {
	if strings.TrimSpace(raw) == "" {
		return DefaultConfig()
	}
	cfg := DefaultConfig()
	// Detect explicit widgetOpacity == 0 vs omitted field.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return cfg
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return DefaultConfig()
	}
	if _, hasOpacity := probe["widgetOpacity"]; !hasOpacity {
		cfg.WidgetOpacity = DefaultWidgetOpacity
	}
	if _, hasRadius := probe["cornerRadius"]; !hasRadius {
		r := DefaultCornerRadius
		cfg.CornerRadius = &r
	}
	if _, hasIcon := probe["iconSize"]; !hasIcon {
		icon := DefaultIconSize
		cfg.IconSize = &icon
	}
	return cfg.Normalize()
}

// MarshalConfig encodes a normalized Config to compact JSON.
func MarshalConfig(cfg Config) (string, error) {
	data, err := json.Marshal(cfg.Normalize())
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Resolve computes the effective Palette and Opacity for a given component ID.
func (c Config) Resolve(componentID string) Resolved {
	cfg := c.Normalize()
	basePreset := PresetByID(cfg.Preset)
	p := applyPartial(basePreset.Palette, cfg.GlobalColors)
	opacity := defaultOpacityFor(componentID, cfg.WidgetOpacity)
	custom := false

	if componentID != "" && componentID != ComponentGlobal {
		if ov, ok := cfg.Components[componentID]; ok && ov.Custom {
			custom = true
			if ov.Preset != "" {
				p = PresetByID(ov.Preset).Palette
			}
			p = applyPartial(p, ov.Colors)
			if ov.Opacity != nil {
				opacity = *ov.Opacity
				if componentID == ComponentLauncher {
					opacity = clampInt(opacity, MinLauncherOpacity, 100)
				}
			}
		}
	}
	if componentID == ComponentSettings {
		opacity = 100
	}
	return Resolved{
		Palette:        p,
		Opacity:        clampInt(opacity, 0, 100),
		GlassStyle:     cfg.GlassStyle,
		CornerRadius:   cfg.EffectiveCornerRadius(),
		IconSize:       cfg.EffectiveIconSize(),
		TitleAccentBar: cfg.TitleAccentBar,
		IsDark:         p.IsDark(),
		Custom:         custom,
	}
}

func defaultOpacityFor(componentID string, widgetOpacity int) int {
	switch componentID {
	case ComponentSettings:
		return 100
	case ComponentLauncher:
		return DefaultLauncherOpacity
	default:
		return clampInt(widgetOpacity, 0, 100)
	}
}

func applyPartial(base Palette, ov PartialPalette) Palette {
	if ov.IsEmpty() {
		return base
	}
	p := base
	bgChanged := false
	if c, ok := ParseHex(ov.Background); ok {
		p.Background = c
		bgChanged = true
	}
	if c, ok := ParseHex(ov.Surface); ok {
		p.Surface = c
	}
	if c, ok := ParseHex(ov.Border); ok {
		p.Border = c
	}
	if c, ok := ParseHex(ov.Text); ok {
		p.Text = c
	}
	if c, ok := ParseHex(ov.Secondary); ok {
		p.Secondary = c
	}
	if c, ok := ParseHex(ov.Accent); ok {
		p.Accent = c
	}
	if c, ok := ParseHex(ov.Weekend); ok {
		p.Weekend = c
	}
	return derivePalette(p, !bgChanged)
}

func sanitizePartial(ov PartialPalette) PartialPalette {
	var out PartialPalette
	for _, slot := range EditableSlots {
		if c, ok := ParseHex(ov.Get(slot)); ok {
			out.Set(slot, FormatHex(c))
		}
	}
	return out
}

// ParseHex parses #RRGGBB, RRGGBB, #RGB, or RGB into an opaque color.NRGBA.
func ParseHex(input string) (color.NRGBA, bool) {
	s := strings.TrimSpace(input)
	s = strings.TrimPrefix(s, "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return color.NRGBA{}, false
	}
	var r, g, b uint8
	for i := 0; i < 6; i++ {
		v, ok := hexNibble(s[i])
		if !ok {
			return color.NRGBA{}, false
		}
		switch i {
		case 0:
			r = v << 4
		case 1:
			r |= v
		case 2:
			g = v << 4
		case 3:
			g |= v
		case 4:
			b = v << 4
		case 5:
			b |= v
		}
	}
	return color.NRGBA{R: r, G: g, B: b, A: 255}, true
}

func hexNibble(ch byte) (uint8, bool) {
	switch {
	case ch >= '0' && ch <= '9':
		return ch - '0', true
	case ch >= 'a' && ch <= 'f':
		return ch - 'a' + 10, true
	case ch >= 'A' && ch <= 'F':
		return ch - 'A' + 10, true
	default:
		return 0, false
	}
}

func mustHex(s string) color.NRGBA {
	c, ok := ParseHex(s)
	if !ok {
		panic("invalid preset hex: " + s)
	}
	return c
}

// FormatHex formats a color as uppercase #RRGGBB.
func FormatHex(c color.NRGBA) string {
	return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B)
}

// WithAlpha returns c with alpha replaced by a.
func WithAlpha(c color.NRGBA, a uint8) color.NRGBA {
	c.A = a
	return c
}

// Blend linearly mixes overlay onto base by ratio t in [0, 1].
func Blend(base, overlay color.NRGBA, t float64) color.NRGBA {
	if t <= 0 {
		return base
	}
	if t >= 1 {
		return overlay
	}
	mix := func(a, b uint8) uint8 {
		return uint8(float64(a)*(1-t) + float64(b)*t + 0.5)
	}
	return color.NRGBA{
		R: mix(base.R, overlay.R),
		G: mix(base.G, overlay.G),
		B: mix(base.B, overlay.B),
		A: 255,
	}
}

// Luminance returns an approximate perceptual luminance in [0, 1].
func Luminance(c color.NRGBA) float64 {
	return (0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)) / 255.0
}

// ContrastInk returns a readable dark or light text color over bg.
func ContrastInk(bg color.NRGBA) color.NRGBA {
	if Luminance(bg) > 0.54 {
		return color.NRGBA{R: 14, G: 17, B: 24, A: 255}
	}
	return color.NRGBA{R: 255, G: 255, B: 255, A: 255}
}

// QuickSwatches returns curated swatches for a given slot across dark and light palettes.
func QuickSwatches(slot ColorSlot) []string {
	switch slot {
	case SlotBackground, SlotSurface:
		return []string{"#11111B", "#1E1E2E", "#0D0E12", "#181A22", "#14121D", "#231F1C", "#F4F6FA", "#FAFBFD"}
	case SlotBorder:
		return []string{"#2C2D40", "#252935", "#312D45", "#323E38", "#342E29", "#E2E7F0", "#DCE0E8", "#D9E4EE"}
	case SlotText:
		return []string{"#D8E0F8", "#ECEEF4", "#EAE6F6", "#E0DBBE", "#F0E7DB", "#1A1F2E", "#393C52", "#28241F"}
	case SlotSecondary:
		return []string{"#9399B2", "#8A90A0", "#9893B0", "#96A297", "#A4988A", "#646F84", "#6B6E84", "#5E7286"}
	case SlotAccent:
		return []string{"#CBA6F7", "#8E9BF7", "#7BB5E8", "#EBBCBA", "#A7C080", "#E0A96D", "#66C7BC", "#4F5FD9"}
	case SlotWeekend:
		return []string{"#FAB387", "#B9A7F5", "#9ED6C8", "#C4A7E7", "#DBBC7F", "#E8A5C8", "#D96B38", "#BE843B"}
	default:
		return []string{"#CBA6F7", "#8E9BF7", "#7BB5E8", "#EBBCBA", "#A7C080", "#66C7BC"}
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
