package plugininstall

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"quailcrate/internal/plugins"
	"quailcrate/internal/update"
)

type ErrorCode string

const (
	ErrorSource            ErrorCode = "source"
	ErrorNetwork           ErrorCode = "network"
	ErrorRateLimit         ErrorCode = "rate_limit"
	ErrorRelease           ErrorCode = "release"
	ErrorAssets            ErrorCode = "assets"
	ErrorAmbiguousAssets   ErrorCode = "ambiguous_assets"
	ErrorDownload          ErrorCode = "download"
	ErrorChecksum          ErrorCode = "checksum"
	ErrorArchive           ErrorCode = "archive"
	ErrorManifestMissing   ErrorCode = "manifest_missing"
	ErrorManifestAmbiguous ErrorCode = "manifest_ambiguous"
	ErrorPackage           ErrorCode = "package"
	ErrorNativeSource      ErrorCode = "native_source"
	ErrorWebSource         ErrorCode = "web_source"
	ErrorReservedID        ErrorCode = "reserved_id"
	ErrorHostVersion       ErrorCode = "host_version"
	ErrorFilesystem        ErrorCode = "filesystem"
)

// Error supplies a code and optional user-facing argument for localized diagnostics.
type Error struct {
	Code   ErrorCode
	Detail string
	err    error
}

func (e *Error) Error() string {
	if e.err == nil {
		return string(e.Code)
	}
	return e.err.Error()
}
func (e *Error) Unwrap() error { return e.err }

func failure(code ErrorCode, detail string, err error) error {
	var known *Error
	if errors.As(err, &known) {
		return err
	}
	return &Error{Code: code, Detail: detail, err: err}
}

type Installer struct {
	CacheDir    string
	HostVersion string
	Client      *http.Client
	Logger      *slog.Logger
}

type StagedPlugin struct {
	Package   plugins.Package
	Source    SourceSpec
	RefOrTag  string
	AssetName string
	Cleanup   func()
}

// Stage downloads and validates a plugin before its files or database metadata are installed.
func (i Installer) Stage(ctx context.Context, rawURL string) (StagedPlugin, error) {
	spec, err := ParseSource(rawURL)
	if err != nil {
		return StagedPlugin{}, err
	}
	if i.CacheDir == "" {
		return StagedPlugin{}, failure(ErrorFilesystem, "", errors.New("plugin installer cache directory is missing"))
	}
	logger := i.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := githubClient(i.Client)
	source, err := resolveArchive(ctx, client, spec)
	if err != nil {
		return StagedPlugin{}, err
	}
	if err := os.MkdirAll(i.CacheDir, 0700); err != nil {
		return StagedPlugin{}, failure(ErrorFilesystem, "", err)
	}
	work, err := os.MkdirTemp(i.CacheDir, "plugin-install-")
	if err != nil {
		return StagedPlugin{}, failure(ErrorFilesystem, "", err)
	}
	archivePath := filepath.Join(work, "package.zip")
	packageDir := filepath.Join(work, "package")
	cleanup := func() {
		// Leave any backup in the workspace intact if rollback could not restore it.
		for _, err := range []error{os.RemoveAll(packageDir), os.Remove(archivePath), os.Remove(work)} {
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				logger.Warn("clean staged plugin package", "error", err)
			}
		}
	}
	success := false
	defer func() {
		if !success {
			cleanup()
		}
	}()
	archive, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return StagedPlugin{}, failure(ErrorFilesystem, "", err)
	}
	download, downloadErr := downloadArchive(ctx, client, source, archive)
	if closeErr := archive.Close(); closeErr != nil && downloadErr == nil {
		return StagedPlugin{}, failure(ErrorFilesystem, "", closeErr)
	}
	if downloadErr != nil {
		return StagedPlugin{}, downloadErr
	}
	if err := os.Mkdir(packageDir, 0700); err != nil {
		return StagedPlugin{}, failure(ErrorFilesystem, "", err)
	}
	if err := extractArchive(ctx, archivePath, packageDir, download.subdir, maxExtractedBytes); err != nil {
		return StagedPlugin{}, err
	}

	pkg, inspectErr := plugins.InspectUserPackage(packageDir)
	id := pkg.Manifest.ID
	if id == "quailcrate" || strings.HasPrefix(id, "quailcrate.") {
		return StagedPlugin{}, failure(ErrorReservedID, "", errors.New("the quailcrate namespace is reserved for bundled plugins"))
	}
	if source.sourceCode && pkg.Manifest.Runtime == "dll" {
		return StagedPlugin{}, failure(ErrorNativeSource, "", errors.New("native DLL plugins require a precompiled GitHub Release ZIP asset"))
	}
	if inspectErr != nil {
		return StagedPlugin{}, failure(ErrorPackage, "", inspectErr)
	}
	if source.sourceCode {
		web, err := os.Stat(filepath.Join(packageDir, "web"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return StagedPlugin{}, failure(ErrorPackage, "", err)
		}
		if err == nil && web.IsDir() {
			return StagedPlugin{}, failure(ErrorWebSource, "", errors.New("plugin packages containing web assets require a GitHub Release ZIP asset"))
		}
	}
	if !validArchiveComponent(id) {
		return StagedPlugin{}, failure(ErrorPackage, "", fmt.Errorf("plugin ID %q cannot be a Windows directory name", id))
	}
	if required := pkg.Manifest.MinHostVersion; required != "" {
		version, _, _ := strings.Cut(i.HostVersion, "+")
		_, prerelease, _ := strings.Cut(version, "-")
		if version == "" || version == "dev" || prerelease == "dev" || strings.HasPrefix(prerelease, "dev.") {
			logger.Info("skip plugin minimum host version for development build", "plugin", id, "required", required, "host", i.HostVersion)
		} else if update.Newer(required, version) {
			return StagedPlugin{}, failure(ErrorHostVersion, required, fmt.Errorf("plugin requires QuailCrate %s or newer", required))
		}
	}
	if err := ctx.Err(); err != nil {
		return StagedPlugin{}, failure(ErrorPackage, "", err)
	}
	success = true
	if spec.Kind == KindTreeArchive {
		spec.Ref, spec.Subdir = download.ref, download.subdir
	}
	return StagedPlugin{Package: pkg, Source: spec, RefOrTag: download.ref, AssetName: source.asset, Cleanup: cleanup}, nil
}

// CommitSwap moves the staged directory into place and restores the previous
// package if either the move or the optional metadata commit fails.
// The caller serializes writes to targetDir and stops any running instance first.
func CommitSwap(targetDir, stagedDir string, commit ...func() error) error {
	if len(commit) > 1 {
		return failure(ErrorFilesystem, "", errors.New("expected at most one plugin metadata commit"))
	}
	if err := os.MkdirAll(filepath.Dir(targetDir), 0700); err != nil {
		return failure(ErrorFilesystem, "", err)
	}
	var backup, backupWork string
	info, err := os.Lstat(targetDir)
	switch {
	case err == nil:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return failure(ErrorFilesystem, "", fmt.Errorf("plugin destination is not a regular directory: %s", targetDir))
		}
		backupWork, err = os.MkdirTemp(filepath.Dir(stagedDir), "plugin-backup-")
		if err != nil {
			return failure(ErrorFilesystem, "", err)
		}
		defer func() {
			if err := os.Remove(backupWork); err != nil && !errors.Is(err, os.ErrNotExist) {
				slog.Warn("clean plugin backup workspace", "error", err)
			}
		}()
		backup = filepath.Join(backupWork, filepath.Base(targetDir)+".bak")
		if err := os.Rename(targetDir, backup); err != nil {
			return failure(ErrorFilesystem, "", err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return failure(ErrorFilesystem, "", err)
	}
	restore := func(cause error) error {
		if backup != "" {
			if err := os.Rename(backup, targetDir); err != nil {
				cause = errors.Join(cause, fmt.Errorf("restore plugin backup %s: %w", backup, err))
			}
		}
		return failure(ErrorFilesystem, "", cause)
	}
	if err := os.Rename(stagedDir, targetDir); err != nil {
		return restore(err)
	}
	if len(commit) != 0 && commit[0] != nil {
		if err := commit[0](); err != nil {
			if moveErr := os.Rename(targetDir, stagedDir); moveErr != nil {
				return failure(ErrorFilesystem, "", errors.Join(err, fmt.Errorf("undo plugin directory move: %w", moveErr)))
			}
			return restore(err)
		}
	}
	if backup != "" {
		if err := os.RemoveAll(backup); err != nil {
			slog.Warn("retire old plugin package", "error", err)
		}
	}
	return nil
}
