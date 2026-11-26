package gioui

import "testing"

func TestMenuViewportOnlyScrollsOnOverflow(t *testing.T) {
	items := []MenuItem{{Label: "Open"}, {Separator: true}, {Label: "Appearance"}}
	for _, dpi := range []int{96, 120, 144, 192} {
		size := MeasureMenu(items, dpi)
		for _, available := range []int{0, size.Y, size.Y + 1} {
			v := NewMenuViewport(items, dpi, available)
			if v.Size != size || v.MaxScroll != 0 || !v.Up.Empty() || !v.Down.Empty() {
				t.Fatalf("dpi %d, height %d: size %v, scroll %d, controls %v %v", dpi, available, v.Size, v.MaxScroll, v.Up, v.Down)
			}
		}
	}
}

func TestMenuViewportScrollAndSelection(t *testing.T) {
	items := make([]MenuItem, 29)
	for _, i := range []int{6, 10, 15, 19, 24} {
		items[i].Separator = true
	}
	items[3].Disabled = true
	const dpi, workHeight = 120, 1000
	v := NewMenuViewport(items, dpi, workHeight)
	if v.Size.Y != workHeight || v.MaxScroll <= 0 || v.Up.Empty() || v.Down.Empty() {
		t.Fatalf("size %v, scroll %d, controls %v %v", v.Size, v.MaxScroll, v.Up, v.Down)
	}
	last := len(items) - 1
	if v.Reveal(last, 0) != v.MaxScroll || v.Reveal(0, v.MaxScroll) != 0 {
		t.Fatal("first and last commands must be reachable when keyboard navigation wraps")
	}
	for _, scroll := range []int{-1, 0, v.RowHeight / 2, v.MaxScroll, v.MaxScroll + 1} {
		opt := MenuOptions{DPI: dpi, Viewport: v, Scroll: scroll}
		hits := LayoutMenu(nil, nil, items, opt)
		if idx := v.HitIndex(v.Up.Min, scroll); idx != -1 {
			t.Fatalf("top scroll control returned command %d", idx)
		}
		if idx := v.HitIndex(v.Down.Min, scroll); idx != -1 {
			t.Fatalf("bottom scroll control returned command %d", idx)
		}
		for i, r := range hits {
			if items[i].Disabled || items[i].Separator {
				if !r.Empty() {
					t.Fatalf("inactive row %d has a hit area %v", i, r)
				}
			} else if !r.Empty() && (!r.In(v.content) || v.HitIndex(r.Min, scroll) != i) {
				t.Fatalf("scroll %d, row %d: visible area %v disagrees with hit testing", scroll, i, r)
			}
		}
		switch {
		case scroll <= 0:
			if hits[0].Dy() != v.RowHeight || !hits[last].Empty() {
				t.Fatalf("top position: first row %v, last row %v", hits[0], hits[last])
			}
		case scroll >= v.MaxScroll:
			if !hits[0].Empty() || hits[last].Dy() != v.RowHeight {
				t.Fatalf("bottom position: first row %v, last row %v", hits[0], hits[last])
			}
		default:
			if hits[0].Dy() != v.RowHeight-scroll {
				t.Fatalf("partially visible first row %v at scroll %d", hits[0], scroll)
			}
		}
	}
}
