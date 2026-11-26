package theme

import (
	"image/color"
	"testing"
)

func TestParseAndFormatHex(t *testing.T) {
	c, ok := ParseHex("#7aa2f7")
	if !ok || c != (color.NRGBA{R: 0x7A, G: 0xA2, B: 0xF7, A: 255}) {
		t.Fatalf("ParseHex(#7aa2f7) = %+v, %v", c, ok)
	}
	if got := FormatHex(c); got != "#7AA2F7" {
		t.Fatalf("FormatHex = %q", got)
	}
	short, ok := ParseHex("0f8")
	if !ok || FormatHex(short) != "#00FF88" {
		t.Fatalf("ParseHex(0f8) = %s, %v", FormatHex(short), ok)
	}
	if _, ok := ParseHex("#ZZZZZZ"); ok {
		t.Fatal("expected invalid hex to fail")
	}
}

func TestConfigRoundTripAndComponentResolution(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Preset != DefaultPresetID || cfg.WidgetOpacity != DefaultWidgetOpacity {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	calOpacity := 0
	cfg.Components[ComponentCalendar] = ComponentOverride{
		Custom:  true,
		Preset:  "abyssal-teal",
		Colors:  PartialPalette{Accent: "#ff00aa", Weekend: "#00ff88"},
		Opacity: &calOpacity,
	}
	launcherOpacity := 10 // should clamp to MinLauncherOpacity
	cfg.Components[ComponentLauncher] = ComponentOverride{
		Custom:  true,
		Opacity: &launcherOpacity,
	}

	raw, err := MarshalConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	restored := ParseConfig(raw)

	cal := restored.Resolve(ComponentCalendar)
	if !cal.Custom || cal.Opacity != 0 || FormatHex(cal.Palette.Accent) != "#FF00AA" || FormatHex(cal.Palette.Weekend) != "#00FF88" {
		t.Fatalf("resolved calendar = %+v", cal)
	}
	groups := restored.Resolve(ComponentGroups)
	if groups.Custom || groups.Opacity != DefaultWidgetOpacity || FormatHex(groups.Palette.Accent) != "#CBA6F7" {
		t.Fatalf("resolved groups = %+v", groups)
	}
	launcher := restored.Resolve(ComponentLauncher)
	if launcher.Opacity != MinLauncherOpacity {
		t.Fatalf("launcher opacity should clamp to %d, got %d", MinLauncherOpacity, launcher.Opacity)
	}
	settings := restored.Resolve(ComponentSettings)
	if settings.Opacity != 100 {
		t.Fatalf("settings opacity should remain 100, got %d", settings.Opacity)
	}
}

func TestBuiltInPresetsAndLegacyAliases(t *testing.T) {
	presets := Presets()
	if len(presets) != 15 {
		t.Fatalf("Presets() len = %d, want 15", len(presets))
	}
	seen := make(map[string]bool, len(presets))
	darkCount, lightCount := 0, 0
	for _, p := range presets {
		if p.ID == "" || p.Name == "" || p.NameZH == "" || p.Tag == "" || p.Group == "" {
			t.Fatalf("incomplete preset metadata: %+v", p)
		}
		if seen[p.ID] {
			t.Fatalf("duplicate preset ID %q", p.ID)
		}
		seen[p.ID] = true
		if p.Palette.IsDark() {
			darkCount++
		} else {
			lightCount++
		}
	}
	if darkCount != 11 || lightCount != 4 {
		t.Fatalf("dark=%d light=%d, want 11 dark and 4 light", darkCount, lightCount)
	}
	aliases := map[string]string{
		"catppuccin":  "catppuccin-mocha",
		"tokyo-night": "midnight-slate",
		"nord-mono":   "midnight-slate",
		"matrix":      "everforest-dusk",
		"cyber-cyan":  "abyssal-teal",
		"amber-crt":   "velvet-espresso",
	}
	for oldID, wantID := range aliases {
		if got := PresetByID(oldID).ID; got != wantID {
			t.Fatalf("PresetByID(%q) = %q, want %q", oldID, got, wantID)
		}
	}
}

func TestParseConfigHandlesZeroWidgetOpacityAndMalformedInput(t *testing.T) {
	zero := ParseConfig(`{"preset":"matrix","widgetOpacity":0,"cornerRadius":0}`)
	if zero.Preset != "everforest-dusk" || zero.WidgetOpacity != 0 || zero.EffectiveCornerRadius() != 0 {
		t.Fatalf("zero widget opacity or corner radius lost: %+v", zero)
	}
	omitted := ParseConfig(`{"preset":"rose-pine","widgetOpacity":0}`)
	if omitted.Preset != "rose-pine" {
		t.Fatalf("expected rose-pine preset, got %q", omitted.Preset)
	}
	if omitted.EffectiveCornerRadius() != DefaultCornerRadius {
		t.Fatalf("omitted cornerRadius should default to %d, got %d", DefaultCornerRadius, omitted.EffectiveCornerRadius())
	}
	if omitted.EffectiveIconSize() != DefaultIconSize {
		t.Fatalf("omitted iconSize should default to %d, got %d", DefaultIconSize, omitted.EffectiveIconSize())
	}
	clamped := ParseConfig(`{"cornerRadius":99,"iconSize":200}`)
	if clamped.EffectiveCornerRadius() != MaxCornerRadius {
		t.Fatalf("cornerRadius should clamp to %d, got %d", MaxCornerRadius, clamped.EffectiveCornerRadius())
	}
	if clamped.EffectiveIconSize() != MaxIconSize {
		t.Fatalf("iconSize should clamp to %d, got %d", MaxIconSize, clamped.EffectiveIconSize())
	}
	tooSmall := ParseConfig(`{"iconSize":8}`)
	if tooSmall.EffectiveIconSize() != MinIconSize {
		t.Fatalf("small iconSize should clamp to %d, got %d", MinIconSize, tooSmall.EffectiveIconSize())
	}
	tooSmall.SetIconSize(52)
	raw, err := MarshalConfig(tooSmall)
	if err != nil {
		t.Fatal(err)
	}
	if got := ParseConfig(raw).Resolve(ComponentGroups).IconSize; got != 52 {
		t.Fatalf("round-tripped iconSize = %d, want 52", got)
	}
	fallback := ParseConfig("{not-json")
	if fallback.Preset != DefaultPresetID || fallback.WidgetOpacity != DefaultWidgetOpacity || fallback.EffectiveCornerRadius() != DefaultCornerRadius || fallback.EffectiveIconSize() != DefaultIconSize || fallback.TitleAccentBar {
		t.Fatalf("malformed fallback = %+v", fallback)
	}
	withBar := ParseConfig(`{"titleAccentBar":true}`)
	if !withBar.Resolve(ComponentGlobal).TitleAccentBar {
		t.Fatalf("expected titleAccentBar=true, got %+v", withBar)
	}
}

func TestScaleRadius(t *testing.T) {
	if got := ScaleRadius(0, 12); got != 0 {
		t.Fatalf("ScaleRadius(0, 12) = %d, want 0", got)
	}
	if got := ScaleRadius(DefaultCornerRadius, 8); got != 8 {
		t.Fatalf("ScaleRadius(default, 8) = %d, want 8", got)
	}
	if got := ScaleRadius(6, 12); got != 6 {
		t.Fatalf("ScaleRadius(6, 12) = %d, want 6", got)
	}
	if got := ScaleRadius(18, 12); got != 18 {
		t.Fatalf("ScaleRadius(18, 12) = %d, want 18", got)
	}
	if got := ScaleRadius(1, 2); got != 1 {
		t.Fatalf("ScaleRadius(1, 2) = %d, want >= 1", got)
	}
}
