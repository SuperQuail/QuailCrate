package v2

import (
	"encoding/json"
	"testing"
)

func TestTreeProtocolValidation(t *testing.T) {
	button := Node{Type: "button", ID: "one", Props: Props{Label: "Next"}, Events: map[string]string{"activate": "calendar.nextMonth"}}
	root := Node{Type: "row", ID: "toolbar", Children: []Node{button}}
	if err := root.ValidateView(); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*Node){
		"duplicate":     func(r *Node) { r.Children[0].ID = r.ID },
		"leaf children": func(r *Node) { r.Children[0].Children = []Node{{Type: "text", ID: "extra"}} },
		"depth": func(r *Node) {
			for i := 0; i < 34; i++ {
				*r = Node{ID: "n" + string(rune('A'+i%26)) + string(rune('A'+i/26)), Type: "column", Children: []Node{*r}}
			}
		},
		"binding":         func(r *Node) { r.Children[0].Events = map[string]string{"click": "next"} },
		"unnamed":         func(r *Node) { r.Children[0].Props.Label = "" },
		"menu and action": func(r *Node) { r.Children[0].Props.Menu = "other" },
		"item keys": func(r *Node) {
			r.Children[0] = Node{Type: "grid", ID: "items", Props: Props{Columns: 1, Items: []Item{{Key: "a"}, {Key: "a"}}}, Events: map[string]string{"activate": "open"}}
		},
		"launcher input": func(r *Node) { r.Children[0] = Node{Type: "searchInput", ID: "query"} },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			bad := root.Clone()
			change(bad)
			if err := bad.ValidateView(); err == nil {
				t.Fatal("accepted invalid tree")
			}
		})
	}
	var parsed Node
	for _, raw := range []string{`{"type":"progress","id":"p","props":{"value":"1"}}`, `{"type":"text","id":"t","props":{"bogus":true}}`} {
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			t.Fatal("accepted unknown or mistyped property", raw)
		}
	}
}
