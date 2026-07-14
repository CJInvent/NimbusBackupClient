package imagebrowse

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// The three fixtures in testdata/ are REAL filesystem images, produced by
// mkfs.ntfs / mkfs.vfat / mkfs.exfat and populated through the kernel drivers,
// then gzipped (they are mostly zeros, so they compress to ~50-180 KB). Each
// holds the identical tree:
//
//	/readme.txt                            27 bytes
//	/Docs/a-long-file-name-with-lfn.bin   256 KiB  (exercises VFAT long names /
//	                                                exFAT name entry sets)
//	/Docs/Nested/deep.txt                  17 bytes
//
// Running all three against one shared expectation is what proves the
// Filesystem abstraction actually abstracts.
const (
	payloadSHA256 = "bbaf556bdf474dc24c0dfc9ea0a58fb28a48b60098d88b0d15404b3f81eecd5f"
	payloadSize   = 256 * 1024
	payloadPath   = "/Docs/a-long-file-name-with-lfn.bin"
	readmePath    = "/readme.txt"
	deepPath      = "/Docs/Nested/deep.txt"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip fixture: %v", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// wrapMBR embeds a bare volume at LBA 2048 of a synthetic MBR disk, so tests
// drive the REAL production path: whole disk -> partition table -> filesystem
// at an offset, not a bare volume at offset 0.
func wrapMBR(vol []byte, mbrType byte) []byte {
	const startLBA = 2048
	d := make([]byte, startLBA*512+len(vol))
	d[510], d[511] = 0x55, 0xAA
	e := d[446:462]
	e[4] = mbrType
	binary.LittleEndian.PutUint32(e[8:12], startLBA)
	binary.LittleEndian.PutUint32(e[12:16], uint32(len(vol)/512))
	copy(d[startLBA*512:], vol)
	return d
}

func TestFilesystems(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		mbrType byte
		wantFS  string
	}{
		{"NTFS", "ntfs.img.gz", 0x07, FSNTFS},
		{"FAT32", "fat32.img.gz", 0x0B, FSFAT32},
		{"exFAT", "exfat.img.gz", 0x07, FSExFAT},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vol := loadFixture(t, tc.fixture)
			disk := wrapMBR(vol, tc.mbrType)
			r := bytes.NewReader(disk)

			// ---- partition table + filesystem identification ----
			parts, err := ListPartitions(r, int64(len(disk)))
			if err != nil {
				t.Fatalf("ListPartitions: %v", err)
			}
			if len(parts) != 1 {
				t.Fatalf("want 1 partition, got %d", len(parts))
			}
			p := parts[0]
			if p.Filesystem != tc.wantFS {
				t.Fatalf("filesystem = %q, want %q", p.Filesystem, tc.wantFS)
			}
			if !Browsable(p.Filesystem) {
				t.Fatalf("%s should be browsable", p.Filesystem)
			}
			if p.StartOffset != 2048*512 || p.Length != int64(len(vol)) {
				t.Fatalf("geometry: start=%d len=%d (want %d/%d)",
					p.StartOffset, p.Length, 2048*512, len(vol))
			}

			// ---- open ----
			fs, err := OpenFilesystem(r, p)
			if err != nil {
				t.Fatalf("OpenFilesystem: %v", err)
			}

			// ---- list root ----
			roots, err := fs.List("/")
			if err != nil {
				t.Fatalf("List(/): %v", err)
			}
			var sawDocs, sawReadme bool
			for _, e := range roots {
				switch e.Path {
				case "/Docs":
					sawDocs = true
					if !e.IsDir {
						t.Error("/Docs should be a directory")
					}
				case readmePath:
					sawReadme = true
					if e.IsDir || e.Size != 27 {
						t.Errorf("/readme.txt: isDir=%v size=%d (want file, 27)", e.IsDir, e.Size)
					}
				}
			}
			if !sawDocs || !sawReadme {
				t.Fatalf("root listing incomplete: %+v", roots)
			}
			if len(roots) >= 2 && !roots[0].IsDir {
				t.Error("directories should sort before files")
			}

			// ---- walk the whole tree ----
			all, err := Walk(fs, "/", 0, nil)
			if err != nil {
				t.Fatalf("Walk: %v", err)
			}
			want := map[string]bool{
				"/Docs": true, readmePath: true, payloadPath: true,
				"/Docs/Nested": true, deepPath: true,
			}
			got := map[string]bool{}
			for _, e := range all {
				got[e.Path] = true
			}
			for w := range want {
				if !got[w] {
					t.Errorf("walk missing %s (got %v)", w, got)
				}
			}

			// ---- stat ----
			st, err := fs.Stat(payloadPath)
			if err != nil {
				t.Fatalf("Stat(%s): %v", payloadPath, err)
			}
			if st.IsDir || st.Size != payloadSize {
				t.Fatalf("stat payload: isDir=%v size=%d (want file, %d)", st.IsDir, st.Size, payloadSize)
			}

			// ---- extract: byte-exact. the thing that actually matters ----
			var buf bytes.Buffer
			n, err := fs.ExtractFile(payloadPath, &buf)
			if err != nil {
				t.Fatalf("ExtractFile: %v", err)
			}
			if n != payloadSize || buf.Len() != payloadSize {
				t.Fatalf("extract size %d/%d, want %d", n, buf.Len(), payloadSize)
			}
			sum := sha256.Sum256(buf.Bytes())
			if hex.EncodeToString(sum[:]) != payloadSHA256 {
				t.Fatalf("extracted payload CHECKSUM MISMATCH: got %s", hex.EncodeToString(sum[:]))
			}

			// small nested file: different code path (tiny, deep)
			var deep bytes.Buffer
			if _, err := fs.ExtractFile(deepPath, &deep); err != nil {
				t.Fatalf("ExtractFile(deep): %v", err)
			}
			if deep.String() != "deep nested file\n" {
				t.Fatalf("deep.txt content = %q", deep.String())
			}

			// ---- used space ----
			used, ok := fs.UsedBytes()
			if !ok {
				t.Logf("%s: used space unavailable (acceptable)", tc.name)
			} else {
				if used < payloadSize {
					t.Errorf("used=%d should be >= payload size %d", used, payloadSize)
				}
				if used > p.Length {
					t.Errorf("used=%d exceeds partition size %d", used, p.Length)
				}
				t.Logf("%s: used=%d of %d bytes (%.1f%%)", tc.name, used, p.Length,
					100*float64(used)/float64(p.Length))
			}

			// ---- failures must be failures, not silent empties ----
			if _, err := fs.ExtractFile("/does/not/exist.txt", io.Discard); err == nil {
				t.Error("extracting a missing file should fail")
			}
			if _, err := fs.ExtractFile("/Docs", io.Discard); err == nil {
				t.Error("extracting a directory should fail")
			}
		})
	}
}

func TestWalkCapAndCancel(t *testing.T) {
	vol := loadFixture(t, "fat32.img.gz")
	disk := wrapMBR(vol, 0x0B)
	r := bytes.NewReader(disk)
	parts, err := ListPartitions(r, int64(len(disk)))
	if err != nil {
		t.Fatal(err)
	}
	fs, err := OpenFilesystem(r, parts[0])
	if err != nil {
		t.Fatal(err)
	}
	got, err := Walk(fs, "/", 1, nil)
	if err != ErrTooManyEntries || len(got) != 1 {
		t.Fatalf("cap not enforced: n=%d err=%v", len(got), err)
	}
	if _, err := Walk(fs, "/", 0, func() bool { return true }); err != ErrCancelled {
		t.Fatalf("cancel not propagated: %v", err)
	}
}

// TestUnsupportedFilesystems: volumes we cannot read must fail with a clear,
// actionable error, not a confusing parse failure. The user has to be told to
// restore the full image — not left staring at "invalid magic".
func TestUnsupportedFilesystems(t *testing.T) {
	mk := func(mut func(b []byte)) []byte {
		vol := make([]byte, 4<<20)
		vol[510], vol[511] = 0x55, 0xAA
		mut(vol)
		return wrapMBR(vol, 0x07)
	}

	t.Run("BitLocker", func(t *testing.T) {
		disk := mk(func(b []byte) { copy(b[3:11], "-FVE-FS-") })
		r := bytes.NewReader(disk)
		parts, err := ListPartitions(r, int64(len(disk)))
		if err != nil {
			t.Fatal(err)
		}
		if parts[0].Filesystem != FSBitLocker {
			t.Fatalf("want bitlocker, got %s", parts[0].Filesystem)
		}
		if Browsable(parts[0].Filesystem) {
			t.Fatal("bitlocker must not be browsable")
		}
		if _, err := OpenFilesystem(r, parts[0]); err == nil {
			t.Fatal("opening a BitLocker volume must fail")
		}
	})

	t.Run("ReFS", func(t *testing.T) {
		disk := mk(func(b []byte) {
			copy(b[3:11], []byte{'R', 'e', 'F', 'S', 0, 0, 0, 0})
			copy(b[0x10:0x14], "FSRS")
		})
		r := bytes.NewReader(disk)
		parts, err := ListPartitions(r, int64(len(disk)))
		if err != nil {
			t.Fatal(err)
		}
		if parts[0].Filesystem != FSReFS {
			t.Fatalf("want refs, got %s", parts[0].Filesystem)
		}
		if _, err := OpenFilesystem(r, parts[0]); err == nil {
			t.Fatal("opening a ReFS volume must fail (unsupported)")
		}
	})
}

func TestGPTParsing(t *testing.T) {
	d := make([]byte, 8192*512)
	d[510], d[511] = 0x55, 0xAA
	d[446+4] = 0xEE
	h := d[512 : 512+512]
	copy(h[0:8], "EFI PART")
	binary.LittleEndian.PutUint64(h[0x48:], 2)
	binary.LittleEndian.PutUint32(h[0x50:], 128)
	binary.LittleEndian.PutUint32(h[0x54:], 128)
	e := d[2*512 : 2*512+128]
	binary.LittleEndian.PutUint32(e[0:4], 0xEBD0A0A2)
	binary.LittleEndian.PutUint16(e[4:6], 0xB9E5)
	binary.LittleEndian.PutUint16(e[6:8], 0x4433)
	binary.BigEndian.PutUint16(e[8:10], 0x87C0)
	copy(e[10:16], []byte{0x68, 0xB6, 0xB7, 0x26, 0x99, 0xC7})
	copy(e[16:32], bytes.Repeat([]byte{1}, 16))
	binary.LittleEndian.PutUint64(e[32:40], 2048)
	binary.LittleEndian.PutUint64(e[40:48], 4095)
	copy(e[56:], []byte{'D', 0, 'a', 0, 't', 0, 'a', 0})

	parts, err := ListPartitions(bytes.NewReader(d), int64(len(d)))
	if err != nil {
		t.Fatalf("gpt: %v", err)
	}
	if len(parts) != 1 || parts[0].Name != "Data" || parts[0].Type != "Windows data" {
		t.Fatalf("gpt parse wrong: %+v", parts)
	}
	if parts[0].StartOffset != 2048*512 || parts[0].Length != 2048*512 {
		t.Fatalf("gpt geometry wrong: %+v", parts[0])
	}
}

func TestNoPartitionTable(t *testing.T) {
	d := make([]byte, 4096)
	if _, err := ListPartitions(bytes.NewReader(d), int64(len(d))); err == nil {
		t.Fatal("expected an error for a disk with no boot signature")
	}
}

// TestNTFSFastTreeMatchesWalk: the sequential-$MFT fast path must return the
// same tree as the recursive walk — same paths, same sizes, same dirness.
func TestNTFSFastTreeMatchesWalk(t *testing.T) {
	vol := loadFixture(t, "ntfs.img.gz")
	disk := wrapMBR(vol, 0x07)
	r := bytes.NewReader(disk)
	parts, err := ListPartitions(r, int64(len(disk)))
	if err != nil {
		t.Fatal(err)
	}
	fs, err := OpenFilesystem(r, parts[0])
	if err != nil {
		t.Fatal(err)
	}
	tl, ok := fs.(TreeLister)
	if !ok {
		t.Fatal("NTFS filesystem must implement TreeLister")
	}

	var progressed bool
	fast, err := tl.FullTree(0, nil, func(done, total int64) { progressed = true })
	if err != nil {
		t.Fatalf("FullTree: %v", err)
	}
	_ = progressed // small fixture may finish before the first progress tick

	walked, err := Walk(fs, "/", 0, nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	type meta struct {
		isDir bool
		size  uint64
	}
	fastM := map[string]meta{}
	for _, e := range fast {
		fastM[e.Path] = meta{e.IsDir, e.Size}
	}
	for _, w := range walked {
		f, ok := fastM[w.Path]
		if !ok {
			t.Errorf("fast tree missing %s", w.Path)
			continue
		}
		if f.isDir != w.IsDir || (!w.IsDir && f.size != w.Size) {
			t.Errorf("%s: fast{dir:%v size:%d} != walk{dir:%v size:%d}",
				w.Path, f.isDir, f.size, w.IsDir, w.Size)
		}
	}
	if len(fast) < len(walked) {
		t.Errorf("fast tree has %d entries, walk found %d", len(fast), len(walked))
	}

	// Cancel propagation on the fast path too.
	if _, err := tl.FullTree(0, func() bool { return true }, nil); err != ErrCancelled {
		t.Fatalf("fast-path cancel not propagated: %v", err)
	}

	// FullTree dispatcher picks the fast path for NTFS.
	viaDispatch, err := FullTree(fs, 0, nil, nil)
	if err != nil {
		t.Fatalf("FullTree dispatch: %v", err)
	}
	if len(viaDispatch) != len(fast) {
		t.Fatalf("dispatcher returned %d entries, direct fast path %d", len(viaDispatch), len(fast))
	}
}

// TestNTFSStoragePlan: the $MFT plan must exist, be sane, and its extents must
// cover at least the MFT's logical size.
func TestNTFSStoragePlan(t *testing.T) {
	vol := loadFixture(t, "ntfs.img.gz")
	disk := wrapMBR(vol, 0x07)
	r := bytes.NewReader(disk)
	parts, _ := ListPartitions(r, int64(len(disk)))
	fs, err := OpenFilesystem(r, parts[0])
	if err != nil {
		t.Fatal(err)
	}
	pl, ok := fs.(Planner)
	if !ok {
		t.Fatal("NTFS must implement Planner")
	}
	size, extents, err := pl.StoragePlan()
	if err != nil {
		t.Fatalf("StoragePlan: %v", err)
	}
	if size <= 0 || len(extents) == 0 {
		t.Fatalf("empty plan: size=%d extents=%d", size, len(extents))
	}
	var covered int64
	for _, e := range extents {
		if e.Offset <= 0 || e.Length <= 0 || e.Offset+e.Length > parts[0].Length {
			t.Fatalf("extent out of partition bounds: %+v", e)
		}
		covered += e.Length
	}
	if covered < size {
		t.Fatalf("extents cover %d bytes < MFT size %d", covered, size)
	}
	t.Logf("$MFT: %d bytes in %d extent(s), %d bytes allocated", size, len(extents), covered)
}
