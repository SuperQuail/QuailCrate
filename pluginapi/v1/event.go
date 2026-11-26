package v1

import "encoding/json"

// Service replies carry typed JSON in value. Legacy action.result retains its string value.
func (e Event) MarshalJSON() ([]byte, error) {
	type wire Event
	if e.Type != "service.result" {
		return json.Marshal(wire(e))
	}
	value := e.Detail
	e.Detail = nil
	return json.Marshal(struct {
		wire
		Value json.RawMessage `json:"value,omitempty"`
	}{wire(e), value})
}
