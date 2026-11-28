package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	ManifestName   = "update-manifest.json"
	SignatureName  = "update-manifest.sig"
	Platform       = "windows-amd64"
	Installer      = "installer"
	Portable       = "portable"
	PortableManual = "portable-manual"

	maxManifestSize  = 32 << 10
	maxSignatureSize = 512
	maxPackageSize   = 512 << 20
)

type Manifest struct {
	Schema   int     `json:"schema"`
	Version  string  `json:"version"`
	Channel  string  `json:"channel"`
	Platform string  `json:"platform"`
	Assets   []Asset `json:"assets"`
}

type Asset struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Prepared identifies a cached package. Its paths and contents are verified again before deployment.
type Prepared struct {
	Version       string `json:"version"`
	Type          string `json:"type"`
	PackagePath   string `json:"packagePath"`
	ManifestPath  string `json:"manifestPath"`
	SignaturePath string `json:"signaturePath"`
}

func PackageName(kind string) string {
	switch kind {
	case Installer:
		return "QuailCrate-windows-amd64-setup.exe"
	case Portable, PortableManual:
		return "QuailCrate-windows-amd64.zip"
	default:
		return ""
	}
}

func (m Manifest) Asset(kind string) (Asset, bool) {
	for _, asset := range m.Assets {
		if asset.Type == kind {
			return asset, true
		}
	}
	return Asset{}, false
}

func VerifyManifest(data, signature []byte, publicKey ed25519.PublicKey) (Manifest, error) {
	if len(data) > maxManifestSize || len(signature) > maxSignatureSize || len(publicKey) != ed25519.PublicKeySize {
		return Manifest{}, errors.New("invalid update manifest, signature, or key size")
	}
	sig, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(signature)))
	if err != nil || !ed25519.Verify(publicKey, data, sig) {
		return Manifest{}, errors.New("invalid update manifest signature")
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("read update manifest: %w", err)
	}
	if manifest.Schema != 1 || manifest.Platform != Platform || Channel(manifest.Version) == "dev" ||
		manifest.Channel != Channel(manifest.Version) || strings.HasPrefix(manifest.Version, "v") || len(manifest.Assets) == 0 {
		return Manifest{}, errors.New("invalid update manifest metadata")
	}
	seen := make(map[string]bool, len(manifest.Assets))
	for _, asset := range manifest.Assets {
		if !validAsset(asset) || seen[asset.Type] {
			return Manifest{}, errors.New("invalid update manifest asset")
		}
		seen[asset.Type] = true
	}
	return manifest, nil
}

func validAsset(asset Asset) bool {
	if (asset.Type != Installer && asset.Type != Portable) || asset.Name != PackageName(asset.Type) ||
		asset.Size <= 0 || asset.Size > maxPackageSize || len(asset.SHA256) != sha256.Size*2 {
		return false
	}
	digest, err := hex.DecodeString(asset.SHA256)
	return err == nil && hex.EncodeToString(digest) == asset.SHA256
}

func VerifyAsset(path string, asset Asset) error {
	if !validAsset(asset) {
		return errors.New("invalid update asset metadata")
	}
	file, err := openRegularFile(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, asset.Size+1))
	if err != nil {
		return err
	}
	if size != asset.Size || hex.EncodeToString(hash.Sum(nil)) != asset.SHA256 {
		return errors.New("update asset does not match signed size and SHA-256")
	}
	return nil
}

func VerifyPrepared(prepared Prepared, publicKey ed25519.PublicKey) (Manifest, Asset, error) {
	manifest, asset, err := verifyPreparedMetadata(prepared, publicKey)
	if err != nil {
		return Manifest{}, Asset{}, err
	}
	if err := VerifyAsset(prepared.PackagePath, asset); err != nil {
		return Manifest{}, Asset{}, err
	}
	return manifest, asset, nil
}

func verifyPreparedMetadata(prepared Prepared, publicKey ed25519.PublicKey) (Manifest, Asset, error) {
	if filepath.Base(prepared.PackagePath) != PackageName(prepared.Type) || PackageName(prepared.Type) == "" {
		return Manifest{}, Asset{}, errors.New("unexpected update package path")
	}
	data, err := readLocalFile(prepared.ManifestPath, maxManifestSize)
	if err != nil {
		return Manifest{}, Asset{}, err
	}
	signature, err := readLocalFile(prepared.SignaturePath, maxSignatureSize)
	if err != nil {
		return Manifest{}, Asset{}, err
	}
	manifest, err := VerifyManifest(data, signature, publicKey)
	if err != nil {
		return Manifest{}, Asset{}, err
	}
	asset, found := manifest.Asset(prepared.Type)
	if !found || manifest.Version != prepared.Version {
		return Manifest{}, Asset{}, errors.New("cached update does not match the requested version and package type")
	}
	return manifest, asset, nil
}

func readLocalFile(path string, limit int64) ([]byte, error) {
	file, err := openRegularFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimited(file, limit)
}

func openRegularFile(path string) (*os.File, error) {
	entry, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !entry.Mode().IsRegular() {
		return nil, errors.New("update path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	actual, err := file.Stat()
	if err != nil || !os.SameFile(entry, actual) {
		file.Close()
		return nil, errors.New("update file changed while opening")
	}
	return file, nil
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("update response exceeds the size limit")
	}
	return data, nil
}
