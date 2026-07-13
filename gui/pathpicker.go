package main

// pathpicker.go — the backend for the in-app folder / Save-As picker.
//
// WHY THIS EXISTS: the Wails native dialogs (SaveFileDialog /
// OpenDirectoryDialog) take a native COM fault on this codebase — the whole
// process dies, tray icon and all, with no Go panic to log and nothing
// recover() can catch. That is unacceptable in a backup tool, and it is not
// something we can chase from a Linux dev box. So we stop calling them and
// render the picker ourselves in the webview instead, over these three plain
// filesystem-enumeration methods. Pure Go, no COM, no shell APIs, no driver:
// it CANNOT take the process down, and it behaves the same in every process
// (GUI, elevated, or otherwise).
//
// Deliberately untagged (no !service): the service never calls these, but
// keeping them out of the tag matrix removes a whole class of build-tag
// mistakes, and they are inert if unused.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DriveInfo is one mounted drive for the picker's drive rail.
type DriveInfo struct {
	Path       string `json:"path"`  // "C:\" (Windows) or "/" (POSIX)
	Label      string `json:"label"` // volume label, when the OS gives us one
	FreeBytes  uint64 `json:"free_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
	Ready      bool   `json:"ready"` // false = present but not readable (empty card reader, etc.)
}

// FolderEntry is one subdirectory inside the browsed folder.
type FolderEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// FolderListing is one level of the picker: where we are, what is under it,
// and how much room the underlying drive has (so the picker can show free
// space at the moment of choosing, not after).
type FolderListing struct {
	Path       string        `json:"path"`
	Parent     string        `json:"parent"` // "" when already at a drive root
	Folders    []FolderEntry `json:"folders"`
	FreeBytes  uint64        `json:"free_bytes"`
	TotalBytes uint64        `json:"total_bytes"`
	Writable   bool          `json:"writable"`
}

// ListDrives enumerates drives for the picker. Never errors: a drive that
// cannot be interrogated is returned with Ready=false rather than omitted, so
// the user can see it exists and why it is unusable.
func (a *App) ListDrives() []DriveInfo {
	roots := logicalDriveRoots()
	out := make([]DriveInfo, 0, len(roots))
	for _, r := range roots {
		d := DriveInfo{Path: r, Label: volumeLabel(r)}
		if free, total, err := driveSpace(r); err == nil && total > 0 {
			d.FreeBytes, d.TotalBytes, d.Ready = free, total, true
		}
		out = append(out, d)
	}
	return out
}

// ListFolders returns the subdirectories of path, for the picker's main pane.
// Files are deliberately NOT returned: the picker chooses a destination
// folder (or a folder + filename for Save As), so listing files would be
// noise. Unreadable entries are skipped rather than failing the whole listing
// — one permission-denied folder must not make the picker unusable.
func (a *App) ListFolders(path string) (FolderListing, error) {
	if strings.TrimSpace(path) == "" {
		path = a.DefaultSaveDir()
	}
	path = filepath.Clean(path)

	st, err := os.Stat(path)
	if err != nil {
		return FolderListing{}, fmt.Errorf("cannot open %s: %v", path, err)
	}
	if !st.IsDir() {
		return FolderListing{}, fmt.Errorf("%s is not a folder", path)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return FolderListing{}, fmt.Errorf("cannot read %s: %v", path, err)
	}

	l := FolderListing{Path: path}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Hidden/system folders are noise in a save dialog; keep them out but
		// do not pretend they are absent if the user types the path directly.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		l.Folders = append(l.Folders, FolderEntry{
			Name: e.Name(),
			Path: filepath.Join(path, e.Name()),
		})
	}
	sort.Slice(l.Folders, func(i, j int) bool {
		return strings.ToLower(l.Folders[i].Name) < strings.ToLower(l.Folders[j].Name)
	})

	if parent := filepath.Dir(path); parent != path {
		l.Parent = parent
	}
	if free, total, err := driveSpace(path); err == nil {
		l.FreeBytes, l.TotalBytes = free, total
	}
	l.Writable = isWritableDir(path)
	return l, nil
}

// CreateFolder makes a new folder inside parent and returns its full path.
// The picker needs this because a native "New folder" button is exactly the
// convenience users lose when we drop the native dialog.
func (a *App) CreateFolder(parent, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("folder name required")
	}
	// Reject path separators and traversal outright — this method takes a NAME,
	// not a path, and must not be turned into an arbitrary-write primitive.
	if strings.ContainsAny(name, `/\:*?"<>|`) || name == "." || name == ".." {
		return "", errors.New(`folder name cannot contain / \ : * ? " < > |`)
	}
	if _, err := os.Stat(parent); err != nil {
		return "", fmt.Errorf("cannot open %s: %v", parent, err)
	}
	full := filepath.Join(parent, name)
	if _, err := os.Stat(full); err == nil {
		return "", fmt.Errorf("%s already exists", name)
	}
	if err := os.Mkdir(full, 0o755); err != nil {
		return "", fmt.Errorf("cannot create folder: %v", err)
	}
	writeDebugLog("PathPicker: created folder " + full)
	return full, nil
}

// DefaultSaveDir is where the picker opens if the user has no path yet.
func (a *App) DefaultSaveDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		dl := filepath.Join(home, "Downloads")
		if st, err := os.Stat(dl); err == nil && st.IsDir() {
			return dl
		}
		return home
	}
	if roots := logicalDriveRoots(); len(roots) > 0 {
		return roots[0]
	}
	return "."
}

// isWritableDir probes with a real temp file rather than reading permission
// bits, which on Windows are not a reliable predictor of "can I actually
// write here" (ACLs, virtualization, read-only media all lie).
func isWritableDir(dir string) bool {
	f, err := os.CreateTemp(dir, ".nimbus-write-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}
