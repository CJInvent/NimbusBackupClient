//go:build windows

package pbscommon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// platformEntryAttrs reads the attributes Lstat already fetched and, only for
// a reparse point, the tag: Go keeps the tag in an unexported field, so it is
// read again with FindFirstFile (WIN32_FIND_DATA.dwReserved0), which works on
// shadow-copy (\\?\GLOBALROOT) paths and opens nothing.
func platformEntryAttrs(path string, fi os.FileInfo) (uint32, uint32, error) {
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok || d == nil {
		return 0, 0, nil
	}
	attrs := d.FileAttributes
	if attrs&fileAttributeReparsePoint == 0 {
		return attrs, 0, nil
	}
	p, err := syscall.UTF16PtrFromString(extendedPath(path))
	if err != nil {
		return attrs, 0, fmt.Errorf("reparse tag of %s: %w", path, err)
	}
	var fd syscall.Win32finddata
	h, err := syscall.FindFirstFile(p, &fd)
	if err != nil {
		return attrs, 0, fmt.Errorf("reparse tag of %s: %w", path, err)
	}
	_ = syscall.FindClose(h)
	return attrs, fd.Reserved0, nil
}

// extendedPath gives FindFirstFile a path it accepts beyond MAX_PATH: Go's os
// package does this internally for its own calls, syscall does not.
func extendedPath(path string) string {
	switch {
	case strings.HasPrefix(path, `\\?\`), strings.HasPrefix(path, `\\.\`):
		return path
	case strings.HasPrefix(path, `\\`):
		return `\\?\UNC\` + strings.TrimPrefix(filepath.Clean(path), `\\`)
	case filepath.IsAbs(path):
		return `\\?\` + filepath.Clean(path)
	default:
		return path
	}
}
