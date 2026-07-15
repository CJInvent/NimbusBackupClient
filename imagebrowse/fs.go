package imagebrowse

// fs.go — the common read-only filesystem view used by every supported
// filesystem inside a disk image (NTFS, FAT12/16/32, exFAT).
//
// Each implementation only has to provide List / Stat / ExtractFile /
// UsedBytes; the recursive Walk is written once here, so tree-building,
// entry caps, and cancellation behave identically for every filesystem.
//
// Everything runs over a plain io.ReaderAt (in production: a SectionReader
// over pbscommon.FIDXReaderAt, so only the disk blocks actually touched are
// downloaded from PBS). No mount, no driver, no admin — the process parses
// the bytes itself and nothing is exposed to the host OS.

import (
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// Entry is one file or directory. Shaped to match the GUI's SnapshotEntry so
// image backups and directory backups render in the same tree.
type Entry struct {
	Path    string `json:"path"` // forward-slash, rooted at the partition ("/Windows/win.ini")
	IsDir   bool   `json:"is_dir"`
	Size    uint64 `json:"size"`
	ModTime int64  `json:"mtime"` // unix seconds; 0 when the filesystem has none
}

// Filesystem is a read-only view of one partition.
type Filesystem interface {
	// List returns the immediate children of dir ("" or "/" = root).
	List(dir string) ([]Entry, error)
	// Stat returns metadata for a single path.
	Stat(p string) (Entry, error)
	// ExtractFile streams the contents of p into w, returning bytes written.
	ExtractFile(p string, w io.Writer) (int64, error)
	// UsedBytes reports allocated-in-use bytes. ok=false means the filesystem
	// could not answer cheaply (the UI then shows "—" rather than a guess).
	UsedBytes() (used int64, ok bool)
	// Kind is the short filesystem name ("ntfs", "fat32", "exfat", ...).
	Kind() string
	// Label is the volume label, if the filesystem carries one.
	Label() string
}

// Sentinel errors surfaced to the GUI with actionable wording.
var (
	// ErrCancelled — the caller's cancel predicate fired mid-walk.
	ErrCancelled = errors.New("image walk cancelled")
	// ErrTooManyEntries — the walk hit maxEntries. Entries gathered so far are
	// still returned, so the UI can show a truncated tree with an honest banner.
	ErrTooManyEntries = errors.New("entry cap reached")
	// ErrUnsupportedFS — the partition holds a filesystem we cannot read.
	ErrUnsupportedFS = errors.New("unsupported filesystem")
	// ErrEncrypted — BitLocker (or similar); nothing to parse without the key.
	ErrEncrypted = errors.New("volume is encrypted")
)

// CleanPath normalizes to a rooted, forward-slash path with no trailing slash.
// Root is "/". Used by every implementation so path handling can't drift.
func CleanPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = path.Clean("/" + strings.Trim(p, "/"))
	return p
}

// splitPath returns the path components below root ("/A/B" -> ["A","B"]).
func splitPath(p string) []string {
	c := strings.Trim(CleanPath(p), "/")
	if c == "" {
		return nil
	}
	return strings.Split(c, "/")
}

// joinPath builds a child path under dir.
func joinPath(dir, name string) string {
	return CleanPath(strings.TrimSuffix(CleanPath(dir), "/") + "/" + name)
}

// sortEntries orders directories first, then by name — the order the GUI tree
// expects, applied uniformly across filesystems.
func sortEntries(e []Entry) {
	sort.Slice(e, func(i, j int) bool {
		if e[i].IsDir != e[j].IsDir {
			return e[i].IsDir
		}
		return strings.ToLower(e[i].Path) < strings.ToLower(e[j].Path)
	})
}

// Walk gathers the tree under startPath breadth-first, for the GUI's
// prebuilt-tree Browse view. maxEntries bounds memory AND the number of disk
// blocks pulled from PBS on pathological volumes; cancel (optional) aborts
// between directories. A single unreadable directory is skipped rather than
// failing the whole listing — one corrupt record should not cost the user
// access to the rest of their backup.
func Walk(fs Filesystem, startPath string, maxEntries int, cancel func() bool) ([]Entry, error) {
	if maxEntries <= 0 {
		maxEntries = int(^uint(0) >> 1) // unlimited: results stay in Go memory
	}
	var out []Entry
	queue := []string{CleanPath(startPath)}
	for len(queue) > 0 {
		if cancel != nil && cancel() {
			return out, ErrCancelled
		}
		dir := queue[0]
		queue = queue[1:]
		children, err := fs.List(dir)
		if err != nil {
			continue
		}
		for _, c := range children {
			out = append(out, c)
			if len(out) >= maxEntries {
				return out, ErrTooManyEntries
			}
			if c.IsDir {
				queue = append(queue, c.Path)
			}
		}
	}
	return out, nil
}

// OpenFilesystem opens the filesystem in p, reading through image (whole-disk
// io.ReaderAt). The partition is presented to the implementation as its own
// SectionReader, so every filesystem sees offsets relative to its volume start.
func OpenFilesystem(image io.ReaderAt, p Partition) (Filesystem, error) {
	if p.Length <= 0 {
		return nil, fmt.Errorf("partition %d has zero length", p.Index)
	}
	sect := io.NewSectionReader(image, p.StartOffset, p.Length)
	switch p.Filesystem {
	case FSNTFS:
		return openNTFS(sect)
	case FSFAT12, FSFAT16, FSFAT32:
		return openFAT(sect)
	case FSExFAT:
		return openExFAT(sect)
	case FSBitLocker:
		return nil, fmt.Errorf("%w: BitLocker-protected volume — restore the full image instead", ErrEncrypted)
	case FSReFS:
		// ReFS is undocumented and its on-disk B+ tree layout differs across
		// versions. No mature pure-Go parser exists, and guessing at structures
		// in a RESTORE tool risks handing back corrupt files — so we refuse
		// rather than approximate. Full-image restore still works.
		return nil, fmt.Errorf("%w: ReFS file browsing is not supported — restore the full image instead", ErrUnsupportedFS)
	default:
		return nil, fmt.Errorf("%w: %s — restore the full image instead", ErrUnsupportedFS, p.Filesystem)
	}
}

// TreeLister is an optional fast path a Filesystem can provide when it can
// enumerate its ENTIRE tree cheaper than a recursive directory walk. NTFS
// implements it by streaming the $MFT sequentially (the WizTree technique):
// one linear read of a single file instead of random reads scattered across
// the volume — which over a chunk-fetching PBS reader is the difference
// between ~1 GB moved and touching most of a 930 GB partition.
type TreeLister interface {
	// FullTree returns every entry, capped at maxEntries (ErrTooManyEntries,
	// entries so far still returned). progress, when non-nil, receives
	// (records processed, records total) as the scan advances.
	FullTree(maxEntries int, cancel func() bool, progress func(done, total int64)) ([]Entry, error)
}

// FullTree lists the whole tree of fs the fastest way it supports: the
// TreeLister fast path when present, the generic breadth-first Walk otherwise
// (FAT/exFAT volumes are small enough that walking them is fine).
func FullTree(fs Filesystem, maxEntries int, cancel func() bool, progress func(done, total int64)) ([]Entry, error) {
	if tl, ok := fs.(TreeLister); ok {
		return tl.FullTree(maxEntries, cancel, progress)
	}
	return Walk(fs, "/", maxEntries, cancel)
}

// Extent is one on-disk run of a file, in PARTITION byte space.
type Extent struct {
	Offset int64
	Length int64
}

// Planner is an optional interface a Filesystem provides when it can report
// WHERE its file table lives on disk before reading it. The caller can then
// download exactly those byte ranges — no read-ahead into unrelated disk
// space, which on a fragmented volume is the difference between fetching the
// file table's size and fetching gigabytes of noise around it.
type Planner interface {
	// StoragePlan returns the file table's logical size and its on-disk
	// extents in partition byte space, ordered as they will be read.
	StoragePlan() (size int64, extents []Extent, err error)
}

// StreamInfo describes one alternate data stream (ADS) of a file.
type StreamInfo struct {
	Name string `json:"name"` // stream name, e.g. "Zone.Identifier"
	Size uint64 `json:"size"`
}

// StreamLister is an optional interface for filesystems that store named
// alternate data streams (NTFS). Restore uses it to carry ADS across when the
// user asks — FAT/exFAT have no such concept, which is exactly why the UI
// greys the option for them.
type StreamLister interface {
	// ListStreams returns the named streams of p (the unnamed main stream is
	// not included).
	ListStreams(p string) ([]StreamInfo, error)
	// ExtractStream writes the named stream's content to w.
	ExtractStream(p, stream string, w io.Writer) (int64, error)
}

// SecurityReader is an optional interface for filesystems that store native
// permissions (NTFS). SecurityDescriptor returns the SELF-RELATIVE security
// descriptor bytes for a path, exactly as the OS stored them; the restore
// side applies them natively. FAT/exFAT have no permissions — the UI greys
// the option for them, which is the honest representation of the format.
type SecurityReader interface {
	SecurityDescriptor(p string) ([]byte, error)
}
