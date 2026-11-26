package v2

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const Version = 2
const SearchProvider = "quailcrate.search.provider"
const ResultActions = "quailcrate.result.actions"
const GroupItems = "quailcrate.groups.items"
const Items = "quailcrate.items"

type Export struct {
	ID      string   `json:"id"`
	Version string   `json:"version"`
	Methods []string `json:"methods"`
}
type Available struct {
	Export
	Provider string `json:"provider"`
}
type Dependency struct {
	ID           string   `json:"id"`
	VersionRange string   `json:"versionRange"`
	Methods      []string `json:"methods"`
	Optional     bool     `json:"optional,omitempty"`
}
type ItemRef struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
}
type ResultAction struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Kinds   []string `json:"kinds"`
	Service string   `json:"service"`
	Method  string   `json:"method"`
	Version string   `json:"version"`
}
type Reply struct {
	Status string          `json:"status"`
	Value  json.RawMessage `json:"value,omitempty"`
	Code   string          `json:"code,omitempty"`
}

func Grant(service, method string) string { return "service:" + service + "/" + method }

// Interface versions use stable major.minor.patch releases. Each space-separated
// comparison in a range must hold. An unprefixed version means exact equality.
func parseVersion(s string) ([3]uint64, bool) {
	var v [3]uint64
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return v, false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return v, false
			}
		}
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}
func ValidVersion(s string) bool { _, ok := parseVersion(s); return ok }
func Matches(version, constraints string) (bool, error) {
	v, ok := parseVersion(version)
	if !ok {
		return false, fmt.Errorf("invalid service version %q", version)
	}
	fields := strings.Fields(constraints)
	if len(fields) == 0 || len(fields) > 8 {
		return false, fmt.Errorf("invalid version range %q", constraints)
	}
	match := true
	for _, part := range fields {
		op := "="
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(part, candidate) {
				op = candidate
				part = part[len(candidate):]
				break
			}
		}
		bound, ok := parseVersion(part)
		if !ok {
			return false, fmt.Errorf("invalid version range %q", constraints)
		}
		cmp := 0
		for i := range v {
			if v[i] < bound[i] {
				cmp = -1
				break
			}
			if v[i] > bound[i] {
				cmp = 1
				break
			}
		}
		switch op {
		case "=":
			match = match && cmp == 0
		case ">":
			match = match && cmp > 0
		case "<":
			match = match && cmp < 0
		case ">=":
			match = match && cmp >= 0
		case "<=":
			match = match && cmp <= 0
		}
	}
	return match, nil
}
