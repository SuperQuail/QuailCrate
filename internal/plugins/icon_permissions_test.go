package plugins

import (
	"errors"
	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
	"testing"
)

func TestIconPermissions(t *testing.T) {
	app := v1.App{ID: "catalog:notes", Icon: `shell:AppsFolder\example!Notes`}
	for _, tc := range []struct {
		key     string
		grants  []string
		allowed bool
	}{
		{"calendar", nil, true},
		{app.ID, nil, false}, {app.Icon, nil, false}, {app.ID, []string{"apps.read"}, true},
		{`shell:AppsFolder\unknown`, []string{"apps.read"}, false},
		{`C:\icons\folder.ico`, nil, false}, {`C:\icons\folder.ico`, []string{"paths.open"}, true},
		{`imgfile:C:\icons\image.png`, []string{"urls.open"}, false}, {`imgfile:C:\icons\image.png`, []string{"paths.open"}, true},
		{`\\server\share\folder.ico`, []string{"paths.open"}, false},
		{"favicon:https://example.invalid/page", nil, false}, {"favicon:https://example.invalid/page", []string{"urls.open"}, true},
		{"favicon:file:///missing", []string{"urls.open"}, false},
	} {
		data := v1.Data{Results: []v1.Result{{Icon: tc.key}}}
		err := authorizeIcons(tc.grants, []v1.App{app}, &data)
		if (err == nil) != tc.allowed {
			t.Errorf("%s grants %v: %v", tc.key, tc.grants, err)
		}
		if tc.key == app.ID && err == nil && data.Results[0].Icon != app.Icon {
			t.Error("catalog ID was not normalized to its issued icon")
		}
	}
	key := `imgfile:C:\fixtures\icon.png`
	cases := map[string]v1.Data{
		"search":  {Results: []v1.Result{{Icon: key}}},
		"legacy":  {View: &v1.View{Controls: []v1.Control{{Items: []v1.Item{{Icon: key}}}}}},
		"tree":    {View: &v1.View{Root: &v2.Node{Type: "column", Children: []v2.Node{{Type: "virtualList", Props: v2.Props{Items: []v2.Item{{Icon: key}}}}}}}},
		"picker":  {Actions: []v1.Action{{Type: "ui.pickOptions", Options: []v1.Option{{Icon: key}}}}},
		"refresh": {Actions: []v1.Action{{Type: "icons.refresh", IconKey: key}}},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			var denied *v1.APIError
			if err := authorizeIcons(nil, nil, &data); !errors.As(err, &denied) || denied.Code != "permission_denied" {
				t.Fatalf("unauthorized key accepted: %v", err)
			}
		})
	}
}
