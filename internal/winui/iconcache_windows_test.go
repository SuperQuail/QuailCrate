package winui

import (
	"testing"

	v1 "quailcrate/pluginapi/v1"
)

func TestWidgetUsesChangedIcon(t *testing.T) {
	view := v1.View{Controls: []v1.Control{
		{Items: []v1.Item{{Icon: "calendar"}}},
		{Items: []v1.Item{{Icon: "shared-app"}}},
	}}
	if widgetUsesChangedIcon(view, map[string]bool{"launcher-only": true}) {
		t.Fatal("an unrelated launcher icon requested a widget frame")
	}
	if !widgetUsesChangedIcon(view, map[string]bool{"launcher-only": true, "shared-app": true}) {
		t.Fatal("shared icon in a batch did not request a widget frame")
	}
	view.Controls = view.Controls[:1]
	if widgetUsesChangedIcon(view, map[string]bool{"shared-app": true}) {
		t.Fatal("an icon removed before completion requested a widget frame")
	}
}
