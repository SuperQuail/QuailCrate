package update

import (
	"io/fs"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func resolvePortableRoot(name string) (string, error) { return CanonicalRoot(name) }

func portableReparse(info fs.FileInfo) bool {
	attrs, _ := info.Sys().(*syscall.Win32FileAttributeData)
	return info.Mode()&fs.ModeSymlink != 0 || attrs != nil && attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func publishPortableFile(source, destination string, replace bool) error {
	from, err := windows.UTF16PtrFromString(portableWindowsPath(source))
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(portableWindowsPath(destination))
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(from, to, flags)
}

func portableWindowsPath(name string) string {
	if strings.HasPrefix(name, `\\?\`) {
		return name
	}
	if strings.HasPrefix(name, `\\`) {
		return `\\?\UNC\` + name[2:]
	}
	return `\\?\` + name
}
