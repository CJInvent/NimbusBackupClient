package imagebrowse

// fat.go — read-only FAT12/FAT16/FAT32 support.
//
// Hand-rolled against the Microsoft FAT specification rather than pulling a
// disk library: we need listing + extraction + used-space only, the format is
// small and frozen, and a backup agent benefits from having no extra
// supply-chain surface. Long filenames (VFAT LFN) are supported, since EFI
// System Partitions and Windows recovery media routinely use them.

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	fatAttrReadOnly  = 0x01
	fatAttrHidden    = 0x02
	fatAttrSystem    = 0x04
	fatAttrVolumeID  = 0x08
	fatAttrDirectory = 0x10
	fatAttrLFN       = 0x0F // ReadOnly|Hidden|System|VolumeID — the LFN marker
)

// fatGeometry is everything the BPB tells us about layout.
type fatGeometry struct {
	bytesPerSector    uint32
	sectorsPerCluster uint32
	clusterSize       uint32
	reservedSectors   uint32
	numFATs           uint32
	rootEntryCount    uint32 // 0 on FAT32
	fatSize           uint32 // sectors per FAT
	totalSectors      uint32
	rootDirSectors    uint32
	firstDataSector   uint32
	countOfClusters   uint32
	rootCluster       uint32 // FAT32 only
	bits              int    // 12, 16 or 32
}

func (g *fatGeometry) fsName() string {
	switch g.bits {
	case 12:
		return FSFAT12
	case 16:
		return FSFAT16
	default:
		return FSFAT32
	}
}

// labelFromBoot reads the 11-byte volume label from the extended BPB. (The
// authoritative label lives in a root-directory volume-id entry; this is the
// cheap version used during partition identification, before we open the FS.)
func (g *fatGeometry) labelFromBoot(b []byte) string {
	off := 0x2B // FAT12/16 extended boot record
	if g.bits == 32 {
		off = 0x47
	}
	if off+11 > len(b) {
		return ""
	}
	l := strings.TrimSpace(string(b[off : off+11]))
	if l == "NO NAME" {
		return ""
	}
	return l
}

// parseFATGeometry validates a boot sector as FAT and derives the layout.
// Returns an error for anything that isn't a plausible FAT BPB — this
// doubles as the FAT detector in identifyBootSector.
func parseFATGeometry(b []byte) (*fatGeometry, error) {
	if len(b) < 512 {
		return nil, fmt.Errorf("short boot sector")
	}
	g := &fatGeometry{
		bytesPerSector:    uint32(binary.LittleEndian.Uint16(b[0x0B:0x0D])),
		sectorsPerCluster: uint32(b[0x0D]),
		reservedSectors:   uint32(binary.LittleEndian.Uint16(b[0x0E:0x10])),
		numFATs:           uint32(b[0x10]),
		rootEntryCount:    uint32(binary.LittleEndian.Uint16(b[0x11:0x13])),
		fatSize:           uint32(binary.LittleEndian.Uint16(b[0x16:0x18])),
		totalSectors:      uint32(binary.LittleEndian.Uint16(b[0x13:0x15])),
	}
	// Sanity: these constraints reject exFAT (whose legacy BPB is all zeros)
	// and any non-FAT sector that happened to reach us.
	if !isPow2(g.bytesPerSector) || g.bytesPerSector < 512 || g.bytesPerSector > 4096 {
		return nil, fmt.Errorf("bad bytes/sector %d", g.bytesPerSector)
	}
	if !isPow2(g.sectorsPerCluster) || g.sectorsPerCluster == 0 || g.sectorsPerCluster > 128 {
		return nil, fmt.Errorf("bad sectors/cluster %d", g.sectorsPerCluster)
	}
	if g.numFATs == 0 || g.numFATs > 4 || g.reservedSectors == 0 {
		return nil, fmt.Errorf("bad FAT count/reserved sectors")
	}
	if g.totalSectors == 0 {
		g.totalSectors = binary.LittleEndian.Uint32(b[0x20:0x24])
	}
	if g.fatSize == 0 {
		g.fatSize = binary.LittleEndian.Uint32(b[0x24:0x28]) // FAT32 BPB_FATSz32
		g.rootCluster = binary.LittleEndian.Uint32(b[0x2C:0x30])
	}
	if g.fatSize == 0 || g.totalSectors == 0 {
		return nil, fmt.Errorf("zero FAT size or total sectors")
	}

	g.clusterSize = g.bytesPerSector * g.sectorsPerCluster
	// Root dir occupies fixed sectors on FAT12/16, zero on FAT32.
	g.rootDirSectors = ((g.rootEntryCount * 32) + (g.bytesPerSector - 1)) / g.bytesPerSector
	g.firstDataSector = g.reservedSectors + (g.numFATs * g.fatSize) + g.rootDirSectors
	if g.totalSectors < g.firstDataSector {
		return nil, fmt.Errorf("total sectors < data start")
	}
	dataSectors := g.totalSectors - g.firstDataSector
	g.countOfClusters = dataSectors / g.sectorsPerCluster

	// The cluster count IS the FAT type, per the Microsoft spec — not the OEM
	// string, not the partition type byte.
	switch {
	case g.countOfClusters < 4085:
		g.bits = 12
	case g.countOfClusters < 65525:
		g.bits = 16
	default:
		g.bits = 32
	}
	if g.bits == 32 && (g.rootCluster < 2 || g.rootCluster > g.countOfClusters+1) {
		return nil, fmt.Errorf("bad FAT32 root cluster %d", g.rootCluster)
	}
	return g, nil
}

// fatFS is a mounted (in the userspace sense) FAT volume.
type fatFS struct {
	r     io.ReaderAt
	g     *fatGeometry
	fat   []byte // the entire first FAT, read once
	label string
}

func openFAT(r io.ReaderAt) (Filesystem, error) {
	boot := make([]byte, 512)
	if _, err := r.ReadAt(boot, 0); err != nil && err != io.EOF {
		return nil, fmt.Errorf("read FAT boot sector: %w", err)
	}
	g, err := parseFATGeometry(boot)
	if err != nil {
		return nil, fmt.Errorf("not a FAT volume: %w", err)
	}
	// Read the whole first FAT once — every chain lookup then costs nothing
	// extra. Bounded by construction: even a 2 TB FAT32 with 32K clusters has
	// a ~256 MB FAT, and real FAT volumes (EFI/recovery) are far smaller. Cap
	// defensively so a corrupt BPB can't make us allocate wildly.
	fatBytes := int64(g.fatSize) * int64(g.bytesPerSector)
	const maxFAT = 256 << 20
	if fatBytes <= 0 || fatBytes > maxFAT {
		return nil, fmt.Errorf("implausible FAT size %d bytes", fatBytes)
	}
	fat := make([]byte, fatBytes)
	if _, err := r.ReadAt(fat, int64(g.reservedSectors)*int64(g.bytesPerSector)); err != nil && err != io.EOF {
		return nil, fmt.Errorf("read FAT table: %w", err)
	}

	// A cluster with no FAT entry does not exist, so the FAT's own size is the
	// real upper bound on the cluster count — the BPB's declared count is
	// merely a claim. A corrupt or crafted BPB can declare ~4.3e9 clusters
	// while carrying a 32 KB FAT (totalSectors=0xFFFFFFFF does it), and
	// trusting that number made UsedBytes() walk billions of entries and
	// never return: browsing one bad image hung the process. Clamp to what
	// the FAT can address. The FAT TYPE stays as parsed — clamping must not
	// reclassify a legitimate volume.
	entryBytes := uint64(4) // FAT32
	switch g.bits {
	case 12:
		entryBytes = 0 // handled below: 12 bits = 1.5 bytes per entry
	case 16:
		entryBytes = 2
	}
	var addressable uint64
	if entryBytes == 0 {
		addressable = uint64(fatBytes) * 2 / 3
	} else {
		addressable = uint64(fatBytes) / entryBytes
	}
	if addressable > 2 {
		addressable -= 2 // FAT entries 0 and 1 are reserved
	} else {
		addressable = 0
	}
	if uint64(g.countOfClusters) > addressable {
		g.countOfClusters = uint32(addressable)
	}
	f := &fatFS{r: r, g: g, fat: fat, label: g.labelFromBoot(boot)}
	// The root-directory volume-id entry, when present, beats the BPB copy.
	if lbl := f.rootVolumeLabel(); lbl != "" {
		f.label = lbl
	}
	return f, nil
}

func (f *fatFS) Kind() string  { return f.g.fsName() }
func (f *fatFS) Label() string { return f.label }

// fatEntry returns the FAT table value for a cluster, honouring the 12/16/32
// bit packing. Out-of-range clusters return an end-of-chain marker so callers
// terminate instead of walking off the table.
func (f *fatFS) fatEntry(cluster uint32) uint32 {
	switch f.g.bits {
	case 12:
		off := cluster + cluster/2 // 1.5 bytes per entry
		if int(off)+1 >= len(f.fat) {
			return 0x0FFFFFFF
		}
		v := uint32(binary.LittleEndian.Uint16(f.fat[off : off+2]))
		if cluster&1 == 1 {
			v >>= 4
		} else {
			v &= 0x0FFF
		}
		if v >= 0x0FF8 {
			return 0x0FFFFFFF
		}
		return v
	case 16:
		off := cluster * 2
		if int(off)+1 >= len(f.fat) {
			return 0x0FFFFFFF
		}
		v := uint32(binary.LittleEndian.Uint16(f.fat[off : off+2]))
		if v >= 0xFFF8 {
			return 0x0FFFFFFF
		}
		return v
	default:
		off := cluster * 4
		if int(off)+3 >= len(f.fat) {
			return 0x0FFFFFFF
		}
		v := binary.LittleEndian.Uint32(f.fat[off:off+4]) & 0x0FFFFFFF
		if v >= 0x0FFFFFF8 {
			return 0x0FFFFFFF
		}
		return v
	}
}

// chain walks a cluster chain from start. Guards against cycles (a corrupt
// FAT must not hang the GUI) and against runs longer than the volume.
func (f *fatFS) chain(start uint32) []uint32 {
	var out []uint32
	seen := make(map[uint32]bool)
	for c := start; c >= 2 && c <= f.g.countOfClusters+1; {
		if seen[c] {
			break // cyclic FAT — stop rather than spin
		}
		seen[c] = true
		out = append(out, c)
		if uint32(len(out)) > f.g.countOfClusters+2 {
			break
		}
		next := f.fatEntry(c)
		if next >= 0x0FFFFFF8 || next < 2 {
			break
		}
		c = next
	}
	return out
}

func (f *fatFS) clusterOffset(c uint32) int64 {
	sector := f.g.firstDataSector + (c-2)*f.g.sectorsPerCluster
	return int64(sector) * int64(f.g.bytesPerSector)
}

// readDirRaw returns the raw 32-byte-entry bytes of a directory. cluster==0
// means the FAT12/16 fixed root region.
func (f *fatFS) readDirRaw(cluster uint32) ([]byte, error) {
	if cluster == 0 && f.g.bits != 32 {
		size := int64(f.g.rootEntryCount) * 32
		buf := make([]byte, size)
		off := int64(f.g.reservedSectors+f.g.numFATs*f.g.fatSize) * int64(f.g.bytesPerSector)
		if _, err := f.r.ReadAt(buf, off); err != nil && err != io.EOF {
			return nil, fmt.Errorf("read FAT root dir: %w", err)
		}
		return buf, nil
	}
	if cluster == 0 {
		cluster = f.g.rootCluster
	}
	clusters := f.chain(cluster)
	if len(clusters) == 0 {
		return nil, fmt.Errorf("empty cluster chain for directory")
	}
	buf := make([]byte, 0, len(clusters)*int(f.g.clusterSize))
	tmp := make([]byte, f.g.clusterSize)
	for _, c := range clusters {
		if _, err := f.r.ReadAt(tmp, f.clusterOffset(c)); err != nil && err != io.EOF {
			return nil, fmt.Errorf("read directory cluster %d: %w", c, err)
		}
		buf = append(buf, tmp...)
	}
	return buf, nil
}

// fatDirEnt is one parsed directory record.
type fatDirEnt struct {
	name    string
	isDir   bool
	size    uint32
	cluster uint32
	mtime   int64
	volume  bool
}

// parseDir decodes 32-byte records, assembling VFAT long filenames.
func (f *fatFS) parseDir(raw []byte) []fatDirEnt {
	var out []fatDirEnt
	var lfn []string // collected in reverse sequence order
	for i := 0; i+32 <= len(raw); i += 32 {
		e := raw[i : i+32]
		switch e[0] {
		case 0x00:
			return out // no further entries in this directory
		case 0xE5:
			lfn = nil // deleted; drop any LFN parts we were accumulating
			continue
		}
		attr := e[11]
		if attr&fatAttrLFN == fatAttrLFN {
			lfn = append(lfn, lfnChars(e))
			continue
		}
		// Assemble the name: LFN parts arrive in reverse order.
		name := ""
		for j := len(lfn) - 1; j >= 0; j-- {
			name += lfn[j]
		}
		lfn = nil
		if name == "" {
			name = shortName(e)
		}
		name = strings.TrimRight(name, "\x00\uFFFF")
		if name == "" || name == "." || name == ".." {
			continue
		}
		out = append(out, fatDirEnt{
			name:    name,
			isDir:   attr&fatAttrDirectory != 0,
			volume:  attr&fatAttrVolumeID != 0,
			size:    binary.LittleEndian.Uint32(e[28:32]),
			cluster: uint32(binary.LittleEndian.Uint16(e[20:22]))<<16 | uint32(binary.LittleEndian.Uint16(e[26:28])),
			mtime:   fatTime(binary.LittleEndian.Uint16(e[22:24]), binary.LittleEndian.Uint16(e[24:26])),
		})
	}
	return out
}

// lfnChars pulls the 13 UTF-16 code units an LFN record carries, from its
// three disjoint field ranges.
func lfnChars(e []byte) string {
	u := make([]uint16, 0, 13)
	add := func(b []byte) {
		for i := 0; i+1 < len(b); i += 2 {
			c := binary.LittleEndian.Uint16(b[i : i+2])
			if c == 0x0000 || c == 0xFFFF {
				return
			}
			u = append(u, c)
		}
	}
	add(e[1:11])
	add(e[14:26])
	add(e[28:32])
	return string(utf16.Decode(u))
}

// shortName renders the legacy 8.3 name.
func shortName(e []byte) string {
	base := strings.TrimRight(string(e[0:8]), " ")
	ext := strings.TrimRight(string(e[8:11]), " ")
	if base == "" {
		return ""
	}
	// 0x05 is an escape for a leading 0xE5 byte in the real name.
	if e[0] == 0x05 {
		base = "\xE5" + base[1:]
	}
	// Case flags (offset 12): bit 3 lowercases the base, bit 4 the extension.
	if e[12]&0x08 != 0 {
		base = strings.ToLower(base)
	}
	if e[12]&0x10 != 0 {
		ext = strings.ToLower(ext)
	}
	if ext == "" {
		return base
	}
	return base + "." + ext
}

// fatTime converts a FAT date/time pair to unix seconds (local-time semantics
// on disk; treated as UTC, which is what every other FAT reader does).
func fatTime(t, d uint16) int64 {
	if d == 0 {
		return 0
	}
	year := int(d>>9) + 1980
	month := time.Month((d >> 5) & 0x0F)
	day := int(d & 0x1F)
	hour := int(t >> 11)
	minute := int((t >> 5) & 0x3F)
	sec := int(t&0x1F) * 2
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return 0
	}
	return time.Date(year, month, day, hour, minute, sec, 0, time.UTC).Unix()
}

func (f *fatFS) rootVolumeLabel() string {
	raw, err := f.readDirRaw(0)
	if err != nil {
		return ""
	}
	for _, e := range f.parseDir(raw) {
		if e.volume && !e.isDir {
			return strings.TrimSpace(e.name)
		}
	}
	return ""
}

// resolve walks a path to its directory entry. Returns cluster 0 + isDir for
// the root itself.
func (f *fatFS) resolve(p string) (fatDirEnt, error) {
	parts := splitPath(p)
	cur := fatDirEnt{name: "/", isDir: true, cluster: 0}
	for _, want := range parts {
		if !cur.isDir {
			return fatDirEnt{}, fmt.Errorf("%s is not a directory", cur.name)
		}
		raw, err := f.readDirRaw(cur.cluster)
		if err != nil {
			return fatDirEnt{}, err
		}
		found := false
		for _, e := range f.parseDir(raw) {
			if e.volume {
				continue
			}
			if strings.EqualFold(e.name, want) { // FAT is case-insensitive
				cur = e
				found = true
				break
			}
		}
		if !found {
			return fatDirEnt{}, fmt.Errorf("path not found: %s", p)
		}
	}
	return cur, nil
}

func (f *fatFS) List(dir string) ([]Entry, error) {
	d, err := f.resolve(dir)
	if err != nil {
		return nil, err
	}
	if !d.isDir {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	raw, err := f.readDirRaw(d.cluster)
	if err != nil {
		return nil, err
	}
	base := CleanPath(dir)
	var out []Entry
	for _, e := range f.parseDir(raw) {
		if e.volume {
			continue // the volume-label pseudo-entry is not a file
		}
		out = append(out, Entry{
			Path:    joinPath(base, e.name),
			IsDir:   e.isDir,
			Size:    uint64(e.size),
			ModTime: e.mtime,
		})
	}
	sortEntries(out)
	return out, nil
}

func (f *fatFS) Stat(p string) (Entry, error) {
	if CleanPath(p) == "/" {
		return Entry{Path: "/", IsDir: true}, nil
	}
	e, err := f.resolve(p)
	if err != nil {
		return Entry{}, err
	}
	return Entry{
		Path:    CleanPath(p),
		IsDir:   e.isDir,
		Size:    uint64(e.size),
		ModTime: e.mtime,
	}, nil
}

func (f *fatFS) ExtractFile(p string, w io.Writer) (int64, error) {
	e, err := f.resolve(p)
	if err != nil {
		return 0, err
	}
	if e.isDir {
		return 0, fmt.Errorf("%s is a directory", p)
	}
	remaining := int64(e.size)
	if remaining == 0 {
		return 0, nil
	}
	var written int64
	buf := make([]byte, f.g.clusterSize)
	for _, c := range f.chain(e.cluster) {
		if remaining <= 0 {
			break
		}
		n := int64(f.g.clusterSize)
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
		// The chain ended before the size in the directory entry was satisfied:
		// the volume is inconsistent. Report rather than return a short file
		// that silently looks complete.
		return written, fmt.Errorf("%s: cluster chain ended %d bytes early (corrupt volume?)", p, remaining)
	}
	return written, nil
}

// UsedBytes counts allocated clusters in the FAT (entry != 0 == in use).
func (f *fatFS) UsedBytes() (int64, bool) {
	var used int64
	for c := uint32(2); c <= f.g.countOfClusters+1; c++ {
		if f.fatEntry(c) != 0 {
			used += int64(f.g.clusterSize)
		}
	}
	return used, true
}

// isPow2 reports whether v is a power of two (BPB fields must be).
func isPow2(v uint32) bool { return v != 0 && v&(v-1) == 0 }
