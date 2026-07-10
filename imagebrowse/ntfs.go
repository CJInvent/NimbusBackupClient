package imagebrowse

// ntfs.go — file listing and extraction from an NTFS partition inside a raw
// disk image, over any io.ReaderAt (in production: pbscommon.FIDXReaderAt, so
// only the chunks the MFT walk touches are ever downloaded).
//
// This is the "sandboxed mount": pure userspace parsing, no kernel driver,
// no admin rights, nothing attached to the host OS. Only this process can
// see the filesystem, read-only by construction.

import (
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	ntfs "www.velocidex.com/golang/go-ntfs/parser"
)

// Entry is one file or directory in the image, shaped to match the GUI's
// existing SnapshotEntry so the same tree UI renders both backup types.
type Entry struct {
	Path    string `json:"path"` // forward-slash, rooted at the partition ("/Windows/win.ini")
	IsDir   bool   `json:"is_dir"`
	Size    uint64 `json:"size"`
	ModTime int64  `json:"mtime"` // unix seconds
}

// ErrCancelled is returned by Walk when the caller's cancel predicate fires.
var ErrCancelled = errors.New("image walk cancelled")

// ErrTooManyEntries is returned by Walk when maxEntries is exceeded; the
// entries gathered so far are still returned so the UI can show a truncated
// tree with an honest banner instead of nothing.
var ErrTooManyEntries = errors.New("entry cap reached")

// Volume is an open NTFS filesystem within a disk image.
type Volume struct {
	ctx *ntfs.NTFSContext
}

// OpenNTFS opens the NTFS filesystem occupying [partOffset, partOffset+length)
// within the image. go-ntfs computes cluster offsets relative to its reader's
// origin (the GetNTFSContext offset parameter positions only the boot
// sector), so the partition is presented as its own io.ReaderAt via a
// SectionReader. The page cache inside go-ntfs sits on top of our chunk-level
// LRU, so repeated MFT record reads are cheap.
func OpenNTFS(image io.ReaderAt, partOffset, length int64) (*Volume, error) {
	sect := io.NewSectionReader(image, partOffset, length)
	ctx, err := ntfs.GetNTFSContext(sect, 0)
	if err != nil {
		return nil, fmt.Errorf("open NTFS at offset %d: %w", partOffset, err)
	}
	return &Volume{ctx: ctx}, nil
}

// root returns the root directory MFT entry (record 5 is defined by NTFS to
// be the volume root).
func (v *Volume) root() (*ntfs.MFT_ENTRY, error) {
	return v.ctx.GetMFT(5)
}

// skipName filters NTFS metafiles ($MFT, $Bitmap, ...) out of listings —
// they aren't user data and half of them are unreadable through the normal
// data path anyway.
func skipName(name string) bool {
	return name == "" || name == "." || strings.HasPrefix(name, "$")
}

// List returns the immediate children of dirPath ("" or "/" = root),
// sorted directories-first then by name, matching the GUI's expectations.
func (v *Volume) List(dirPath string) ([]Entry, error) {
	root, err := v.root()
	if err != nil {
		return nil, fmt.Errorf("read MFT root: %w", err)
	}
	dir := root
	clean := strings.Trim(path.Clean("/"+dirPath), "/")
	if clean != "" && clean != "." {
		dir, err = root.Open(v.ctx, clean)
		if err != nil {
			return nil, fmt.Errorf("open dir %q: %w", clean, err)
		}
	}
	infos := ntfs.ListDir(v.ctx, dir)
	out := make([]Entry, 0, len(infos))
	for _, fi := range infos {
		if skipName(fi.Name) {
			continue
		}
		out = append(out, Entry{
			Path:    "/" + strings.Trim(clean+"/"+fi.Name, "/"),
			IsDir:   fi.IsDir,
			Size:    uint64(fi.Size),
			ModTime: fi.Mtime.Unix(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// Walk gathers the full tree under startPath (breadth-first), for the GUI's
// prebuilt-tree Browse view. maxEntries bounds memory and chunk downloads on
// pathological volumes; cancel (optional) aborts between directories.
func (v *Volume) Walk(startPath string, maxEntries int, cancel func() bool) ([]Entry, error) {
	if maxEntries <= 0 {
		maxEntries = 250000
	}
	var out []Entry
	queue := []string{strings.Trim(path.Clean("/"+startPath), "/")}
	for len(queue) > 0 {
		if cancel != nil && cancel() {
			return out, ErrCancelled
		}
		dir := queue[0]
		queue = queue[1:]
		children, err := v.List(dir)
		if err != nil {
			// A single unreadable directory (corrupt record, exotic
			// attribute) should not kill the whole listing: note and move on.
			continue
		}
		for _, c := range children {
			out = append(out, c)
			if len(out) >= maxEntries {
				return out, ErrTooManyEntries
			}
			if c.IsDir {
				queue = append(queue, strings.TrimPrefix(c.Path, "/"))
			}
		}
	}
	return out, nil
}

// ExtractFile streams the contents of filePath into w, returning bytes
// written. Sparse runs read as zeros (go-ntfs RangeReaderAt semantics),
// which matches what a filesystem copy would produce.
func (v *Volume) ExtractFile(filePath string, w io.Writer) (int64, error) {
	clean := strings.Trim(path.Clean("/"+filePath), "/")
	if clean == "" {
		return 0, fmt.Errorf("empty file path")
	}
	reader, err := ntfs.GetDataForPath(v.ctx, clean)
	if err != nil {
		return 0, fmt.Errorf("open %q: %w", clean, err)
	}
	root, err := v.root()
	if err != nil {
		return 0, err
	}
	entry, err := root.Open(v.ctx, clean)
	if err != nil {
		return 0, fmt.Errorf("stat %q: %w", clean, err)
	}
	size := int64(0)
	for _, fi := range ntfs.Stat(v.ctx, entry) {
		if fi != nil && !fi.IsDir {
			size = fi.Size
			break
		}
	}
	return io.Copy(w, io.NewSectionReader(reader, 0, size))
}

// StatSize returns the file size for filePath (0 for directories), used by
// the free-space preflight before any bytes are extracted.
func (v *Volume) StatSize(filePath string) (int64, bool, error) {
	clean := strings.Trim(path.Clean("/"+filePath), "/")
	root, err := v.root()
	if err != nil {
		return 0, false, err
	}
	entry, err := root.Open(v.ctx, clean)
	if err != nil {
		return 0, false, fmt.Errorf("stat %q: %w", clean, err)
	}
	for _, fi := range ntfs.Stat(v.ctx, entry) {
		if fi != nil {
			return fi.Size, fi.IsDir, nil
		}
	}
	return 0, false, fmt.Errorf("no stat info for %q", clean)
}
