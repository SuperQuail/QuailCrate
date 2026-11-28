//go:build !windows

package update

import (
	"io/fs"
	"os"
	"path/filepath"
)

func resolvePortableRoot(name string) (string, error) { return filepath.EvalSymlinks(name) }

func portableReparse(info fs.FileInfo) bool { return info.Mode()&fs.ModeSymlink != 0 }

func publishPortableFile(source, destination string, replace bool) error {
	if replace {
		return os.Rename(source, destination)
	}
	if err := os.Link(source, destination); err != nil {
		return err
	}
	return os.Remove(source)
}
