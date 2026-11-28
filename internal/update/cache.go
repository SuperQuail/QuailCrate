package update

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func cachePaths(root, version, kind string) Prepared {
	folder := filepath.Join(root, kind+"-"+version)
	return Prepared{
		Version: version, Type: kind,
		PackagePath:   filepath.Join(folder, PackageName(kind)),
		ManifestPath:  filepath.Join(folder, ManifestName),
		SignaturePath: filepath.Join(folder, SignatureName),
	}
}

func loadPrepared(dir, current, kind string, key ed25519.PublicKey) (Prepared, Asset, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return Prepared{}, Asset{}, nil
	}
	if err != nil {
		return Prepared{}, Asset{}, failure(ErrorCache, err)
	}
	prefix := kind + "-"
	var versions []string
	for _, entry := range entries {
		version, match := strings.CutPrefix(entry.Name(), prefix)
		if entry.IsDir() && match && CanFollow(version, current) {
			versions = append(versions, version)
		}
	}
	sort.SliceStable(versions, func(i, j int) bool { return Newer(versions[i], versions[j]) })
	for _, version := range versions {
		prepared := cachePaths(dir, version, kind)
		if _, asset, err := VerifyPrepared(prepared, key); err == nil {
			return prepared, asset, nil
		}
	}
	return Prepared{}, Asset{}, nil
}

func cacheDownload(ctx context.Context, client *http.Client, directory string, update *candidate, key ed25519.PublicKey, progress func(int64)) (Prepared, error) {
	prepared := cachePaths(directory, update.version, update.asset.Type)
	if _, asset, err := VerifyPrepared(prepared, key); err == nil && asset == update.asset {
		return prepared, nil
	}
	folder := filepath.Dir(prepared.PackagePath)
	if err := os.MkdirAll(folder, 0700); err != nil {
		return Prepared{}, failure(ErrorCache, err)
	}
	file, err := os.CreateTemp(folder, "package-*.part")
	if err != nil {
		return Prepared{}, failure(ErrorCache, err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	tracker := &downloadProgress{update: progress, total: update.asset.Size}
	if err := download(ctx, client, update, io.MultiWriter(file, tracker)); err != nil {
		return Prepared{}, err
	}
	if err := syncAndClose(file); err != nil {
		return Prepared{}, failure(ErrorCache, err)
	}
	if err := VerifyAsset(file.Name(), update.asset); err != nil {
		return Prepared{}, failure(ErrorIntegrity, err)
	}
	if err := writeCacheFile(prepared.ManifestPath, update.manifest); err != nil {
		return Prepared{}, failure(ErrorCache, err)
	}
	if err := writeCacheFile(prepared.SignaturePath, update.signature); err != nil {
		return Prepared{}, failure(ErrorCache, err)
	}
	if err := os.Rename(file.Name(), prepared.PackagePath); err != nil {
		return Prepared{}, failure(ErrorCache, err)
	}
	return prepared, nil
}

type downloadProgress struct {
	done, total int64
	percent     int64
	update      func(int64)
}

func (t *downloadProgress) Write(p []byte) (int, error) {
	t.done += int64(len(p))
	completed := min(t.done, t.total)
	if percent := 100 * completed / t.total; percent != t.percent {
		t.percent = percent
		t.update(completed)
	}
	return len(p), nil
}

func writeCacheFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".update-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := syncAndClose(file); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func syncAndClose(file *os.File) error {
	syncErr := file.Sync()
	return errors.Join(syncErr, file.Close())
}
