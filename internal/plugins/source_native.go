package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const nativeRunner = "QuailCrateNative.exe"

type bundledSourceDLLSpec struct {
	dir    string
	id     string
	dll    string
	source string
}

var bundledSourceDLLs []bundledSourceDLLSpec

func matchBundledSourceDLL(directory, id, entry string) (bundledSourceDLLSpec, bool) {
	for _, spec := range bundledSourceDLLs {
		if directory == spec.dir && id == spec.id && entry == spec.dll {
			return spec, true
		}
	}
	return bundledSourceDLLSpec{}, false
}

type nativeArtifacts struct {
	dir        string
	dllHashes  map[string]string
	runnerHash string
}

// SourceNative owns the binaries produced by a single development host.
// A failed build can be retried; other hosts never share partially built files.
type SourceNative struct {
	Build     func(context.Context, string) error
	TempRoot  string
	mu        sync.Mutex
	artifacts *nativeArtifacts
	closed    bool
}

func (source *SourceNative) prepare(ctx context.Context, pkg *Package) error {
	source.mu.Lock()
	defer source.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if source.closed {
		return fmt.Errorf("development native build is closed")
	}
	if source.artifacts == nil {
		tempRoot := source.TempRoot
		if tempRoot != "" {
			if err := os.MkdirAll(tempRoot, 0700); err != nil {
				return err
			}
		}
		dir, err := os.MkdirTemp(tempRoot, "quailcrate-dev-native-")
		if err != nil {
			return err
		}
		out := &nativeArtifacts{dir: dir, dllHashes: make(map[string]string)}
		if err = source.Build(ctx, dir); err == nil {
			out.runnerHash, err = hashBuildOutput(filepath.Join(dir, nativeRunner))
		}
		if err == nil {
			for _, spec := range bundledSourceDLLs {
				hash, hashErr := hashBuildOutput(filepath.Join(dir, spec.dll))
				if hashErr == nil {
					out.dllHashes[spec.id] = hash
					continue
				}
				if !errors.Is(hashErr, os.ErrNotExist) {
					err = hashErr
					break
				}
			}
		}
		if err == nil && pkg.sourceDLL != "" && out.dllHashes[pkg.Manifest.ID] == "" {
			err = fmt.Errorf("missing built native plugin %s: %w", pkg.sourceDLL, os.ErrNotExist)
		}
		if err != nil {
			os.RemoveAll(dir)
			return fmt.Errorf("build native development components: %w", err)
		}
		source.artifacts = out
	}
	pkg.runner = source.artifacts
	if pkg.sourceDLL != "" {
		hash := source.artifacts.dllHashes[pkg.Manifest.ID]
		if hash == "" {
			return fmt.Errorf("missing built native plugin %s: %w", pkg.sourceDLL, os.ErrNotExist)
		}
		builtEntry := filepath.Join(source.artifacts.dir, pkg.sourceDLL)
		pkg.Entry = builtEntry
		pkg.sourceBuiltEntry = builtEntry
		pkg.dllHash = hash
	}
	return nil
}

func hashBuildOutput(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (source *SourceNative) Close() error {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.closed = true
	if source.artifacts == nil {
		return nil
	}
	return os.RemoveAll(source.artifacts.dir)
}
