package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"quailcrate/internal/update"
	"strings"
	"testing"
)

func TestSignRelease(t *testing.T) {
	seed := bytes.Repeat([]byte{11}, ed25519.SeedSize)
	encoded := base64.StdEncoding.EncodeToString(seed)
	var output bytes.Buffer
	if err := run([]string{"public-key"}, strings.NewReader(encoded), &output); err != nil {
		t.Fatal(err)
	}
	public, err := base64.StdEncoding.DecodeString(strings.TrimSpace(output.String()))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	setup := filepath.Join(dir, "test-setup.exe")
	zip := filepath.Join(dir, "test.zip")
	for _, name := range []string{setup, zip} {
		if err := os.WriteFile(name, []byte(filepath.Base(name)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := run([]string{"release", "-version", "1.2.3-alpha.10", "-setup", setup, "-zip", zip, "-out", dir}, strings.NewReader(encoded), &output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, update.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	signature, err := os.ReadFile(filepath.Join(dir, update.SignatureName))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := update.VerifyManifest(data, signature, public)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "1.2.3-alpha.10" || manifest.Channel != "alpha" {
		t.Fatalf("unexpected release metadata: %+v", manifest)
	}
	for kind, file := range map[string]string{update.Installer: setup, update.Portable: zip} {
		asset, ok := manifest.Asset(kind)
		if !ok {
			t.Fatalf("missing %s asset", kind)
		}
		if err := update.VerifyAsset(file, asset); err != nil {
			t.Fatal(err)
		}
	}
	data[0] ^= 1
	if _, err := update.VerifyManifest(data, signature, public); err == nil {
		t.Fatal("tampered manifest passed verification")
	}
}
