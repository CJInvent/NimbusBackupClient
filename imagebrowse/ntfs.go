package imagebrowse

// ntfs.go — read-only NTFS support, layered on go-ntfs
// (www.velocidex.com/golang/go-ntfs — pure Go, no cgo, read-only).
//
// The volume is presented to go-ntfs as its own io.ReaderAt (a SectionReader
// over the whole-disk image), so cluster offsets resolve relative to the
// partition start. Every read ultimately lands on pbscommon.FIDXReaderAt,
// which pulls only the 4 MB image chunks the MFT walk actually touches.

import (
	"context"
	"fmt"
	"io"
	"math/bits"
	"slices"
	"strings"
	"sync"

	ntfs "www.velocidex.com/golang/go-ntfs/parser"
)

// ntfsUsedBitmapCap bounds the $Bitmap read used for used-space reporting.
// $Bitmap is one bit per cluster: at the standard 4 KiB cluster size, 64 MiB
// of bitmap covers a ~2 TB volume. Past that we report "unknown" rather than
// pull hundreds of MB from PBS just to draw a number.
const ntfsUsedBitmapCap = 64 << 20

type ntfsFS struct {
	ctx *ntfs.NTFSContext

	// $SDS security-descriptor index, built lazily on first permissions
	// request (see ntfs_security.go). Browsing never pays for it.
	sdsOnce sync.Once
	sds     map[uint32][]byte
	sdsErr  error
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
	if name == "" || name == "." || name == ".." {
		return true
	}
	// ListDir surfaces alternate data streams as "name:stream" pseudo-entries;
	// those are not files — they are carried by StreamLister and the ADS
	// restore option. NB: legitimate $-prefixed USER directories (e.g.
	// $WINDOWS.~BT) are NOT filtered here; only the reserved metafile records
	// are excluded, and that happens by MFT entry number in FullTree.
	if strings.Contains(name, ":") {
		return true
	}
	return false
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
		// Reserved metafiles live in the root; hide them there (FullTree does
		// the same by entry number < 24). $-named USER dirs elsewhere show.
		if base == "/" && strings.HasPrefix(fi.Name, "$") && ntfsReservedName(fi.Name) {
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

// ---- fast full-tree via sequential $MFT scan (the WizTree technique) ---------

// mftRec is the in-memory skeleton of one MFT record: just enough to rebuild
// the tree without touching the disk again.
type mftRec struct {
	parent uint64
	name   string
	isDir  bool
	size   uint64
	mtime  int64
}

// ntfsMaxRecords bounds the in-memory record map (~100 bytes/record; 4M
// records ≈ 400 MB worst case). A volume past this is beyond what a prebuilt
// GUI tree can present anyway.
const ntfsMaxRecords = 4_000_000

// FullTree implements TreeLister: ONE sequential read of the $MFT stream,
// then the tree is rebuilt in memory from parent references. No per-directory
// disk reads at all — this is how WizTree lists a volume in seconds, and over
// a PBS chunk reader it means fetching ~the MFT's size instead of most of the
// volume.
func (f *ntfsFS) FullTree(maxEntries int, cancel func() bool, progress func(done, total int64)) ([]Entry, error) {
	if maxEntries <= 0 {
		// Unlimited. The result lives in the GO-side session cache and is
		// served to the UI one directory at a time — there is no JSON-size
		// reason to cap it, and capping was exactly the "most of my C: drive
		// is hiding" bug: a random (map-ordered!) 250k subset of 1.4M records.
		maxEntries = int(^uint(0) >> 1)
	}

	// The $MFT is itself a file (record 0); read it as a stream.
	mftReader, err := ntfs.GetDataForPath(f.ctx, "$MFT")
	if err != nil {
		return nil, fmt.Errorf("open $MFT: %w", err)
	}
	var mftSize int64
	if e0, err := f.ctx.GetMFT(0); err == nil {
		for _, fi := range ntfs.Stat(f.ctx, e0) {
			if fi != nil && fi.Size > 0 {
				mftSize = fi.Size
				break
			}
		}
	}
	if mftSize <= 0 {
		return nil, fmt.Errorf("cannot determine $MFT size")
	}
	recordSize := f.ctx.RecordSize
	if recordSize <= 0 {
		recordSize = 1024
	}
	totalRecords := mftSize / recordSize

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	records := make(map[uint64]mftRec, 1024)
	var done int64
	for hl := range ntfs.ParseMFTFile(ctx, mftReader, mftSize, f.ctx.ClusterSize, recordSize) {
		done++
		if progress != nil && done%50000 == 0 {
			progress(done, totalRecords)
		}
		if cancel != nil && cancel() {
			stop()
			return nil, ErrCancelled
		}
		if hl == nil || !hl.InUse {
			continue
		}
		entry := uint64(hl.EntryNumber)
		// One highlight per named stream can appear; keep the first (the
		// default data stream) per entry.
		if _, dup := records[entry]; dup {
			continue
		}
		name := pickBestName(hl.FileNames)
		if name == "" {
			continue
		}
		// Skip only TRUE NTFS metafiles: the reserved records (0-23: $MFT,
		// $Bitmap, $Secure, $Extend, ...). A user directory that merely STARTS
		// with '$' — $WINDOWS.~BT, $AV_ASW, $WinREAgent — is real data and
		// must show. ($Extend's children have a parent outside the record map
		// and drop out of path resolution on their own.)
		if entry < 24 && strings.HasPrefix(name, "$") {
			continue
		}
		if len(records) >= ntfsMaxRecords {
			return nil, fmt.Errorf("volume has more than %d MFT records — too large for a full tree listing", ntfsMaxRecords)
		}
		records[entry] = mftRec{
			parent: hl.ParentEntryNumber,
			name:   name,
			isDir:  hl.IsDir,
			size:   uint64(hl.FileSize),
			mtime:  hl.LastModified0x10.Unix(),
		}
	}
	if progress != nil {
		progress(totalRecords, totalRecords)
	}

	// Rebuild paths from parent references, memoized. Entries whose chain does
	// not reach the root (5), cycles, orphans of deleted trees, and anything
	// under a $-metafile are dropped — same shape the directory walk produces.
	const rootEntry = 5
	paths := make(map[uint64]string, len(records))
	paths[rootEntry] = ""
	var resolve func(e uint64, depth int) (string, bool)
	resolve = func(e uint64, depth int) (string, bool) {
		if p, ok := paths[e]; ok {
			return p, p != "\x00"
		}
		if depth > 512 { // deeper than any real filesystem: cycle guard
			return "", false
		}
		r, ok := records[e]
		if !ok {
			paths[e] = "\x00" // memoize the failure (parent is a metafile or gone)
			return "", false
		}
		parentPath, ok := resolve(r.parent, depth+1)
		if !ok {
			paths[e] = "\x00"
			return "", false
		}
		p := parentPath + "/" + r.name
		paths[e] = p
		return p, true
	}

	keys := make([]uint64, 0, len(records))
	for e := range records {
		keys = append(keys, e)
	}
	slices.Sort(keys) // deterministic: caps (if any) truncate predictably
	out := make([]Entry, 0, len(records))
	for _, e := range keys {
		if e == rootEntry {
			continue
		}
		r := records[e]
		p, ok := resolve(e, 0)
		if !ok || p == "" {
			continue
		}
		out = append(out, Entry{Path: p, IsDir: r.isDir, Size: r.size, ModTime: r.mtime})
		if len(out) >= maxEntries {
			sortEntries(out)
			return out, ErrTooManyEntries
		}
	}
	sortEntries(out)
	return out, nil
}

// pickBestName chooses the display name among an entry's hard-linked names:
// the longest one wins, which drops DOS 8.3 aliases (FILENA~1.TXT) in favour
// of the Win32 long name.
func pickBestName(names []string) string {
	best := ""
	for _, n := range names {
		if len(n) > len(best) {
			best = n
		}
	}
	return best
}

// StoragePlan implements Planner: the $MFT's actual size and its on-disk run
// list, straight from record 0's unnamed $DATA attribute. A decade-old volume
// can hold the MFT in hundreds of fragments; sequential-in-file is then
// scattered-on-disk, and any image-linear read-ahead drags in unrelated data.
// This plan is what lets the reader fetch exactly the MFT's chunks instead.
func (f *ntfsFS) StoragePlan() (int64, []Extent, error) {
	e0, err := f.ctx.GetMFT(0)
	if err != nil {
		return 0, nil, fmt.Errorf("read $MFT record: %w", err)
	}
	const attrData = 128
	attr, err := e0.GetAttribute(f.ctx, attrData, -1, "")
	if err != nil {
		return 0, nil, fmt.Errorf("$MFT $DATA attribute: %w", err)
	}
	size := int64(attr.Actual_size())
	if size <= 0 {
		return 0, nil, fmt.Errorf("implausible $MFT size %d", size)
	}
	cs := f.ctx.ClusterSize
	if cs <= 0 {
		return 0, nil, fmt.Errorf("unknown cluster size")
	}
	var extents []Extent
	for _, r := range attr.RunList() {
		if r == nil || r.Length <= 0 {
			continue
		}
		if r.Offset <= 0 {
			continue // sparse/unallocated run — nothing on disk to fetch
		}
		extents = append(extents, Extent{Offset: r.Offset * cs, Length: r.Length * cs})
	}
	if len(extents) == 0 {
		return 0, nil, fmt.Errorf("$MFT has no allocated runs")
	}
	return size, extents, nil
}

// ---- alternate data streams --------------------------------------------------

const ntfsAttrData = 128 // $DATA attribute type

// ListStreams implements StreamLister: the named $DATA attributes of a file.
// (The unnamed attribute is the file's main content and is not listed.)
func (f *ntfsFS) ListStreams(p string) ([]StreamInfo, error) {
	e, err := f.open(p)
	if err != nil {
		return nil, err
	}
	var out []StreamInfo
	seen := map[string]bool{}
	for _, attr := range e.EnumerateAttributes(f.ctx) {
		if attr.Type().Value != ntfsAttrData {
			continue
		}
		name := attr.Name()
		if name == "" || seen[name] {
			continue // unnamed main stream, or a continuation record of one we have
		}
		seen[name] = true
		out = append(out, StreamInfo{Name: name, Size: ntfsAttrSize(attr)})
	}
	return out, nil
}

// ExtractStream implements StreamLister for one named stream.
func (f *ntfsFS) ExtractStream(p, stream string, w io.Writer) (int64, error) {
	if stream == "" {
		return f.ExtractFile(p, w)
	}
	e, err := f.open(p)
	if err != nil {
		return 0, err
	}
	var size int64 = -1
	for _, attr := range e.EnumerateAttributes(f.ctx) {
		if attr.Type().Value == ntfsAttrData && attr.Name() == stream {
			size = int64(ntfsAttrSize(attr))
			break
		}
	}
	if size < 0 {
		return 0, fmt.Errorf("%s has no stream %q", p, stream)
	}
	reader, err := ntfs.OpenStream(f.ctx, e, ntfsAttrData, 65535, stream)
	if err != nil {
		return 0, fmt.Errorf("open stream %s:%s: %w", p, stream, err)
	}
	n, err := io.Copy(w, io.NewSectionReader(reader, 0, size))
	if err != nil {
		return n, fmt.Errorf("read stream %s:%s: %w", p, stream, err)
	}
	if n != size {
		return n, fmt.Errorf("stream %s:%s: read %d of %d bytes", p, stream, n, size)
	}
	return n, nil
}

// ntfsReservedName reports whether name is one of the reserved NTFS metafile
// names that occupy MFT records 0-23. Only these are hidden from listings;
// any other $-prefixed name is user data.
func ntfsReservedName(name string) bool {
	switch name {
	case "$MFT", "$MFTMirr", "$LogFile", "$Volume", "$AttrDef",
		"$Bitmap", "$Boot", "$BadClus", "$Secure", "$UpCase", "$Extend":
		return true
	}
	return false
}

// ntfsAttrSize returns an attribute's data size. Resident attributes (small
// streams live inline in the MFT record) carry it in Content_size;
// Actual_size is only meaningful for non-resident attributes — reading it on
// a resident one returns bytes of the content itself as a bogus number.
func ntfsAttrSize(attr *ntfs.NTFS_ATTRIBUTE) uint64 {
	if attr.IsResident() {
		return uint64(attr.Content_size())
	}
	return attr.Actual_size()
}
