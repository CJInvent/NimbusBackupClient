package imagebrowse

// ntfs.go — read-only NTFS support, layered on go-ntfs
// (www.velocidex.com/golang/go-ntfs — pure Go, no cgo, read-only).
//
// The volume is presented to go-ntfs as its own io.ReaderAt (a SectionReader
// over the whole-disk image), so cluster offsets resolve relative to the
// partition start. Every read ultimately lands on pbscommon.FIDXReaderAt,
// which pulls only the 4 MB image chunks the MFT walk actually touches.

import (
	"fmt"
	"io"
	"math/bits"
	"strings"

	ntfs "www.velocidex.com/golang/go-ntfs/parser"
)

// ntfsUsedBitmapCap bounds the $Bitmap read used for used-space reporting.
// $Bitmap is one bit per cluster: at the standard 4 KiB cluster size, 64 MiB
// of bitmap covers a ~2 TB volume. Past that we report "unknown" rather than
// pull hundreds of MB from PBS just to draw a number.
const ntfsUsedBitmapCap = 64 << 20

type ntfsFS struct {
	ctx *ntfs.NTFSContext
}

func openNTFS(r io.ReaderAt) (Filesystem, error) {
	ctx, err := ntfs.GetNTFSContext(r, 0)
	if err != nil {
		return nil, fmt.Errorf("open NTFS volume: %w", err)
	}
	return &ntfsFS{ctx: ctx}, nil
}

func (f *ntfsFS) Kind() string  { return FSNTFS }
func (f *ntfsFS) Label() string { return "" } // identity comes from the GPT type + size columns

// root returns the root directory. MFT record 5 is defined by NTFS to be the
// volume root.
func (f *ntfsFS) root() (*ntfs.MFT_ENTRY, error) {
	return f.ctx.GetMFT(5)
}

// isMeta filters NTFS metafiles ($MFT, $Bitmap, ...) out of user-facing
// listings — they are not user data, and several are unreadable through the
// ordinary data path.
func isMeta(name string) bool {
	return name == "" || name == "." || name == ".." || strings.HasPrefix(name, "$")
}

func (f *ntfsFS) open(p string) (*ntfs.MFT_ENTRY, error) {
	root, err := f.root()
	if err != nil {
		return nil, fmt.Errorf("read MFT root: %w", err)
	}
	rel := strings.Trim(CleanPath(p), "/")
	if rel == "" {
		return root, nil
	}
	e, err := root.Open(f.ctx, rel)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", p, err)
	}
	return e, nil
}

func (f *ntfsFS) List(dir string) ([]Entry, error) {
	d, err := f.open(dir)
	if err != nil {
		return nil, err
	}
	base := CleanPath(dir)
	infos := ntfs.ListDir(f.ctx, d)
	out := make([]Entry, 0, len(infos))
	for _, fi := range infos {
		if fi == nil || isMeta(fi.Name) {
			continue
		}
		out = append(out, Entry{
			Path:    joinPath(base, fi.Name),
			IsDir:   fi.IsDir,
			Size:    uint64(fi.Size),
			ModTime: fi.Mtime.Unix(),
		})
	}
	sortEntries(out)
	return out, nil
}

func (f *ntfsFS) Stat(p string) (Entry, error) {
	if CleanPath(p) == "/" {
		return Entry{Path: "/", IsDir: true}, nil
	}
	e, err := f.open(p)
	if err != nil {
		return Entry{}, err
	}
	for _, fi := range ntfs.Stat(f.ctx, e) {
		if fi == nil {
			continue
		}
		return Entry{
			Path:    CleanPath(p),
			IsDir:   fi.IsDir,
			Size:    uint64(fi.Size),
			ModTime: fi.Mtime.Unix(),
		}, nil
	}
	return Entry{}, fmt.Errorf("no metadata for %q", p)
}

func (f *ntfsFS) ExtractFile(p string, w io.Writer) (int64, error) {
	e, err := f.Stat(p)
	if err != nil {
		return 0, err
	}
	if e.IsDir {
		return 0, fmt.Errorf("%s is a directory", p)
	}
	rel := strings.Trim(CleanPath(p), "/")
	reader, err := ntfs.GetDataForPath(f.ctx, rel)
	if err != nil {
		return 0, fmt.Errorf("open data stream for %q: %w", p, err)
	}
	// Sparse runs read as zeros (go-ntfs RangeReaderAt semantics) — the same
	// bytes a filesystem-level copy would produce.
	n, err := io.Copy(w, io.NewSectionReader(reader, 0, int64(e.Size)))
	if err != nil {
		return n, fmt.Errorf("read %q: %w", p, err)
	}
	if n != int64(e.Size) {
		return n, fmt.Errorf("%s: read %d bytes, expected %d (corrupt volume?)", p, n, e.Size)
	}
	return n, nil
}

// UsedBytes popcounts $Bitmap (one bit per cluster). Bounded by
// ntfsUsedBitmapCap; beyond that we honestly report "unknown" instead of
// downloading a huge bitmap just to render a size column.
func (f *ntfsFS) UsedBytes() (int64, bool) {
	clusterSize := f.ctx.ClusterSize
	if clusterSize <= 0 {
		return 0, false
	}
	// $Bitmap is MFT record 6, and also reachable by name from the root.
	info, err := f.Stat("/$Bitmap")
	size := int64(0)
	if err == nil {
		size = int64(info.Size)
	} else {
		e, merr := f.ctx.GetMFT(6)
		if merr != nil {
			return 0, false
		}
		for _, fi := range ntfs.Stat(f.ctx, e) {
			if fi != nil {
				size = fi.Size
				break
			}
		}
	}
	if size <= 0 || size > ntfsUsedBitmapCap {
		return 0, false
	}
	reader, err := ntfs.GetDataForPath(f.ctx, "$Bitmap")
	if err != nil {
		return 0, false
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(io.NewSectionReader(reader, 0, size), raw); err != nil {
		return 0, false
	}
	used := 0
	for _, b := range raw {
		used += bits.OnesCount8(b)
	}
	return int64(used) * clusterSize, true
}
