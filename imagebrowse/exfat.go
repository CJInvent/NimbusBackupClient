package imagebrowse

// exfat.go — read-only exFAT support.
//
// Hand-rolled against Microsoft's published exFAT specification (the format
// was documented in 2019). exFAT is what Windows uses for large removable
// media and some data volumes, so a machine image can legitimately contain
// one. Read-only, no dependencies.
//
// Layout recap: a boot sector gives the geometry; files live in "directory
// entry sets" of 32-byte records — a File entry (0x85) followed by a Stream
// Extension (0xC0, holding size + first cluster) and one or more File Name
// entries (0xC1, 15 UTF-16 units each). Data is either a FAT chain or, when
// the NoFatChain flag is set, a contiguous run.

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	exEntryFile       = 0x85
	exEntryStreamExt  = 0xC0
	exEntryFileName   = 0xC1
	exEntryBitmap     = 0x81
	exEntryVolumeLbl  = 0x83
	exAttrDirectory   = 0x10
	exFlagNoFatChain  = 0x02
	exEntryInUseBit   = 0x80
	exEntryEndOfDir   = 0x00
	exMaxBitmapBytes  = 256 << 20 // defensive cap on the allocation bitmap read
	exMaxDirBytes     = 64 << 20  // defensive cap on a single directory's size
	exMaxChainCluster = 1 << 28
)

type exfatFS struct {
	r                 io.ReaderAt
	bytesPerSector    uint32
	sectorsPerCluster uint32
	clusterSize       uint32
	fatOffset         uint32 // sectors
	clusterHeapOffset uint32 // sectors
	clusterCount      uint32
	rootCluster       uint32
	label             string

	bitmapCluster uint32
	bitmapLength  uint64
	bitmapFlags   uint8
}

// exfatLabelFromBoot is a cheap best-effort label read used during partition
// identification (before the volume is opened). The real label lives in a
// root-directory entry, so this returns "" — the label is filled in properly
// by openExFAT. Kept as a named hook so identifyBootSector reads clearly.
func exfatLabelFromBoot(_ []byte) string { return "" }

func openExFAT(r io.ReaderAt) (Filesystem, error) {
	b := make([]byte, 512)
	if _, err := r.ReadAt(b, 0); err != nil && err != io.EOF {
		return nil, fmt.Errorf("read exFAT boot sector: %w", err)
	}
	if string(b[3:11]) != "EXFAT   " {
		return nil, fmt.Errorf("not an exFAT volume")
	}
	bpsShift := b[0x6C]
	spcShift := b[0x6D]
	// Per the exFAT spec, BytesPerSectorShift is 9..12 and
	// SectorsPerClusterShift may not exceed 25 - BytesPerSectorShift, i.e. a
	// cluster is at most 32 MB. Bounding spcShift ALONE at 25 was not the same
	// check: bpsShift=9 with spcShift=22 yields a 2 GB cluster that passed
	// every subsequent test, and the reader allocates a whole cluster per read
	// (readRun, readDirBytes, ExtractFile). A 64 KB crafted boot sector was
	// enough to force multi-GB allocations and take the process down —
	// browsing an image must survive a corrupted one.
	if bpsShift < 9 || bpsShift > 12 || spcShift > 25-bpsShift {
		return nil, fmt.Errorf("implausible exFAT geometry (shifts %d/%d)", bpsShift, spcShift)
	}
	f := &exfatFS{
		r:                 r,
		bytesPerSector:    1 << bpsShift,
		sectorsPerCluster: 1 << spcShift,
		fatOffset:         binary.LittleEndian.Uint32(b[0x50:0x54]),
		clusterHeapOffset: binary.LittleEndian.Uint32(b[0x58:0x5C]),
		clusterCount:      binary.LittleEndian.Uint32(b[0x5C:0x60]),
		rootCluster:       binary.LittleEndian.Uint32(b[0x60:0x64]),
	}
	f.clusterSize = f.bytesPerSector * f.sectorsPerCluster
	if f.clusterSize == 0 || f.rootCluster < 2 || f.clusterCount == 0 {
		return nil, fmt.Errorf("invalid exFAT geometry")
	}
	// The allocation bitmap and the volume label are themselves root-directory
	// entries, so scan the root once to pick them up.
	f.scanRootMeta()
	return f, nil
}

func (f *exfatFS) Kind() string  { return FSExFAT }
func (f *exfatFS) Label() string { return f.label }

func (f *exfatFS) clusterOffset(c uint32) int64 {
	return int64(f.clusterHeapOffset)*int64(f.bytesPerSector) +
		int64(c-2)*int64(f.clusterSize)
}

// fatEntry reads a 32-bit FAT entry.
func (f *exfatFS) fatEntry(c uint32) uint32 {
	off := int64(f.fatOffset)*int64(f.bytesPerSector) + int64(c)*4
	var buf [4]byte
	if _, err := f.r.ReadAt(buf[:], off); err != nil && err != io.EOF {
		return 0xFFFFFFFF
	}
	return binary.LittleEndian.Uint32(buf[:])
}

// chain returns the clusters backing a run. When noFatChain is set the run is
// contiguous and the FAT must NOT be consulted (it may hold stale data) —
// getting this wrong is a classic exFAT bug, so it is explicit here.
func (f *exfatFS) chain(start uint32, length uint64, noFatChain bool) []uint32 {
	if start < 2 || length == 0 {
		return nil
	}
	n := (length + uint64(f.clusterSize) - 1) / uint64(f.clusterSize)
	if n > exMaxChainCluster {
		return nil
	}
	out := make([]uint32, 0, n)
	if noFatChain {
		for i := uint64(0); i < n; i++ {
			c := start + uint32(i)
			if c > f.clusterCount+1 {
				break
			}
			out = append(out, c)
		}
		return out
	}
	seen := make(map[uint32]bool)
	for c := start; c >= 2 && c <= f.clusterCount+1 && uint64(len(out)) < n; {
		if seen[c] {
			break // cyclic FAT — refuse to spin
		}
		seen[c] = true
		out = append(out, c)
		next := f.fatEntry(c)
		if next < 2 || next >= 0xFFFFFFF7 {
			break
		}
		c = next
	}
	return out
}

// readRun reads the bytes of a cluster run into a buffer (bounded by cap).
func (f *exfatFS) readRun(start uint32, length uint64, noFatChain bool, cap uint64) ([]byte, error) {
	if length > cap {
		return nil, fmt.Errorf("run of %d bytes exceeds cap %d", length, cap)
	}
	clusters := f.chain(start, length, noFatChain)
	if len(clusters) == 0 {
		return nil, fmt.Errorf("empty cluster chain")
	}
	out := make([]byte, 0, length)
	tmp := make([]byte, f.clusterSize)
	remaining := length
	for _, c := range clusters {
		if remaining == 0 {
			break
		}
		n := uint64(f.clusterSize)
		if n > remaining {
			n = remaining
		}
		if _, err := f.r.ReadAt(tmp[:n], f.clusterOffset(c)); err != nil && err != io.EOF {
			return nil, fmt.Errorf("read cluster %d: %w", c, err)
		}
		out = append(out, tmp[:n]...)
		remaining -= n
	}
	return out, nil
}

// exDirEnt is a decoded directory entry set.
type exDirEnt struct {
	name       string
	isDir      bool
	size       uint64
	cluster    uint32
	noFatChain bool
	mtime      int64
}

// readDir decodes every entry set in the directory starting at cluster.
func (f *exfatFS) readDir(cluster uint32) ([]exDirEnt, error) {
	// A directory's length is not recorded, so walk its chain to the end.
	raw, err := f.readDirBytes(cluster)
	if err != nil {
		return nil, err
	}
	var out []exDirEnt
	for i := 0; i+32 <= len(raw); {
		e := raw[i : i+32]
		typ := e[0]
		if typ == exEntryEndOfDir {
			break
		}
		if typ&exEntryInUseBit == 0 { // deleted entry set
			i += 32
			continue
		}
		if typ != exEntryFile {
			i += 32
			continue
		}
		secondary := int(e[1])
		attrs := binary.LittleEndian.Uint16(e[4:6])
		mtime := exfatTime(binary.LittleEndian.Uint32(e[12:16]))

		// The Stream Extension must immediately follow the File entry.
		if i+64 > len(raw) || secondary < 1 {
			break
		}
		se := raw[i+32 : i+64]
		if se[0] != exEntryStreamExt {
			i += 32
			continue
		}
		flags := se[1]
		nameLen := int(se[3])
		firstCluster := binary.LittleEndian.Uint32(se[20:24])
		dataLength := binary.LittleEndian.Uint64(se[24:32])

		// File Name entries follow, 15 UTF-16 units each.
		var u []uint16
		for k := 2; k <= secondary && i+32*(k+1) <= len(raw); k++ {
			ne := raw[i+32*k : i+32*(k+1)]
			if ne[0] != exEntryFileName {
				break
			}
			for j := 2; j+1 < 32; j += 2 {
				u = append(u, binary.LittleEndian.Uint16(ne[j:j+2]))
			}
		}
		if nameLen > 0 && nameLen <= len(u) {
			u = u[:nameLen]
		}
		name := strings.TrimRight(string(utf16.Decode(u)), "\x00")
		if name != "" && name != "." && name != ".." {
			out = append(out, exDirEnt{
				name:       name,
				isDir:      attrs&exAttrDirectory != 0,
				size:       dataLength,
				cluster:    firstCluster,
				noFatChain: flags&exFlagNoFatChain != 0,
				mtime:      mtime,
			})
		}
		i += 32 * (secondary + 1)
	}
	return out, nil
}

// readDirBytes walks a directory's cluster chain to its end (directories carry
// no explicit length; the terminator is an end-of-directory entry).
func (f *exfatFS) readDirBytes(cluster uint32) ([]byte, error) {
	if cluster < 2 {
		return nil, fmt.Errorf("invalid directory cluster %d", cluster)
	}
	var out []byte
	tmp := make([]byte, f.clusterSize)
	seen := make(map[uint32]bool)
	for c := cluster; c >= 2 && c <= f.clusterCount+1; {
		if seen[c] || len(out) > exMaxDirBytes {
			break
		}
		seen[c] = true
		if _, err := f.r.ReadAt(tmp, f.clusterOffset(c)); err != nil && err != io.EOF {
			return nil, fmt.Errorf("read directory cluster %d: %w", c, err)
		}
		out = append(out, tmp...)
		next := f.fatEntry(c)
		if next < 2 || next >= 0xFFFFFFF7 {
			break
		}
		c = next
	}
	return out, nil
}

// scanRootMeta picks up the allocation bitmap and volume label, which are
// ordinary (non-file) entries in the root directory.
func (f *exfatFS) scanRootMeta() {
	raw, err := f.readDirBytes(f.rootCluster)
	if err != nil {
		return
	}
	for i := 0; i+32 <= len(raw); i += 32 {
		e := raw[i : i+32]
		switch e[0] {
		case exEntryEndOfDir:
			return
		case exEntryBitmap:
			f.bitmapFlags = e[1]
			f.bitmapCluster = binary.LittleEndian.Uint32(e[20:24])
			f.bitmapLength = binary.LittleEndian.Uint64(e[24:32])
		case exEntryVolumeLbl:
			n := int(e[1])
			if n > 11 {
				n = 11
			}
			u := make([]uint16, 0, n)
			for j := 0; j < n; j++ {
				u = append(u, binary.LittleEndian.Uint16(e[2+j*2:4+j*2]))
			}
			f.label = string(utf16.Decode(u))
		}
	}
}

// exfatTime converts an exFAT timestamp to unix seconds.
func exfatTime(v uint32) int64 {
	if v == 0 {
		return 0
	}
	sec := int(v&0x1F) * 2
	minute := int((v >> 5) & 0x3F)
	hour := int((v >> 11) & 0x1F)
	day := int((v >> 16) & 0x1F)
	month := time.Month((v >> 21) & 0x0F)
	year := int((v>>25)&0x7F) + 1980
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return 0
	}
	return time.Date(year, month, day, hour, minute, sec, 0, time.UTC).Unix()
}

func (f *exfatFS) resolve(p string) (exDirEnt, error) {
	cur := exDirEnt{name: "/", isDir: true, cluster: f.rootCluster}
	for _, want := range splitPath(p) {
		if !cur.isDir {
			return exDirEnt{}, fmt.Errorf("%s is not a directory", cur.name)
		}
		kids, err := f.readDir(cur.cluster)
		if err != nil {
			return exDirEnt{}, err
		}
		found := false
		for _, k := range kids {
			if strings.EqualFold(k.name, want) { // exFAT is case-insensitive
				cur = k
				found = true
				break
			}
		}
		if !found {
			return exDirEnt{}, fmt.Errorf("path not found: %s", p)
		}
	}
	return cur, nil
}

func (f *exfatFS) List(dir string) ([]Entry, error) {
	d, err := f.resolve(dir)
	if err != nil {
		return nil, err
	}
	if !d.isDir {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	kids, err := f.readDir(d.cluster)
	if err != nil {
		return nil, err
	}
	base := CleanPath(dir)
	out := make([]Entry, 0, len(kids))
	for _, k := range kids {
		out = append(out, Entry{
			Path:    joinPath(base, k.name),
			IsDir:   k.isDir,
			Size:    k.size,
			ModTime: k.mtime,
		})
	}
	sortEntries(out)
	return out, nil
}

func (f *exfatFS) Stat(p string) (Entry, error) {
	if CleanPath(p) == "/" {
		return Entry{Path: "/", IsDir: true}, nil
	}
	e, err := f.resolve(p)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Path: CleanPath(p), IsDir: e.isDir, Size: e.size, ModTime: e.mtime}, nil
}

func (f *exfatFS) ExtractFile(p string, w io.Writer) (int64, error) {
	e, err := f.resolve(p)
	if err != nil {
		return 0, err
	}
	if e.isDir {
		return 0, fmt.Errorf("%s is a directory", p)
	}
	if e.size == 0 {
		return 0, nil
	}
	clusters := f.chain(e.cluster, e.size, e.noFatChain)
	if len(clusters) == 0 {
		return 0, fmt.Errorf("%s: no data clusters", p)
	}
	var written int64
	remaining := e.size
	buf := make([]byte, f.clusterSize)
	for _, c := range clusters {
		if remaining == 0 {
			break
		}
		n := uint64(f.clusterSize)
		if n > remaining {
			n = remaining
		}
		if _, err := f.r.ReadAt(buf[:n], f.clusterOffset(c)); err != nil && err != io.EOF {
			return written, fmt.Errorf("read cluster %d: %w", c, err)
		}
		m, werr := w.Write(buf[:n])
		written += int64(m)
		if werr != nil {
			return written, werr
		}
		remaining -= n
	}
	if remaining > 0 {
		return written, fmt.Errorf("%s: cluster chain ended %d bytes early (corrupt volume?)", p, remaining)
	}
	return written, nil
}

// UsedBytes popcounts the allocation bitmap — exact, and cheap because the
// bitmap is one bit per cluster.
func (f *exfatFS) UsedBytes() (int64, bool) {
	if f.bitmapCluster < 2 || f.bitmapLength == 0 {
		return 0, false
	}
	raw, err := f.readRun(f.bitmapCluster, f.bitmapLength,
		f.bitmapFlags&exFlagNoFatChain != 0, exMaxBitmapBytes)
	if err != nil {
		return 0, false
	}
	used := 0
	for _, b := range raw {
		used += bits.OnesCount8(b)
	}
	// The bitmap covers exactly clusterCount clusters; ignore padding bits.
	if uint32(used) > f.clusterCount {
		used = int(f.clusterCount)
	}
	return int64(used) * int64(f.clusterSize), true
}
