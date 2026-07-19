package imagebrowse

// Parser robustness against hostile images.
//
// A backup image is untrusted input. It can be corrupted in transit or on the
// source disk, and it can be crafted — nothing in NTFS/FAT/exFAT on disk
// constrains the fields these parsers read. The requirement is that a bad
// image FAILS: never a panic (in the service that takes the check-in loop with
// it), never an unbounded loop, never an allocation sized by the image.
//
// The geometry cases below are deterministic regressions, not decoration —
// each one is a shape that reached real code.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"testing"
	"time"
)

// validFAT32 builds a boot sector parseFATGeometry accepts, so that mutations
// of it reach the parser instead of bouncing off the sanity checks. A fuzz
// that never gets past validation proves nothing.
func validFAT32(volBytes int) []byte {
	b := make([]byte, volBytes)
	binary.LittleEndian.PutUint16(b[0x0B:], 512) // bytes/sector
	b[0x0D] = 1                                  // sectors/cluster
	binary.LittleEndian.PutUint16(b[0x0E:], 32)  // reserved sectors
	b[0x10] = 2                                  // FAT count
	binary.LittleEndian.PutUint32(b[0x20:], uint32(volBytes/512))
	binary.LittleEndian.PutUint32(b[0x24:], 64) // FATSz32
	binary.LittleEndian.PutUint32(b[0x2C:], 2)  // root cluster
	b[510], b[511] = 0x55, 0xAA
	return b
}

func validExFAT(volBytes int) []byte {
	b := make([]byte, volBytes)
	copy(b[3:], []byte("EXFAT   "))
	b[0x6C] = 9 // bytes/sector shift  -> 512
	b[0x6D] = 1 // sectors/cluster shift
	binary.LittleEndian.PutUint32(b[0x50:], 32)
	binary.LittleEndian.PutUint32(b[0x58:], 64)
	binary.LittleEndian.PutUint32(b[0x5C:], 128)
	binary.LittleEndian.PutUint32(b[0x60:], 2)
	b[510], b[511] = 0x55, 0xAA
	return b
}

// driveFilesystem touches every entry point a browse or restore would.
func driveFilesystem(fs Filesystem) {
	if fs == nil {
		return
	}
	_, _ = fs.UsedBytes()
	entries, err := fs.List("/")
	if err == nil {
		for i, e := range entries {
			if i > 8 {
				break
			}
			_, _ = fs.Stat(e.Path)
			_, _ = fs.ExtractFile(e.Path, io.Discard)
			if e.IsDir {
				_, _ = fs.List(e.Path)
			}
		}
	}
	_, _ = fs.Stat("/does-not-exist")
	_, _ = fs.ExtractFile("/does-not-exist", io.Discard)
	_, _ = FullTree(fs, 500, func() bool { return false }, nil)
}

// openAndDrive runs one image under a watchdog: a parser that never returns is
// as bad as one that panics, and a cyclic cluster chain is a real image defect.
func openAndDrive(t *testing.T, label string, img []byte, kind string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("PANIC [%s]: %v", label, r)
			}
			close(done)
		}()
		p := Partition{Index: 1, StartOffset: 0, Length: int64(len(img)), Filesystem: kind}
		fs, err := OpenFilesystem(bytes.NewReader(img), p)
		if err != nil {
			return // refusing a bad image is the correct outcome
		}
		driveFilesystem(fs)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("HANG [%s]: parser did not return", label)
	}
}

func TestPartitionTableHostileInput(t *testing.T) {
	seeds := map[string][]byte{
		"empty":        {},
		"one byte":     {0x55},
		"short sector": bytes.Repeat([]byte{0}, 511),
		"zero sector":  bytes.Repeat([]byte{0}, 512),
		"all 0xFF":     bytes.Repeat([]byte{0xFF}, 8192),
	}
	for name, data := range seeds {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("PANIC on %q: %v", name, r)
				}
			}()
			_, _ = ListPartitions(bytes.NewReader(data), int64(len(data)))
		}()
	}

	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		n := 512 + rng.Intn(8192)
		b := make([]byte, n)
		rng.Read(b)
		b[510], b[511] = 0x55, 0xAA // get past the signature check
		if i%3 == 0 && n >= 520 {
			copy(b[512:], []byte("EFI PART")) // and into the GPT path
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("PANIC at iteration %d (size %d): %v", i, n, r)
				}
			}()
			_, _ = ListPartitions(bytes.NewReader(b), int64(n))
		}()
	}
}

func TestFATParserHostileInput(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	const vol = 1 << 20
	for i := 0; i < 80; i++ {
		img := validFAT32(vol)
		for m := 0; m < 1+rng.Intn(6); m++ {
			img[rng.Intn(0x40)] = byte(rng.Intn(256)) // corrupt BPB geometry
		}
		for j := 0x200; j < len(img); j += rng.Intn(64) + 1 {
			img[j] = byte(rng.Intn(256)) // noise in the data area
		}
		openAndDrive(t, "fat32-mutated", img, FSFAT32)
	}
}

func TestExFATParserHostileInput(t *testing.T) {
	rng := rand.New(rand.NewSource(43))
	const vol = 1 << 20
	for i := 0; i < 200; i++ {
		img := validExFAT(vol)
		for m := 0; m < 1+rng.Intn(6); m++ {
			img[0x40+rng.Intn(0x30)] = byte(rng.Intn(256))
		}
		for j := 0x200; j < len(img); j += rng.Intn(64) + 1 {
			img[j] = byte(rng.Intn(256))
		}
		openAndDrive(t, "exfat-mutated", img, FSExFAT)
	}
}

// Deterministic adversarial geometries: integer overflow in the derived
// values, and volumes that claim far more space than the image holds.
func TestParserAdversarialGeometry(t *testing.T) {
	const vol = 1 << 20

	overflow := validFAT32(vol)
	overflow[0x10] = 4 // numFATs * fatSize == 2^32, wraps to 0
	binary.LittleEndian.PutUint32(overflow[0x24:], 0x40000000)
	openAndDrive(t, "fat32 numFATs*fatSize overflow", overflow, FSFAT32)

	hugeFAT := validFAT32(vol)
	binary.LittleEndian.PutUint32(hugeFAT[0x24:], 0xFFFFFFF0)
	openAndDrive(t, "fat32 huge fatSize", hugeFAT, FSFAT32)

	hugeTotal := validFAT32(vol)
	binary.LittleEndian.PutUint32(hugeTotal[0x20:], 0xFFFFFFFF)
	openAndDrive(t, "fat32 huge totalSectors", hugeTotal, FSFAT32)

	bigCluster := validFAT32(vol)
	bigCluster[0x0D] = 128
	binary.LittleEndian.PutUint16(bigCluster[0x0B:], 4096)
	openAndDrive(t, "fat32 512KB clusters", bigCluster, FSFAT32)

	exHuge := validExFAT(vol)
	binary.LittleEndian.PutUint32(exHuge[0x5C:], 0xFFFFFFFF)
	binary.LittleEndian.PutUint32(exHuge[0x58:], 0xFFFFFFF0)
	openAndDrive(t, "exfat huge cluster count/heap", exHuge, FSExFAT)

	exRootFar := validExFAT(vol)
	binary.LittleEndian.PutUint32(exRootFar[0x60:], 0xFFFFFFFE)
	openAndDrive(t, "exfat root cluster past end", exRootFar, FSExFAT)

	openAndDrive(t, "fat32 truncated volume", validFAT32(4096), FSFAT32)
	openAndDrive(t, "exfat truncated volume", validExFAT(4096), FSExFAT)
}

// Regression: the exFAT spec bounds a cluster to 32 MB
// (SectorsPerClusterShift <= 25 - BytesPerSectorShift). Bounding
// SectorsPerClusterShift alone at 25 is NOT the same check — bpsShift=9 with
// spcShift=22 produced a 2 GB clusterSize that passed every later test, and
// the reader allocates a whole cluster per read. A 64 KB crafted boot sector
// was enough to force multi-GB allocations.
func TestExFATClusterSizeIsSpecBounded(t *testing.T) {
	const specMaxCluster = 1 << 25 // 32 MB

	for _, c := range []struct{ bps, spc byte }{
		{9, 22},  // 2 GB
		{10, 21}, // 2 GB
		{11, 20}, // 2 GB
		{9, 20},  // 512 MB
		{12, 25}, // 137 GB — survives only because it wraps uint32 to zero
	} {
		b := make([]byte, 1<<16)
		copy(b[3:], []byte("EXFAT   "))
		b[0x6C], b[0x6D] = c.bps, c.spc
		binary.LittleEndian.PutUint32(b[0x50:], 32)
		binary.LittleEndian.PutUint32(b[0x58:], 64)
		binary.LittleEndian.PutUint32(b[0x5C:], 128)
		binary.LittleEndian.PutUint32(b[0x60:], 2)

		fs, err := openExFAT(bytes.NewReader(b))
		if err != nil {
			continue // refused — the safe outcome
		}
		ex, ok := fs.(*exfatFS)
		if !ok {
			t.Fatalf("unexpected filesystem type %T", fs)
		}
		if ex.clusterSize > specMaxCluster {
			t.Errorf("bpsShift=%d spcShift=%d accepted with clusterSize=%d (%d MB), "+
				"above the 32 MB the spec allows — the reader allocates a whole "+
				"cluster per read", c.bps, c.spc, ex.clusterSize, ex.clusterSize>>20)
		}
	}
}

// validNTFS builds a boot sector plausible enough that go-ntfs proceeds to
// parse the volume, so mutations of it exercise the reader rather than
// bouncing off the first sanity check.
func validNTFS(volBytes int) []byte {
	b := make([]byte, volBytes)
	b[0], b[1], b[2] = 0xEB, 0x52, 0x90
	copy(b[3:], []byte("NTFS    "))
	binary.LittleEndian.PutUint16(b[0x0B:], 512) // bytes/sector
	b[0x0D] = 8                                  // sectors/cluster -> 4 KiB clusters
	b[0x15] = 0xF8                               // media descriptor
	binary.LittleEndian.PutUint16(b[0x18:], 63)  // sectors/track
	binary.LittleEndian.PutUint16(b[0x1A:], 255) // heads
	binary.LittleEndian.PutUint64(b[0x28:], uint64(volBytes/512))
	binary.LittleEndian.PutUint64(b[0x30:], 4) // $MFT cluster
	binary.LittleEndian.PutUint64(b[0x38:], 8) // $MFTMirr cluster
	b[0x40] = 0xF6                             // signed -10 => 1024-byte records
	b[0x44] = 0x01                             // clusters per index record
	binary.LittleEndian.PutUint64(b[0x48:], 0x0123456789ABCDEF)
	b[510], b[511] = 0x55, 0xAA
	return b
}

// NTFS is parsed by a third-party library (go-ntfs) walking deeply nested
// on-disk structures, which is the largest untrusted-input surface in this
// module. A crafted offset there surfaces as an index-out-of-range, not an
// error return — so without a panic boundary a bad image takes the process
// down, and in the service that means an unrelated running backup dies too.
func TestNTFSParserHostileInput(t *testing.T) {
	rng := rand.New(rand.NewSource(44))
	const vol = 1 << 20
	for i := 0; i < 60; i++ {
		img := validNTFS(vol)
		for m := 0; m < 1+rng.Intn(6); m++ {
			img[0x0B+rng.Intn(0x40)] = byte(rng.Intn(256)) // corrupt the BPB
		}
		for j := 0x200; j < len(img); j += rng.Intn(64) + 1 {
			img[j] = byte(rng.Intn(256)) // noise where the $MFT would live
		}
		openAndDrive(t, "ntfs-mutated", img, FSNTFS)
	}
}

// Deterministic NTFS geometries that a corrupt or crafted volume can carry.
// Each is an allocation or an offset sized by the IMAGE rather than by us.
func TestNTFSAdversarialGeometry(t *testing.T) {
	const vol = 1 << 20

	cases := []struct {
		name   string
		mutate func(b []byte)
	}{
		{"mft beyond end of volume", func(b []byte) {
			binary.LittleEndian.PutUint64(b[0x30:], 0xFFFFFFFF)
		}},
		{"record size 2^128 via signed shift", func(b []byte) {
			b[0x40] = 0x80 // signed -128 => 1<<128, overflows any int
		}},
		{"record size 127 clusters", func(b []byte) {
			b[0x40] = 0x7F // 127 * 4 KiB per record
		}},
		{"zero bytes per sector", func(b []byte) {
			binary.LittleEndian.PutUint16(b[0x0B:], 0) // division by zero shape
		}},
		{"zero sectors per cluster", func(b []byte) {
			b[0x0D] = 0
		}},
		{"huge sectors per cluster", func(b []byte) {
			b[0x0D] = 0xFF
		}},
		{"total sectors overflows int64", func(b []byte) {
			binary.LittleEndian.PutUint64(b[0x28:], ^uint64(0))
		}},
		{"index record 127 clusters", func(b []byte) {
			b[0x44] = 0x7F
		}},
		{"mft and mirror identical", func(b []byte) {
			binary.LittleEndian.PutUint64(b[0x38:], 4)
		}},
	}
	for _, c := range cases {
		img := validNTFS(vol)
		c.mutate(img)
		openAndDrive(t, "ntfs/"+c.name, img, FSNTFS)
	}

	// Truncation: an image cut short mid-structure is the commonest real
	// corruption, and every offset the boot sector declares is now past EOF.
	for _, n := range []int{512, 1024, 4096, 16384, 65536} {
		img := validNTFS(vol)[:n]
		openAndDrive(t, "ntfs/truncated", img, FSNTFS)
	}
}

// The boundary itself: a parser panic must become a classifiable error, and
// must not hand the caller a half-built result alongside it.
func TestPanicGuardConvertsToError(t *testing.T) {
	boom := func() (_ []Entry, err error) {
		defer catchPanic("ntfs: list /x", &err)
		out := []Entry{{Path: "/partial"}}
		_ = out
		var s []int
		_ = s[5] // the shape a crafted offset produces inside go-ntfs
		return out, nil
	}
	got, err := boom()
	if err == nil {
		t.Fatal("a parser panic did not become an error — it would reach the process")
	}
	if !errors.Is(err, ErrCorruptStructure) {
		t.Errorf("error is not classifiable as corruption: %v", err)
	}
	if got != nil {
		t.Errorf("a partial result leaked alongside the error: %v", got)
	}

	// The (value, ok) accessors carry no error, so a panic degrades to
	// "unknown" — which those APIs already model.
	usedBytes := func() (_ int64, ok bool) {
		defer catchPanicBool(&ok)
		panic("bad cluster geometry")
	}
	if _, ok := usedBytes(); ok {
		t.Error("a panicking size probe reported success")
	}
}
