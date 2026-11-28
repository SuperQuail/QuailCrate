package plugins

import (
	"strings"
	"testing"

	v1 "quailcrate/pluginapi/v1"
)

func TestValidateManifestInstallMetadata(t *testing.T) {
	base := v1.Manifest{ID: "example.install", Name: "Install", Version: "1.0.0", Runtime: "lua",
		Entry: "plugin.lua", SchemaVersion: 1, HostAPIVersion: 1}
	for _, tc := range []struct {
		name, homepage, minimum string
		valid                   bool
	}{
		{name: "optional", valid: true},
		{name: "https and prerelease", homepage: "https://example.com/docs?q=plugin#install", minimum: "0.3.0-alpha.1+test", valid: true},
		{name: "plain http", homepage: "http://example.com"},
		{name: "missing host", homepage: "https:///docs"},
		{name: "credentials", homepage: "https://user@example.com/docs"},
		{name: "long homepage", homepage: "https://example.com/" + strings.Repeat("a", 512)},
		{name: "leading v", minimum: "v0.3.0"},
		{name: "invalid prerelease identifier", minimum: "0.3.0-alpha.01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			m.Homepage, m.MinHostVersion = tc.homepage, tc.minimum
			if err := ValidateManifest(m); (err == nil) != tc.valid {
				t.Fatalf("ValidateManifest() = %v, want valid %v", err, tc.valid)
			}
		})
	}
}
