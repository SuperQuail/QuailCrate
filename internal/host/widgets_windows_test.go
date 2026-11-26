package host

import (
	"context"
	"testing"

	"quailcrate/internal/store"
	"quailcrate/internal/winui"
)

func TestWidgetLayoutSaveOrder(t *testing.T) {
	const plugin = "example.widgets"
	ctx := context.Background()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Apply(ctx, plugin, store.Mutations{Create: []store.Widget{
		{ID: "clock", Type: "clock"}, {ID: "hidden", Type: "clock"},
	}}); err != nil {
		t.Fatal(err)
	}
	rec := &record{
		generation: 2, lifetime: ctx,
		widgets: []winui.Widget{{ID: "clock", Generation: 2}},
	}
	h := Host{ctx: ctx, db: db, records: map[string]*record{plugin: rec}}
	latest := winui.Layout{Revision: 2, AlwaysOnTop: true}
	h.move(plugin, "clock", 2, latest)
	h.move(plugin, "clock", 2, winui.Layout{Revision: 1})
	h.move(plugin, "clock", 2, winui.Layout{Revision: 2})
	h.move(plugin, "clock", 1, winui.Layout{Revision: 3})
	h.move(plugin, "hidden", 2, latest)
	if got := rec.widgets[0].Layout; got != latest {
		t.Fatalf("host layout = %+v, want %+v", got, latest)
	}
	snap, err := db.Snapshot(ctx, plugin)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Widgets) != 2 || !snap.Widgets[0].AlwaysOnTop || snap.Widgets[1].AlwaysOnTop {
		t.Fatalf("stored widgets = %+v", snap.Widgets)
	}
}
