package gioui

import (
	"image"
	"image/color"
	"reflect"
	"testing"

	"quailcrate/internal/theme"
	v1 "quailcrate/pluginapi/v1"
	v2 "quailcrate/pluginapi/v2"
)

type widgetPaintRecord struct {
	fill   []image.Rectangle
	radii  []int
	stroke []image.Rectangle
	images []image.Rectangle
	labels map[string]image.Rectangle
	styles map[string]WidgetText
	alphas []uint8
}

func (r *widgetPaintRecord) Fill(area image.Rectangle, col color.NRGBA, radius int) {
	r.fill = append(r.fill, area)
	r.radii = append(r.radii, radius)
	r.alphas = append(r.alphas, col.A)
}
func (r *widgetPaintRecord) Stroke(area image.Rectangle, _ color.NRGBA, _, _ int) {
	r.stroke = append(r.stroke, area)
}
func (r *widgetPaintRecord) Image(area image.Rectangle, _ image.Image) {
	r.images = append(r.images, area)
}
func (r *widgetPaintRecord) Icon(_ image.Rectangle, _ string, _ color.NRGBA) {}
func (r *widgetPaintRecord) Label(area image.Rectangle, text string, style WidgetText) {
	r.labels[text] = area
	if r.styles != nil {
		r.styles[text] = style
	}
}

func TestWidgetDrawingAndInputShareGeometry(t *testing.T) {
	p := theme.DefaultPalette()
	colors := WidgetColors{
		Background: p.Background, Surface: p.Surface, Ink: p.Text, Secondary: p.Secondary,
		Border: p.Border, Accent: p.Accent, Selection: p.Selected, Hover: p.Hover,
		Weekend: p.Weekend, Warning: p.Warning, OnAccent: p.OnAccent, OnSelection: p.Text,
	}
	view := v1.View{Title: "Archive", Controls: []v1.Control{
		{ID: "add", Type: "button", Text: "Add"}, {ID: "order", Type: "button", Text: "Organize"},
		{ID: "files", Type: "list", Drop: true, Reorder: true, Items: []v1.Item{
			{ID: "a", Text: "Alpha"}, {ID: "b", Text: "Beta"}, {ID: "c", Text: "Gamma"}, {ID: "d", Text: "Delta"},
		}},
	}}
	for _, dpi := range []int{96, 120, 192} {
		opt := WidgetOptions{DPI: dpi, Size: image.Pt(312*dpi/96, 196*dpi/96), Colors: colors, Scroll: map[string]int{"files": 99}}
		paint := &widgetPaintRecord{labels: make(map[string]image.Rectangle), styles: make(map[string]WidgetText)}
		geometry := LayoutWidget(view, opt, paint)
		if !reflect.DeepEqual(geometry, LayoutWidget(view, opt, nil)) {
			t.Fatal("paint and hit geometry differ")
		}
		if len(geometry.Hits) != 4 || len(geometry.DropZones) != 1 {
			t.Fatalf("%d DPI: unexpected geometry %+v", dpi, geometry)
		}
		if geometry.Hits[0].ControlID != "add" || !paint.labels["Add"].In(geometry.Hits[0].Area) {
			t.Fatalf("%d DPI: button label outside button", dpi)
		}
		zone := geometry.DropZones[0]
		last := geometry.Hits[3]
		if last.ItemID != "d" || !last.Area.In(zone.Area) || !last.Drop || !last.Reorder || last.Index != 3 || last.Total != 4 || opt.Scroll["files"] != 2 {
			t.Fatalf("%d DPI: scrolled row or drop geometry incorrect: %+v", dpi, last)
		}
		if !paint.labels["Delta"].In(last.Area) || paint.labels["Delta"].Empty() {
			t.Fatalf("%d DPI: last visible row missing", dpi)
		}
	}
}

func TestWidgetEmptyDropZoneAndGridHeader(t *testing.T) {
	p := theme.DefaultPalette()
	colors := WidgetColors{
		Background: p.Background, Surface: p.Surface, Ink: p.Text, Secondary: p.Secondary,
		Border: p.Border, Accent: p.Accent, Selection: p.Selected, Hover: p.Hover,
		Weekend: p.Weekend, Warning: p.Warning, OnAccent: p.OnAccent, OnSelection: p.Text,
		Opacity: 0, Translucent: true,
	}
	view := v1.View{Title: "Calendar", Controls: []v1.Control{
		{ID: "days", Type: "grid", Columns: 2, Items: []v1.Item{
			{ID: "w1", Text: "MON", State: "header"},
			{ID: "w7", Text: "SUN", State: "header-weekend"},
			{ID: "today", Text: "24", State: "current"},
			{ID: "sat", Text: "25", State: "weekend"},
		}},
		{ID: "drop", Type: "list", Drop: true, Text: "Drop files"},
	}}
	paint := &widgetPaintRecord{labels: make(map[string]image.Rectangle), styles: make(map[string]WidgetText)}
	geometry := LayoutWidget(view, WidgetOptions{DPI: 96, Size: image.Pt(280, 260), Colors: colors}, paint)
	if len(geometry.Hits) != 2 || geometry.Hits[0].ItemID != "today" || geometry.Hits[1].ItemID != "sat" || len(geometry.DropZones) != 1 || geometry.DropZones[0].Area.Empty() {
		t.Fatalf("grid and empty drop target: %+v", geometry)
	}
	// At 0% translucent opacity, outer shell keeps alpha=1 for Win32 click hit-testing and enables text shadow.
	if len(paint.alphas) == 0 || paint.alphas[0] != 1 {
		t.Fatalf("expected translucent 0%% shell alpha=1, got %v", paint.alphas)
	}
	if !paint.styles["25"].Mono || !paint.styles["25"].Shadow || paint.styles["25"].Color != p.Weekend {
		t.Fatalf("expected monospace shadowed weekend text, got %+v", paint.styles["25"])
	}
}

func TestWidgetIconGridRendersIconsAndMatchesHitGeometry(t *testing.T) {
	p := theme.DefaultPalette()
	colors := WidgetColors{
		Background: p.Background, Surface: p.Surface, Ink: p.Text, Secondary: p.Secondary,
		Border: p.Border, Accent: p.Accent, Selection: p.Selected, Hover: p.Hover,
		Weekend: p.Weekend, Warning: p.Warning, OnAccent: p.OnAccent, OnSelection: p.Text,
		Opacity: 65, Translucent: true,
	}
	sampleIcon := image.NewRGBA(image.Rect(0, 0, 32, 32))
	view := v1.View{Title: "Apps", Controls: []v1.Control{
		{ID: "items", Type: "list", Columns: 4, Drop: true, Reorder: true, Items: []v1.Item{
			{ID: "a", Text: "Edge", Icon: `C:\Apps\Edge.lnk`},
			{ID: "b", Text: "Notes.txt", Icon: `C:\Docs\Notes.txt`},
		}},
	}}
	opt := WidgetOptions{
		DPI: 96, Size: image.Pt(396, 260), Colors: colors,
		Icons: map[string]image.Image{`C:\Apps\Edge.lnk`: sampleIcon},
	}
	paint := &widgetPaintRecord{labels: make(map[string]image.Rectangle), styles: make(map[string]WidgetText)}
	g := LayoutWidget(view, opt, paint)
	if !reflect.DeepEqual(g, LayoutWidget(view, opt, nil)) {
		t.Fatal("icon grid paint and hit geometry differ")
	}
	if len(g.Hits) != 2 || len(g.DropZones) != 1 || len(paint.images) != 1 {
		t.Fatalf("icon grid hits=%d dropZones=%d images=%d", len(g.Hits), len(g.DropZones), len(paint.images))
	}
	if !paint.images[0].In(g.Hits[0].Area) || !paint.labels["Edge"].In(g.Hits[0].Area) {
		t.Fatalf("icon or label outside tile: hit=%v icon=%v label=%v", g.Hits[0].Area, paint.images[0], paint.labels["Edge"])
	}
}

func TestMenuMeasureAndLayoutHits(t *testing.T) {
	items := []MenuItem{
		{ID: "add", Label: "添加文件…", Icon: "plus"},
		{Separator: true},
		{ID: "del", Label: "删除分组", Icon: "trash", Danger: true},
	}
	sz := MeasureMenu(items, 96)
	if sz.X < 200 || sz.Y < 80 {
		t.Fatalf("unexpected menu size %v", sz)
	}
	hits := LayoutMenu(nil, nil, items, MenuOptions{DPI: 96, Palette: theme.DefaultPalette()})
	if len(hits) != 3 || hits[0].Empty() || !hits[1].Empty() || hits[2].Empty() {
		t.Fatalf("menu hits = %+v", hits)
	}
}

func TestWidgetInlineTabsBadgesAndMonogramFallback(t *testing.T) {
	p := theme.DefaultPalette()
	colors := WidgetColors{
		Background: p.Background, Surface: p.Surface, Ink: p.Text, Secondary: p.Secondary,
		Border: p.Border, Accent: p.Accent, Selection: p.Selected, Hover: p.Hover,
		Weekend: p.Weekend, Warning: p.Warning, OnAccent: p.OnAccent, OnSelection: p.Text,
		Opacity: 75, Translucent: true,
	}
	view := v1.View{
		Title: "Hub", Summary: "2 项",
		Tabs: []v1.WidgetTab{
			{ID: "tab-1", Title: "游戏"},
			{ID: "tab-2", Title: "网页"},
			{ID: "tab-3", Title: "项目"},
		},
		ActiveTab: "tab-2",
		Controls: []v1.Control{
			{ID: "items", Type: "list", Columns: 1, Drop: true, Reorder: true, Items: []v1.Item{
				{ID: "w1", Text: "内部后台", Hint: "192.168.1.10:8080", Badge: "WEB", Icon: "favicon:http://192.168.1.10:8080"},
				{ID: "w2", Text: "QuailCrate", Hint: `F:\Projects\QuailCrate`, Badge: "VS Code", Icon: `F:\Projects\QuailCrate`},
			}},
		},
	}

	// Expanded view renders inline tabs, monogram fallback ("内"), and right-aligned badges.
	paint := &widgetPaintRecord{labels: make(map[string]image.Rectangle), styles: make(map[string]WidgetText)}
	opt := WidgetOptions{DPI: 96, Size: image.Pt(420, 260), Colors: colors}
	g := LayoutWidget(view, opt, paint)
	if !reflect.DeepEqual(g, LayoutWidget(view, opt, nil)) {
		t.Fatal("tabbed widget paint and hit geometry differ")
	}
	if _, ok := paint.labels["游戏"]; !ok {
		t.Fatalf("missing inline tab label: %+v", paint.labels)
	}
	if _, ok := paint.labels["内"]; !ok {
		t.Fatalf("missing colored monogram fallback for intranet URL: %+v", paint.labels)
	}
	if _, ok := paint.labels["VS Code"]; !ok {
		t.Fatalf("missing list badge label: %+v", paint.labels)
	}

	// Hovering a collapsed view keeps only the inline tabs interactive.
	collapsedPaint := &widgetPaintRecord{labels: make(map[string]image.Rectangle), styles: make(map[string]WidgetText)}
	cg := LayoutWidget(view, WidgetOptions{DPI: 96, Size: image.Pt(420, 44), Colors: colors, Collapsed: true, MouseInside: true}, collapsedPaint)
	if len(cg.Hits) != len(view.Tabs) {
		t.Fatalf("expected one hit per collapsed tab, got %+v", cg.Hits)
	}
	for _, h := range cg.Hits {
		if h.ControlID != "__tab__" {
			t.Fatalf("unexpected control in collapsed header: %+v", h)
		}
	}
}

func TestWidgetRespectsGlobalCornerRadius(t *testing.T) {
	p := theme.DefaultPalette()
	base := WidgetColors{
		Background: p.Background, Surface: p.Surface, Ink: p.Text, Secondary: p.Secondary,
		Border: p.Border, Accent: p.Accent, Selection: p.Selected, Hover: p.Hover,
		Weekend: p.Weekend, Warning: p.Warning, OnAccent: p.OnAccent, OnSelection: p.Text,
		Opacity: 80, Translucent: true, HasCornerRadius: true,
	}
	view := v1.View{Title: "Groups", Controls: []v1.Control{{ID: "ok", Type: "button", Text: "OK"}}}

	sharp := base
	sharp.CornerRadius = 0
	sharpPaint := &widgetPaintRecord{labels: make(map[string]image.Rectangle)}
	LayoutWidget(view, WidgetOptions{DPI: 96, Size: image.Pt(280, 160), Colors: sharp}, sharpPaint)
	for i, rad := range sharpPaint.radii {
		if rad != 0 {
			t.Fatalf("sharp widget fill[%d] had non-zero radius %d", i, rad)
		}
	}

	soft := base
	soft.CornerRadius = 18
	softPaint := &widgetPaintRecord{labels: make(map[string]image.Rectangle)}
	LayoutWidget(view, WidgetOptions{DPI: 96, Size: image.Pt(280, 160), Colors: soft}, softPaint)
	maxR := 0
	for _, rad := range softPaint.radii {
		if rad > maxR {
			maxR = rad
		}
	}
	if maxR <= 14 {
		t.Fatalf("soft widget max radius = %d, expected > 14", maxR)
	}

	// Title accent bar is off by default and shifts the title right when enabled.
	offPaint := &widgetPaintRecord{labels: make(map[string]image.Rectangle)}
	LayoutWidget(view, WidgetOptions{DPI: 96, Size: image.Pt(280, 160), Colors: base}, offPaint)
	onColors := base
	onColors.TitleAccentBar = true
	onPaint := &widgetPaintRecord{labels: make(map[string]image.Rectangle)}
	LayoutWidget(view, WidgetOptions{DPI: 96, Size: image.Pt(280, 160), Colors: onColors}, onPaint)
	if offPaint.labels["Groups"].Min.X >= onPaint.labels["Groups"].Min.X {
		t.Fatalf("expected title to align further left when accent bar is off: off=%v on=%v", offPaint.labels["Groups"], onPaint.labels["Groups"])
	}

	// Compact list with items floats cleanly without an inner box-in-box border,
	// while an empty drop list still displays its drop guide border.
	populated := v1.View{Title: "Quick", Controls: []v1.Control{{
		ID: "items", Type: "list", Columns: 1, Drop: true,
		Items: []v1.Item{{ID: "1", Text: "Baidu", Hint: "baidu.com", Badge: "WEB"}},
	}}}
	popPaint := &widgetPaintRecord{labels: make(map[string]image.Rectangle)}
	popGeo := LayoutWidget(populated, WidgetOptions{DPI: 96, Size: image.Pt(280, 260), Colors: base}, popPaint)
	for _, s := range popPaint.stroke {
		if s == popGeo.DropZones[0].Area {
			t.Fatalf("populated compact list should not stroke an inner container box: %v", s)
		}
	}
	emptyList := v1.View{Title: "Quick", Controls: []v1.Control{{
		ID: "items", Type: "list", Columns: 1, Drop: true, Text: "Drop here",
	}}}
	emptyPaint := &widgetPaintRecord{labels: make(map[string]image.Rectangle)}
	emptyGeo := LayoutWidget(emptyList, WidgetOptions{DPI: 96, Size: image.Pt(280, 260), Colors: base}, emptyPaint)
	foundGuide := false
	for _, s := range emptyPaint.stroke {
		if s == emptyGeo.DropZones[0].Area {
			foundGuide = true
			break
		}
	}
	if !foundGuide {
		t.Fatalf("empty compact list missing drop guide border: strokes=%+v drop=%v", emptyPaint.stroke, emptyGeo.DropZones[0].Area)
	}
}

func TestWidgetIconSizeScalesGridAndCompactList(t *testing.T) {
	p := theme.DefaultPalette()
	base := WidgetColors{
		Background: p.Background, Surface: p.Surface, Ink: p.Text, Secondary: p.Secondary,
		Border: p.Border, Accent: p.Accent, Selection: p.Selected, Hover: p.Hover,
		Weekend: p.Weekend, Warning: p.Warning, OnAccent: p.OnAccent, OnSelection: p.Text,
		Opacity: 70, Translucent: true, HasCornerRadius: true, CornerRadius: theme.DefaultCornerRadius,
	}
	sampleIcon := image.NewRGBA(image.Rect(0, 0, 64, 64))
	items := []v1.Item{
		{ID: "1", Text: "Adobe", Hint: "folder", Icon: "app:1"},
		{ID: "2", Text: "Blender", Hint: "3D", Icon: "app:2"},
		{ID: "3", Text: "Baidu", Hint: "baidu.com", Icon: "app:3"},
		{ID: "4", Text: "VS Code", Hint: `F:\Projects`, Icon: "app:4"},
		{ID: "5", Text: "Steam", Hint: "games", Icon: "app:5"},
		{ID: "6", Text: "Terminal", Hint: "pwsh", Icon: "app:6"},
		{ID: "7", Text: "Notes", Hint: "docs", Icon: "app:7"},
		{ID: "8", Text: "Music", Hint: "audio", Icon: "app:8"},
	}
	icons := map[string]image.Image{}
	for _, it := range items {
		icons[it.Icon] = sampleIcon
	}

	gridView := v1.View{Title: "Groups", Controls: []v1.Control{{ID: "items", Type: "list", Columns: 4, Drop: true, Reorder: true, Items: items}}}
	listView := v1.View{Title: "Quick", Controls: []v1.Control{{ID: "items", Type: "list", Columns: 1, Drop: true, Reorder: true, Items: items}}}

	var prevCols int
	var prevIconW, prevListRowH, prevListIconW int
	for idx, sz := range []int{22, 40, 66, 72} {
		cols := base
		cols.IconSize = sz
		for _, dpi := range []int{96, 144} {
			opt := WidgetOptions{DPI: dpi, Size: image.Pt(396*dpi/96, 320*dpi/96), Colors: cols, Icons: icons}
			gridPaint := &widgetPaintRecord{labels: make(map[string]image.Rectangle), styles: make(map[string]WidgetText)}
			gridGeo := LayoutWidget(gridView, opt, gridPaint)
			if !reflect.DeepEqual(gridGeo, LayoutWidget(gridView, opt, nil)) {
				t.Fatalf("iconSize=%d dpi=%d: grid paint and hit geometry differ", sz, dpi)
			}
			if len(gridGeo.Hits) == 0 || len(gridPaint.images) == 0 {
				t.Fatalf("iconSize=%d dpi=%d: missing grid tiles/icons", sz, dpi)
			}
			if !gridPaint.images[0].In(gridGeo.Hits[0].Area) || !gridPaint.labels["Adobe"].In(gridGeo.Hits[0].Area) {
				t.Fatalf("iconSize=%d dpi=%d: icon or label outside tile: hit=%v icon=%v label=%v", sz, dpi, gridGeo.Hits[0].Area, gridPaint.images[0], gridPaint.labels["Adobe"])
			}

			listPaint := &widgetPaintRecord{labels: make(map[string]image.Rectangle), styles: make(map[string]WidgetText)}
			listGeo := LayoutWidget(listView, opt, listPaint)
			if !reflect.DeepEqual(listGeo, LayoutWidget(listView, opt, nil)) {
				t.Fatalf("iconSize=%d dpi=%d: list paint and hit geometry differ", sz, dpi)
			}
			if !listPaint.images[0].In(listGeo.Hits[0].Area) || !listPaint.labels["Adobe"].In(listGeo.Hits[0].Area) || !listPaint.labels["folder"].In(listGeo.Hits[0].Area) {
				t.Fatalf("iconSize=%d dpi=%d: compact list elements outside row: hit=%v icon=%v title=%v hint=%v",
					sz, dpi, listGeo.Hits[0].Area, listPaint.images[0], listPaint.labels["Adobe"], listPaint.labels["folder"])
			}

			if dpi == 96 {
				// Count columns on row 0 by matching tile Min.Y.
				rowCols := 0
				y0 := gridGeo.Hits[0].Area.Min.Y
				for _, h := range gridGeo.Hits {
					if h.Area.Min.Y == y0 {
						rowCols++
					}
				}
				iconW := gridPaint.images[0].Dx()
				listRowH := listGeo.Hits[0].Area.Dy()
				listIconW := listPaint.images[0].Dx()
				if idx > 0 {
					if rowCols > prevCols {
						t.Fatalf("iconSize=%d increased columns from %d to %d", sz, prevCols, rowCols)
					}
					if iconW <= prevIconW {
						t.Fatalf("iconSize=%d did not grow icon width: got %d, prev %d", sz, iconW, prevIconW)
					}
					if listRowH < prevListRowH || listIconW < prevListIconW {
						t.Fatalf("iconSize=%d shrank compact list sizing: rowH %d<%d or iconW %d<%d", sz, listRowH, prevListRowH, listIconW, prevListIconW)
					}
				}
				prevCols, prevIconW, prevListRowH, prevListIconW = rowCols, iconW, listRowH, listIconW
			}
		}
	}
	if prevCols >= 4 {
		t.Fatalf("expected 72px icons on 396dp widget to use fewer than 4 columns, got %d", prevCols)
	}
}

func TestDragGhostAndSourcePlaceholderGeometry(t *testing.T) {
	p := theme.DefaultPalette()
	colors := WidgetColors{
		Background: p.Background, Surface: p.Surface, Ink: p.Text, Secondary: p.Secondary,
		Border: p.Border, Accent: p.Accent, Selection: p.Selected, Hover: p.Hover,
		Weekend: p.Weekend, Warning: p.Warning, OnAccent: p.OnAccent, OnSelection: p.Text,
		Opacity: 75, Translucent: true, HasCornerRadius: true, CornerRadius: theme.DefaultCornerRadius,
	}
	sampleIcon := image.NewRGBA(image.Rect(0, 0, 64, 64))
	item := v1.Item{ID: "a", Text: "Blender 5.2", Icon: `C:\Apps\Blender.lnk`}

	plainOpt := DragGhostOptions{DPI: 96, Locale: "zh-CN", Colors: colors, Item: item, Icon: sampleIcon}
	plainSize := MeasureDragGhost(plainOpt)
	plainPaint := &widgetPaintRecord{labels: make(map[string]image.Rectangle), styles: make(map[string]WidgetText)}
	plainCard := LayoutDragGhost(plainOpt, plainPaint)
	if plainSize.X <= 0 || plainSize.Y <= 0 || len(plainPaint.images) != 1 || !plainPaint.images[0].In(plainCard) {
		t.Fatalf("plain drag ghost invalid: size=%v card=%v images=%v", plainSize, plainCard, plainPaint.images)
	}
	if !plainPaint.labels["Blender 5.2"].In(plainCard) {
		t.Fatalf("drag ghost label outside card: label=%v card=%v", plainPaint.labels["Blender 5.2"], plainCard)
	}

	targetOpt := plainOpt
	targetOpt.TargetBadge = "移到 神秘"
	targetSize := MeasureDragGhost(targetOpt)
	targetPaint := &widgetPaintRecord{labels: make(map[string]image.Rectangle), styles: make(map[string]WidgetText)}
	targetCard := LayoutDragGhost(targetOpt, targetPaint)
	if targetSize.Y <= plainSize.Y || !targetPaint.labels["→ 移到 神秘"].In(targetCard) {
		t.Fatalf("target drag ghost missing badge or height growth: plain=%v target=%v labels=%+v", plainSize, targetSize, targetPaint.labels)
	}

	// DraggingItemID dims the source item label in-place without changing hit geometry.
	view := v1.View{Title: "工作", Controls: []v1.Control{{
		ID: "items", Type: "list", Columns: 4, Drop: true, Reorder: true,
		Items: []v1.Item{item, {ID: "b", Text: "Adobe", Icon: `C:\Apps\Adobe`}},
	}}}
	baseOpt := WidgetOptions{DPI: 96, Size: image.Pt(396, 240), Colors: colors, Icons: map[string]image.Image{item.Icon: sampleIcon}}
	dragOpt := baseOpt
	dragOpt.DraggingItemID = "a"
	normalPaint := &widgetPaintRecord{labels: make(map[string]image.Rectangle), styles: make(map[string]WidgetText)}
	dragPaint := &widgetPaintRecord{labels: make(map[string]image.Rectangle), styles: make(map[string]WidgetText)}
	normalGeo := LayoutWidget(view, baseOpt, normalPaint)
	dragGeo := LayoutWidget(view, dragOpt, dragPaint)
	if !reflect.DeepEqual(normalGeo, dragGeo) {
		t.Fatalf("dragging placeholder should preserve hit geometry")
	}
	if dragPaint.styles["Blender 5.2"].Color.A >= normalPaint.styles["Blender 5.2"].Color.A {
		t.Fatalf("source dragged item label should be dimmed: normal=%d drag=%d",
			normalPaint.styles["Blender 5.2"].Color.A, dragPaint.styles["Blender 5.2"].Color.A)
	}
	if dragPaint.styles["Adobe"].Color.A != normalPaint.styles["Adobe"].Color.A {
		t.Fatalf("non-dragged sibling label alpha changed unexpectedly")
	}
}

func TestWidgetV2SliderLayoutAndDragPreview(t *testing.T) {
	p := theme.DefaultPalette()
	colors := WidgetColors{
		Background: p.Background, Surface: p.Surface, Ink: p.Text, Secondary: p.Secondary,
		Border: p.Border, Accent: p.Accent, Selection: p.Selected, Hover: p.Hover,
		Weekend: p.Weekend, Warning: p.Warning, OnAccent: p.OnAccent, OnSelection: p.Text,
		Opacity: 80, Translucent: true,
	}
	view := v1.View{
		UIVersion: 2,
		Title:     "迷你播放器",
		Root: &v2.Node{
			Type: "column",
			ID:   "root",
			Props: v2.Props{
				Gap: 6,
			},
			Children: []v2.Node{
				{Type: "text", ID: "title", Props: v2.Props{Text: "Test Track", Height: 20}},
				{Type: "slider", ID: "position", Props: v2.Props{Value: 0.25, Height: 22}, Events: map[string]string{"submit": "seek"}},
				{Type: "button", ID: "play", Props: v2.Props{Text: "Play", Icon: "play"}, Events: map[string]string{"activate": "play_pause"}},
			},
		},
	}

	for _, dpi := range []int{96, 144} {
		baseOpt := WidgetOptions{DPI: dpi, Size: image.Pt(320*dpi/96, 148*dpi/96), Colors: colors}
		paint := &widgetPaintRecord{labels: make(map[string]image.Rectangle)}
		geo := LayoutWidget(view, baseOpt, paint)
		if !reflect.DeepEqual(geo, LayoutWidget(view, baseOpt, nil)) {
			t.Fatalf("%d DPI: v2 slider paint and hit geometry differ", dpi)
		}
		var sliderHit *WidgetHit
		for i := range geo.Hits {
			if geo.Hits[i].Slider {
				sliderHit = &geo.Hits[i]
				break
			}
		}
		if sliderHit == nil || sliderHit.ControlID != "position" || sliderHit.Area.Empty() || sliderHit.Value != 0.25 {
			t.Fatalf("%d DPI: missing or invalid slider hit: %+v", dpi, geo.Hits)
		}

		// Local drag preview overrides rendered slider value while preserving hit geometry.
		dragOpt := baseOpt
		dragOpt.SliderDragControl = "position"
		dragOpt.SliderDragValue = 0.8
		dragGeo := LayoutWidget(view, dragOpt, &widgetPaintRecord{labels: make(map[string]image.Rectangle)})
		var dragHit *WidgetHit
		for i := range dragGeo.Hits {
			if dragGeo.Hits[i].Slider {
				dragHit = &dragGeo.Hits[i]
				break
			}
		}
		if dragHit == nil || dragHit.Area != sliderHit.Area || dragHit.Value != 0.8 {
			t.Fatalf("%d DPI: drag preview hit = %+v, want area=%v val=0.8", dpi, dragHit, sliderHit.Area)
		}
	}
}
