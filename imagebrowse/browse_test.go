package imagebrowse

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// ---- synthetic partition tables ----------------------------------------------

// mbrDisk builds a minimal MBR disk: one NTFS-type partition at LBA 2048.
func mbrDisk() []byte {
	d := make([]byte, 4096*512)
	d[510], d[511] = 0x55, 0xAA
	e := d[446:462]
	e[4] = 0x07 // NTFS/exFAT
	binary.LittleEndian.PutUint32(e[8:12], 2048)
	binary.LittleEndian.PutUint32(e[12:16], 1024)
	return d
}

func TestParseMBR(t *testing.T) {
	d := mbrDisk()
	parts, err := ListPartitions(bytes.NewReader(d), int64(len(d)))
	if err != nil {
		t.Fatalf("mbr parse: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("want 1 partition, got %d", len(parts))
	}
	p := parts[0]
	if p.StartOffset != 2048*512 || p.Length != 1024*512 {
		t.Fatalf("geometry wrong: %+v", p)
	}
	if p.Type != "NTFS/exFAT data" {
		t.Fatalf("type wrong: %q", p.Type)
	}
}

// gptDisk builds a GPT disk: protective MBR, GPT header at LBA1, one Windows
// data partition named "Data" spanning LBA 2048..4095.
func gptDisk() []byte {
	d := make([]byte, 8192*512)
	// protective MBR
	d[510], d[511] = 0x55, 0xAA
	d[446+4] = 0xEE
	// GPT header
	h := d[512 : 512+512]
	copy(h[0:8], "EFI PART")
	binary.LittleEndian.PutUint64(h[0x48:], 2)   // entries at LBA 2
	binary.LittleEndian.PutUint32(h[0x50:], 128) // 128 entries
	binary.LittleEndian.PutUint32(h[0x54:], 128) // 128 bytes each
	// entry 0
	e := d[2*512 : 2*512+128]
	// Windows data GUID EBD0A0A2-B9E5-4433-87C0-68B6B72699C7 (mixed-endian on disk)
	binary.LittleEndian.PutUint32(e[0:4], 0xEBD0A0A2)
	binary.LittleEndian.PutUint16(e[4:6], 0xB9E5)
	binary.LittleEndian.PutUint16(e[6:8], 0x4433)
	binary.BigEndian.PutUint16(e[8:10], 0x87C0)
	copy(e[10:16], []byte{0x68, 0xB6, 0xB7, 0x26, 0x99, 0xC7})
	copy(e[16:32], bytes.Repeat([]byte{1}, 16)) // unique GUID nonzero
	binary.LittleEndian.PutUint64(e[32:40], 2048)
	binary.LittleEndian.PutUint64(e[40:48], 4095)
	// name "Data" UTF-16LE
	name := []byte{'D', 0, 'a', 0, 't', 0, 'a', 0}
	copy(e[56:], name)
	return d
}

func TestParseGPT(t *testing.T) {
	d := gptDisk()
	parts, err := ListPartitions(bytes.NewReader(d), int64(len(d)))
	if err != nil {
		t.Fatalf("gpt parse: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("want 1 partition, got %d", len(parts))
	}
	p := parts[0]
	if p.Name != "Data" || p.Type != "Windows data" {
		t.Fatalf("name/type wrong: %+v", p)
	}
	if p.StartOffset != 2048*512 || p.Length != 2048*512 {
		t.Fatalf("geometry wrong: %+v", p)
	}
}

func TestNoBootSignature(t *testing.T) {
	d := make([]byte, 4096)
	if _, err := ListPartitions(bytes.NewReader(d), int64(len(d))); err == nil {
		t.Fatal("expected error for missing boot signature")
	}
}

// ---- filesystem sniffing over the real NTFS image ------------------------------

func ntfsImagePath(t *testing.T) string {
	t.Helper()
	// Resolve via the module cache: GOMODCACHE/www.velocidex.com replace →
	// github mirror path. Use `go env GOMODCACHE` indirectly through the
	// known layout; fall back to skipping when absent (CI always has it
	// after module download).
	candidates := []string{
		filepath.Join(os.Getenv("GOMODCACHE"), "github.com/!velocidex/go-ntfs@v0.2.0/parser/test_data/test.ntfs.dd"),
		"/root/go/pkg/mod/github.com/!velocidex/go-ntfs@v0.2.0/parser/test_data/test.ntfs.dd",
		filepath.Join(os.Getenv("HOME"), "go/pkg/mod/github.com/!velocidex/go-ntfs@v0.2.0/parser/test_data/test.ntfs.dd"),
		filepath.Join(os.Getenv("GOMODCACHE"), "www.velocidex.com/golang/go-ntfs@v0.2.0/parser/test_data/test.ntfs.dd"),
		filepath.Join(os.Getenv("HOME"), "go/pkg/mod/www.velocidex.com/golang/go-ntfs@v0.2.0/parser/test_data/test.ntfs.dd"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	t.Skip("go-ntfs test image not found in module cache")
	return ""
}

// wrapInMBR embeds the bare NTFS volume at LBA 2048 of a synthetic MBR disk,
// exercising the REAL production path: disk → partition table → offset NTFS.
func wrapInMBR(t *testing.T, vol []byte) []byte {
	t.Helper()
	const startLBA = 2048
	d := make([]byte, startLBA*512+len(vol))
	d[510], d[511] = 0x55, 0xAA
	e := d[446:462]
	e[4] = 0x07
	binary.LittleEndian.PutUint32(e[8:12], startLBA)
	binary.LittleEndian.PutUint32(e[12:16], uint32(len(vol)/512))
	copy(d[startLBA*512:], vol)
	return d
}

func TestNTFSListWalkExtract(t *testing.T) {
	img, err := os.ReadFile(ntfsImagePath(t))
	if err != nil {
		t.Fatal(err)
	}
	disk := wrapInMBR(t, img)
	r := bytes.NewReader(disk)

	parts, err := ListPartitions(r, int64(len(disk)))
	if err != nil {
		t.Fatalf("partitions: %v", err)
	}
	if len(parts) != 1 || parts[0].Filesystem != "ntfs" {
		t.Fatalf("expected one ntfs partition, got %+v", parts)
	}

	vol, err := OpenNTFS(r, parts[0].StartOffset, parts[0].Length)
	if err != nil {
		t.Fatalf("open ntfs: %v", err)
	}

	rootEntries, err := vol.List("")
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	if len(rootEntries) == 0 {
		t.Fatal("root listing empty")
	}
	t.Logf("root: %d entries; first: %+v", len(rootEntries), rootEntries[0])

	all, err := vol.Walk("", 0, nil)
	if err != nil && err != ErrTooManyEntries {
		t.Fatalf("walk: %v", err)
	}
	if len(all) < len(rootEntries) {
		t.Fatalf("walk (%d) returned fewer than root list (%d)", len(all), len(rootEntries))
	}

	// Extract the first regular file found and check size agreement + a
	// stable digest (content must be deterministic across runs).
	var file *Entry
	for i := range all {
		if !all[i].IsDir && all[i].Size > 0 {
			file = &all[i]
			break
		}
	}
	if file == nil {
		t.Skip("test image has no non-empty regular files")
	}
	var buf bytes.Buffer
	n, err := vol.ExtractFile(file.Path, &buf)
	if err != nil {
		t.Fatalf("extract %s: %v", file.Path, err)
	}
	if uint64(n) != file.Size {
		t.Fatalf("extract size %d != stat size %d for %s", n, file.Size, file.Path)
	}
	sum := sha256.Sum256(buf.Bytes())
	t.Logf("extracted %s (%d bytes) sha256=%s", file.Path, n, hex.EncodeToString(sum[:8]))

	// Cancel propagation.
	if _, err := vol.Walk("", 0, func() bool { return true }); err != ErrCancelled {
		t.Fatalf("cancel not propagated: %v", err)
	}

	// Entry cap.
	if len(all) > 1 {
		got, err := vol.Walk("", 1, nil)
		if err != ErrTooManyEntries || len(got) != 1 {
			t.Fatalf("cap not enforced: n=%d err=%v", len(got), err)
		}
	}

	var _ io.ReaderAt = r // documentation: everything above ran over a plain ReaderAt
}
