package update

import (
	"regexp"
	"strings"
)

var semverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

type version struct {
	core       [3]string
	prerelease []string
}

func parseVersion(text string) (version, bool) {
	// Limit versions from network metadata before they can enter a URL or cache path.
	if len(text) > 128 {
		return version{}, false
	}
	match := semverPattern.FindStringSubmatch(strings.TrimPrefix(text, "v"))
	if match == nil {
		return version{}, false
	}
	result := version{core: [3]string{match[1], match[2], match[3]}}
	if match[4] != "" {
		result.prerelease = strings.Split(match[4], ".")
		for _, id := range result.prerelease {
			if len(id) > 1 && id[0] == '0' && numeric(id) {
				return version{}, false
			}
		}
	}
	return result, true
}

func numeric(id string) bool {
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func compareNumeric(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

// Newer compares valid Semantic Versions without considering build metadata.
func Newer(candidate, current string) bool {
	a, validA := parseVersion(candidate)
	b, validB := parseVersion(current)
	if !validA || !validB {
		return false
	}
	for i, id := range a.core {
		if c := compareNumeric(id, b.core[i]); c != 0 {
			return c > 0
		}
	}
	if len(a.prerelease) == 0 || len(b.prerelease) == 0 {
		return len(b.prerelease) > 0 && len(a.prerelease) == 0
	}
	for i := 0; i < min(len(a.prerelease), len(b.prerelease)); i++ {
		x, y := a.prerelease[i], b.prerelease[i]
		switch {
		case x == y:
		case numeric(x) && numeric(y):
			return compareNumeric(x, y) > 0
		case numeric(x):
			return false
		case numeric(y):
			return true
		default:
			return x > y
		}
	}
	return len(a.prerelease) > len(b.prerelease)
}

// Channel is the supported release channel, or dev for builds outside the release feed.
func Channel(text string) string {
	v, valid := parseVersion(text)
	if valid && len(v.prerelease) == 0 {
		return "stable"
	}
	if valid && v.prerelease[0] == "alpha" {
		return "alpha"
	}
	return "dev"
}

// CanFollow reports whether the candidate is newer and compatible with the current channel.
func CanFollow(candidate, current string) bool {
	candidateChannel, currentChannel := Channel(candidate), Channel(current)
	return (candidateChannel == "stable" && currentChannel != "dev" ||
		candidateChannel == "alpha" && currentChannel == "alpha") && Newer(candidate, current)
}
