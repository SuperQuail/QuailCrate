package plugininstall

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	maxArchiveFiles   = 2000
	maxExtractedBytes = 256 << 20
	maxIndexBytes     = 16 << 20
	maxArchiveEntries = 8000
	maxArchivePaths   = 12000
	maxArchivePathLen = 4096
	maxArchiveDepth   = 64
)

type archiveEntry struct {
	name      string
	directory bool
	explicit  bool
}

type packageArchive struct {
	files map[string]*zip.File
	paths map[string]archiveEntry
}

func extractArchive(ctx context.Context, archive, destination, subdir string, byteLimit int64) error {
	source, err := os.Open(archive)
	if err != nil {
		return failure(ErrorArchive, "", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return failure(ErrorArchive, "", err)
	}
	reader := &zipIndexReader{file: source, remaining: maxIndexBytes, indexing: true}
	zr, err := zip.NewReader(reader, info.Size())
	if err != nil {
		return failure(ErrorArchive, "", err)
	}
	reader.indexing = false
	if len(zr.File) > maxArchiveEntries {
		return failure(ErrorArchive, "", errors.New("ZIP contains too many entries"))
	}
	index := packageArchive{
		files: make(map[string]*zip.File),
		paths: make(map[string]archiveEntry),
	}
	for _, file := range zr.File {
		if err := ctx.Err(); err != nil {
			return failure(ErrorArchive, "", err)
		}
		if err := index.add(file); err != nil {
			return failure(ErrorArchive, "", err)
		}
	}
	root, err := index.packageRoot(subdir)
	if err != nil {
		return err
	}

	directories := make(map[string]bool)
	for name, file := range index.files {
		if !strings.HasPrefix(name, root) {
			continue
		}
		relative := strings.TrimPrefix(name, root)
		if err := ctx.Err(); err != nil {
			return failure(ErrorArchive, "", err)
		}
		if err := makeArchiveDirs(destination, path.Dir(relative), directories); err != nil {
			return failure(ErrorArchive, "", err)
		}
		target := filepath.Join(destination, filepath.FromSlash(relative))
		n, err := extractFile(ctx, file, target, byteLimit)
		if err != nil {
			return failure(ErrorArchive, "", err)
		}
		byteLimit -= n
	}
	return nil
}

func (a *packageArchive) add(file *zip.File) error {
	mode := file.Mode().Type()
	if mode != 0 && mode != os.ModeDir {
		return fmt.Errorf("ZIP entry %q is not a regular file or directory", file.Name)
	}
	name := strings.TrimSuffix(file.Name, "/")
	if len(name) > maxArchivePathLen {
		return errors.New("ZIP entry path is too long")
	}
	parts := strings.Split(name, "/")
	if len(parts) > maxArchiveDepth {
		return fmt.Errorf("ZIP entry path is too long: %q", file.Name)
	}
	ignore := false
	for _, part := range parts {
		if !validArchiveComponent(part) {
			return fmt.Errorf("invalid ZIP path %q", file.Name)
		}
		ignore = ignore || strings.EqualFold(part, "__MACOSX") || strings.EqualFold(part, ".DS_Store")
	}
	if ignore {
		return nil
	}
	directory := mode == os.ModeDir
	parent := ""
	for i, part := range parts {
		parent += part
		last := i == len(parts)-1
		entry := archiveEntry{name: parent, directory: directory || !last, explicit: last}
		key := foldPath(parent)
		if old, found := a.paths[key]; found {
			if old.name != entry.name || !old.directory || !entry.directory || old.explicit && entry.explicit {
				return fmt.Errorf("conflicting ZIP paths %q and %q", old.name, parent)
			}
			entry.explicit = old.explicit || entry.explicit
		} else if len(a.paths) == maxArchivePaths {
			return errors.New("ZIP contains too many paths")
		}
		a.paths[key] = entry
		parent += "/"
	}
	if !directory {
		if len(a.files) == maxArchiveFiles {
			return errors.New("ZIP contains more than 2000 files")
		}
		a.files[name] = file
	}
	return nil
}

func (a packageArchive) packageRoot(subdir string) (string, error) {
	if subdir == "" && a.files["manifest.json"] != nil {
		return "", nil
	}

	var wrapper string
	var topLevel, manifests int
	for _, entry := range a.paths {
		if !strings.Contains(entry.name, "/") {
			topLevel++
			if entry.directory {
				wrapper = entry.name + "/"
			}
		}
		if !entry.directory && path.Base(entry.name) == "manifest.json" {
			manifests++
		}
	}
	if subdir != "" {
		if topLevel == 1 && wrapper != "" && a.files[wrapper+subdir+"/manifest.json"] != nil {
			return wrapper + subdir + "/", nil
		}
		if a.files[subdir+"/manifest.json"] != nil {
			return subdir + "/", nil
		}
	} else if topLevel == 1 && wrapper != "" && a.files[wrapper+"manifest.json"] != nil {
		return wrapper, nil
	} else if manifests > 1 {
		return "", failure(ErrorManifestAmbiguous, "", errors.New("ZIP contains multiple plugin manifests"))
	}
	return "", failure(ErrorManifestMissing, subdir, errors.New("manifest.json is missing at the package root"))
}

func validArchiveComponent(name string) bool {
	if name == "" || !utf8.ValidString(name) || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") ||
		strings.ContainsAny(name, `/\:<>"|?*`) || strings.ContainsFunc(name, unicode.IsControl) {
		return false
	}
	if len(name) > 255 && len(utf16.Encode([]rune(name))) > 255 {
		return false
	}
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	if strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT") {
		digit, size := utf8.DecodeRuneInString(base[3:])
		if len(base) == 3+size && strings.ContainsRune("123456789¹²³", digit) {
			return false
		}
	}
	return true
}

func foldPath(name string) string {
	return strings.Map(func(r rune) rune {
		key := unicode.ToUpper(r)
		for other := unicode.SimpleFold(r); other != r; other = unicode.SimpleFold(other) {
			key = min(key, unicode.ToUpper(other))
		}
		return key
	}, name)
}

func makeArchiveDirs(root, relative string, made map[string]bool) error {
	if relative == "." || made[relative] {
		return nil
	}
	if err := makeArchiveDirs(root, path.Dir(relative), made); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(root, filepath.FromSlash(relative)), 0700); err != nil {
		return err
	}
	made[relative] = true
	return nil
}

type contextReader struct {
	context context.Context
	reader  io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// zip.NewReader reads and allocates central-directory records before returning.
// Bound its reads separately from file contents so directory-only archives
// cannot spend the entire compressed-size allowance on index metadata.
type zipIndexReader struct {
	file      *os.File
	remaining int
	indexing  bool
}

func (r *zipIndexReader) ReadAt(p []byte, offset int64) (int, error) {
	if r.indexing {
		if len(p) > r.remaining {
			return 0, errors.New("ZIP metadata exceeds the index size limit")
		}
		r.remaining -= len(p)
	}
	return r.file.ReadAt(p, offset)
}

func extractFile(ctx context.Context, file *zip.File, target string, limit int64) (int64, error) {
	source, err := file.Open()
	if err != nil {
		return 0, err
	}
	defer source.Close()
	destination, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return 0, err
	}
	reader := contextReader{ctx, source}
	n, copyErr := io.Copy(destination, io.LimitReader(reader, limit))
	if n == limit && copyErr == nil {
		extra, err := io.CopyN(io.Discard, reader, 1)
		if extra != 0 {
			copyErr = errors.New("ZIP extraction exceeds the uncompressed size limit")
		} else if err != io.EOF {
			copyErr = err
		}
	}
	return n, errors.Join(copyErr, destination.Close())
}
