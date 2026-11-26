package gioui

import (
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"quailcrate/internal/pluginapps"
	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

func TestAppWindowPackagedPageLoading(t *testing.T) {
	document := &pluginapps.Page{Document: &pluginapps.Document{URL: "https://example.quailcrate.invalid/index.html"}}
	for _, tt := range []struct {
		name   string
		model  AppWindowModel
		labels []string
	}{
		{
			name:   "loading web view",
			model:  AppWindowModel{Locale: "en-US", State: AppStateReady, Page: document},
			labels: []string{"Loading plugin view…"},
		},
		{
			name:   "loading web view in Chinese",
			model:  AppWindowModel{Locale: "zh-CN", State: AppStateReady, Page: document},
			labels: []string{"正在加载插件视图…"},
		},
		{
			name:  "active web view",
			model: AppWindowModel{Locale: "en-US", State: AppStateReady, Page: document, WebViewActive: true},
		},
		{
			name:   "empty declarative page",
			model:  AppWindowModel{Locale: "en-US", State: AppStateReady, Page: &pluginapps.Page{}},
			labels: []string{"This page has no controls"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var router input.Router
			gtx := layout.Context{
				Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: layout.Exact(image.Pt(640, 480)),
			}
			NewAppWindowView().Layout(gtx, tt.model, nil)
			router.Frame(gtx.Ops)
			var labels []string
			for _, node := range router.AppendSemantics(nil) {
				if node.Desc.Label != "" {
					labels = append(labels, node.Desc.Label)
				}
			}
			if !slices.Equal(labels, tt.labels) {
				t.Fatalf("app body labels = %q, want %q", labels, tt.labels)
			}
		})
	}
}

func TestAppWindowStatesTabsAndControlRouting(t *testing.T) {
	view := NewAppWindowView()
	var router input.Router
	gtx := layout.Context{
		Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(640, 480)),
	}
	tabs := []pluginapps.Tab{
		{TabRef: pluginapps.TabRef{PluginID: "demo.counter", TabID: "overview", Generation: 1}, PluginName: "Demo", Title: "Overview"},
		{TabRef: pluginapps.TabRef{PluginID: "demo.counter", TabID: "counter", Generation: 1}, PluginName: "Demo", Title: "Counter"},
	}
	page := &pluginapps.Page{
		PageRef: pluginapps.PageRef{Session: pluginapps.Session{TabRef: tabs[1].TabRef, ID: 7}, Revision: 3},
		View: v1.View{
			Title: "Counter",
			Controls: []v1.Control{
				{ID: "count", Type: "text", Text: "Current count 1"},
				{ID: "plus", Type: "button", Text: "Increase"},
				{ID: "items", Type: "list", Items: []v1.Item{{ID: "row-1", Text: "Alpha", Hint: "hint"}}},
				{ID: "grid", Type: "grid", Columns: 2, Items: []v1.Item{
					{ID: "hdr", Text: "MON", State: "header"},
					{ID: "cell-1", Text: "25", State: "current"},
				}},
			},
		},
	}

	var actions []AppWindowAction
	frame := func(m AppWindowModel) {
		gtx.Reset()
		gtx.Now = time.Now()
		view.Layout(gtx, m, func(a AppWindowAction) { actions = append(actions, a) })
		router.Frame(gtx.Ops)
	}

	// Loading and Error + Retry state
	errModel := AppWindowModel{
		Locale: "en-US", PluginID: "demo.counter", PluginName: "Demo",
		Tabs: tabs, ActiveTabID: "counter", State: AppStateError, Error: "boom",
		CustomTitleBar: true,
	}
	frame(errModel)
	view.retryBtn.Click()
	frame(errModel)
	if len(actions) != 1 || actions[0].Kind != AppRetry {
		t.Fatalf("expected AppRetry, got %+v", actions)
	}

	// Ready state: switch tab + click button + click list item + click grid cell
	actions = nil
	readyModel := errModel
	readyModel.State = AppStateReady
	readyModel.Page = page
	frame(readyModel)
	view.tabButtons["overview"].Click()
	view.controlButtons["plus"].Click()
	view.itemButtons[[2]string{"items", "row-1"}].Click()
	view.itemButtons[[2]string{"grid", "cell-1"}].Click()
	frame(readyModel)
	if len(actions) != 4 {
		t.Fatalf("expected 4 actions, got %+v", actions)
	}
	if actions[0].Kind != AppSelectTab || actions[0].Tab != tabs[0].TabRef {
		t.Fatalf("tab switch action = %+v", actions[0])
	}
	if actions[1].Kind != AppActControl || actions[1].Page != page.PageRef || actions[1].ControlID != "plus" || actions[1].ItemID != "" {
		t.Fatalf("button action = %+v", actions[1])
	}
	if actions[2].ControlID != "items" || actions[2].ItemID != "row-1" {
		t.Fatalf("list action = %+v", actions[2])
	}
	if actions[3].ControlID != "grid" || actions[3].ItemID != "cell-1" {
		t.Fatalf("grid action = %+v", actions[3])
	}
}

func TestAppWindowActivatesNestedV2Controls(t *testing.T) {
	page := &pluginapps.Page{View: v1.View{UIVersion: 2, Root: &v2.Node{Type: "column", ID: "page", Children: []v2.Node{
		{Type: "row", ID: "toolbar", Children: []v2.Node{{Type: "button", ID: "nav-button", Props: v2.Props{Text: "Next"}, Events: map[string]string{"activate": "month.next"}}}},
		{Type: "grid", ID: "calendar-grid", Props: v2.Props{Columns: 2, Items: []v2.Item{{Key: "header", Role: "columnheader", Text: "MON"}, {Key: "date", Text: "1"}}}, Events: map[string]string{"activate": "month.pick"}},
	}}}}
	view := NewAppWindowView()
	var router input.Router
	gtx := layout.Context{Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(640, 480))}
	var actions []AppWindowAction
	frame := func() {
		gtx.Reset()
		gtx.Now = time.Now()
		view.Layout(gtx, AppWindowModel{Locale: "en-US", State: AppStateReady, Page: page}, func(a AppWindowAction) { actions = append(actions, a) })
		router.Frame(gtx.Ops)
	}
	frame()
	if view.itemButtons[[2]string{"calendar-grid", "header"}] != nil {
		t.Fatal("column header is interactive")
	}
	view.controlButtons["nav-button"].Click()
	view.itemButtons[[2]string{"calendar-grid", "date"}].Click()
	frame()
	if len(actions) != 2 || actions[0].ControlID != "month.next" || actions[1].ControlID != "month.pick" || actions[1].ItemID != "date" {
		t.Fatalf("app actions %+v", actions)
	}
}
