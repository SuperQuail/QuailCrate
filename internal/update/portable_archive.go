package update

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func stagePortableArchive(archive, stage, version string, key ed25519.PublicKey) (map[string]PortableFile, error) {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	entries := make(map[string]*zip.File, len(reader.File))
	for _, entry := range reader.File {
		name := strings.TrimSuffix(entry.Name, "/")
		if err := validatePortablePath(name); err != nil {
			return nil, err
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		id := strings.ToLower(name)
		if entries[id] != nil || !entry.Mode().IsRegular() || name != entry.Name {
			return nil, fmt.Errorf("invalid portable archive entry: %s", entry.Name)
		}
		entries[id] = entry
	}
	contents, err := readPortableArchiveEntry(entries[portableManifestName], portableMetadataLimit)
	if err != nil {
		return nil, err
	}
	signature, err := readPortableArchiveEntry(entries[portableSignatureName], 256)
	if err != nil {
		return nil, err
	}
	manifest, inventory, err := verifyPortableInventory(contents, signature, key)
	if err != nil {
		return nil, err
	}
	if manifest.Version != version || len(entries) != len(inventory) {
		return nil, errors.New("portable archive does not match the selected release")
	}
	for id, file := range inventory {
		entry := entries[id]
		if entry == nil || entry.Name != file.Path || entry.UncompressedSize64 != uint64(file.Size) {
			return nil, fmt.Errorf("portable archive does not match ownership for %s", file.Path)
		}
		if err := extractPortableEntry(entry, stage, file); err != nil {
			return nil, fmt.Errorf("stage %s: %w", file.Path, err)
		}
	}
	return inventory, nil
}

func readPortableArchiveEntry(entry *zip.File, limit int64) ([]byte, error) {
	if entry == nil || entry.UncompressedSize64 > uint64(limit) {
		return nil, errors.New("missing or oversized portable archive metadata")
	}
	contents, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer contents.Close()
	return readPortableLimit(contents, limit)
}

func extractPortableEntry(entry *zip.File, stage string, file PortableFile) error {
	target := filepath.Join(stage, filepath.FromSlash(file.Path))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	contents, err := entry.Open()
	if err != nil {
		return err
	}
	defer contents.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	digest := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(output, digest), io.LimitReader(contents, file.Size+1))
	if copyErr != nil || size != file.Size || hex.EncodeToString(digest.Sum(nil)) != file.SHA256 {
		return errors.Join(errors.New("portable archive file verification failed"), copyErr, output.Close())
	}
	return errors.Join(output.Sync(), output.Close())
}
