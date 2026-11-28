package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"quailcrate/internal/update"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("quailcrate-update-release", flag.ContinueOnError)
	version := flags.String("version", "", "release version")
	stage := flags.String("stage", "", "staged portable package directory")
	setup := flags.String("setup", "", "installer package file")
	zip := flags.String("zip", "", "portable package file")
	out := flags.String("out", "", "output directory for update metadata")
	if len(args) < 1 {
		return errors.New("expected public-key, portable, or release command")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected signing argument")
	}
	key, err := readKey(input)
	if err != nil {
		return err
	}
	defer clear(key)
	switch args[0] {
	case "public-key":
		_, err := fmt.Fprintln(output, base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)))
		return err
	case "portable":
		if *stage == "" || update.Channel(*version) == "dev" {
			return errors.New("portable signing requires a stage and a release version")
		}
		return update.WritePortableManifest(*stage, *version, key)
	case "release":
		if *zip == "" || *setup == "" || *out == "" || update.Channel(*version) == "dev" {
			return errors.New("release signing requires a version, output directory, and both packages")
		}
		return writeReleaseManifest(*version, *setup, *zip, *out, key)
	default:
		return errors.New("expected public-key, portable, or release command")
	}
}

func readKey(input io.Reader) (ed25519.PrivateKey, error) {
	data, err := io.ReadAll(io.LimitReader(input, 513))
	if err != nil {
		return nil, err
	}
	defer clear(data)
	if len(data) > 512 {
		return nil, errors.New("invalid update signing key")
	}
	encoded := bytes.TrimSpace(data)
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	defer clear(decoded)
	size, err := base64.StdEncoding.Decode(decoded, encoded)
	if err != nil || (size != ed25519.SeedSize && size != ed25519.PrivateKeySize) {
		return nil, errors.New("update signing key must be a base64 Ed25519 seed or private key")
	}
	key := ed25519.NewKeyFromSeed(decoded[:ed25519.SeedSize])
	if size == ed25519.PrivateKeySize && !bytes.Equal(key, decoded[:size]) {
		clear(key)
		return nil, errors.New("invalid Ed25519 private key")
	}
	return key, nil
}

func writeReleaseManifest(version, setup, zip, out string, key ed25519.PrivateKey) error {
	version = strings.TrimPrefix(version, "v")
	installer, err := describeAsset(setup, update.Installer)
	if err != nil {
		return err
	}
	portable, err := describeAsset(zip, update.Portable)
	if err != nil {
		return err
	}
	manifest := update.Manifest{
		Schema: 1, Version: version, Channel: update.Channel(version), Platform: update.Platform,
		Assets: []update.Asset{installer, portable},
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	signature := []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, data)) + "\n")
	if _, err := update.VerifyManifest(data, signature, key.Public().(ed25519.PublicKey)); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, update.ManifestName), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, update.SignatureName), signature, 0o644)
}

func describeAsset(path, kind string) (update.Asset, error) {
	file, err := os.Open(path)
	if err != nil {
		return update.Asset{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return update.Asset{}, err
	}
	return update.Asset{Type: kind, Name: update.PackageName(kind), Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}
