//go:build windows

package pbscommon

// Windows smoke for V4-BETA-FIXES F-71 (Windows Smoke job): what a directory
// backup does with the reparse points a real profile contains, on real NTFS,
// through the production writer and reader. The classification rules
// themselves are covered on every platform by pxar_reparse_test.go; this is
// the proof that Windows hands the writer what those rules assume.
//
// Expected (V4-BETA-FIXES §13.2, rule R1):
//   junction, directory symlink, file symlink  -> not followed, not archived, not a read error
//   app execution alias                        -> not archived, not a read error
//   online-only placeholder (OFFLINE + tag)    -> not archived, not a read error, listed as skipped
//   WOF-compressed file                        -> archived with its real content
//   OFFLINE attribute alone (data on disk)     -> archived with its real content
//   the junction's target directory            -> archived once, where it really is

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unicode/utf16"
)

const (
	testFsctlSetReparsePoint     = 0x000900A4
	testFileFlagOpenReparsePoint = 0x00200000
	testFileAttributeOffline     = 0x00001000
	testFileAttributeReparse     = 0x00000400
	testTagAppExecLink           = 0x8000001B
	// A third-party tag (bit 31 clear, not a name surrogate) with no filter
	// behind it: what a tiering or sync placeholder looks like when its
	// provider is not there to supply the data.
	testTagThirdParty = 0x00007A7A
)

func utf16z(s string) []byte {
	u := utf16.Encode([]rune(s + "\x00"))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return b
}

// setReparsePoint writes a raw reparse buffer onto an existing file.
func setReparsePoint(t *testing.T, path string, buf []byte) {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS|testFileFlagOpenReparsePoint, 0)
	if err != nil {
		t.Fatalf("open %s for FSCTL_SET_REPARSE_POINT: %v", path, err)
	}
	defer syscall.CloseHandle(h)
	var ret uint32
	if err := syscall.DeviceIoControl(h, testFsctlSetReparsePoint, &buf[0], uint32(len(buf)), nil, 0, &ret, nil); err != nil {
		t.Fatalf("FSCTL_SET_REPARSE_POINT on %s: %v", path, err)
	}
}

// appExecLinkBuffer is a Microsoft-format reparse buffer with the app
// execution alias tag: version 3 and the four strings Windows writes.
func appExecLinkBuffer() []byte {
	var data bytes.Buffer
	_ = binary.Write(&data, binary.LittleEndian, uint32(3))
	for _, s := range []string{"Nimbus.Test_8wekyb3d8bbwe", "Nimbus.Test_8wekyb3d8bbwe!App", `C:\Windows\System32\cmd.exe`, "0"} {
		data.Write(utf16z(s))
	}
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint32(testTagAppExecLink))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(data.Len()))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0))
	buf.Write(data.Bytes())
	return buf.Bytes()
}

// thirdPartyBuffer is a REPARSE_GUID_DATA_BUFFER, the format every
// non-Microsoft tag must use.
func thirdPartyBuffer() []byte {
	payload := []byte("placeholder")
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint32(testTagThirdParty))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(payload)))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0))
	buf.Write([]byte{0x4e, 0x49, 0x4d, 0x42, 0x55, 0x53, 0x00, 0x46, 0x37, 0x31, 0x00, 0x54, 0x45, 0x53, 0x54, 0x21})
	buf.Write(payload)
	return buf.Bytes()
}

func setAttrs(t *testing.T, path string, add uint32) {
	t.Helper()
	p, _ := syscall.UTF16PtrFromString(path)
	attrs, err := syscall.GetFileAttributes(p)
	if err != nil {
		t.Fatalf("GetFileAttributes %s: %v", path, err)
	}
	if err := syscall.SetFileAttributes(p, attrs|add); err != nil {
		t.Fatalf("SetFileAttributes %s: %v", path, err)
	}
}

func rawAttrs(t *testing.T, path string) uint32 {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat %s: %v", path, err)
	}
	return fi.Sys().(*syscall.Win32FileAttributeData).FileAttributes
}

func TestWindowsReparseEntriesInADirectoryBackup(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel string, data []byte) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		must(os.MkdirAll(filepath.Dir(full), 0o755))
		must(os.WriteFile(full, data, 0o644))
	}

	// Real data that must be in the archive, byte for byte.
	wofContent := bytes.Repeat([]byte("compressible WOF payload line\r\n"), 4096) // 128 KiB
	want := map[string][]byte{
		"regular.txt":       []byte("regular file"),
		"target/inner.txt":  []byte("reached through the real directory only"),
		"wof.bin":           wofContent,
		"offline-local.txt": []byte("OFFLINE attribute alone moves no data"),
	}
	for rel, data := range want {
		write(rel, data)
	}

	// Junction to target/ (mklink /J needs no privilege).
	junction := filepath.Join(root, "junction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, filepath.Join(root, "target")).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v: %s", err, out)
	}
	// Symbolic links (the runner's account holds the privilege).
	must(os.Symlink(filepath.Join(root, "target"), filepath.Join(root, "dirlink")))
	must(os.Symlink(filepath.Join(root, "regular.txt"), filepath.Join(root, "filelink.txt")))

	// App execution alias, like %LOCALAPPDATA%\Microsoft\WindowsApps\*.exe.
	alias := filepath.Join(root, "alias.exe")
	write("alias.exe", nil)
	setReparsePoint(t, alias, appExecLinkBuffer())

	// Online-only placeholder: OFFLINE plus a tag no filter answers for.
	tiered := filepath.Join(root, "online-only.docx")
	write("online-only.docx", nil)
	setAttrs(t, tiered, testFileAttributeOffline)
	setReparsePoint(t, tiered, thirdPartyBuffer())

	// OFFLINE alone, data on disk.
	setAttrs(t, filepath.Join(root, "offline-local.txt"), testFileAttributeOffline)

	// WOF compression (compact /exe) makes wof.bin a WOF reparse point.
	wof := filepath.Join(root, "wof.bin")
	if out, err := exec.Command("compact", "/c", "/exe:xpress4k", wof).CombinedOutput(); err != nil {
		t.Fatalf("compact /exe: %v: %s", err, out)
	}

	// Setup checks: each fixture is what it claims to be.
	for _, p := range []string{junction, alias, tiered} {
		if rawAttrs(t, p)&testFileAttributeReparse == 0 {
			t.Fatalf("setup: %s is not a reparse point", p)
		}
	}
	// compact /exe succeeds without applying WOF on GitHub's Windows runners
	// (seen 2026-09-28), so wof.bin is then an ordinary file and is still
	// checked as data below. The real-volume check is ledger F71 on the VM.
	if rawAttrs(t, wof)&testFileAttributeReparse == 0 {
		t.Logf("setup: WOF was not applied to %s on this machine; it is checked as a plain data file", wof)
	}
	if fi, _ := os.Lstat(junction); fi != nil {
		t.Logf("toolchain facts: Lstat(junction).Mode()=%v IsDir=%v", fi.Mode(), fi.IsDir())
	}
	if fi, _ := os.Lstat(wof); fi != nil {
		t.Logf("toolchain facts: Lstat(wof).Mode()=%v", fi.Mode())
	}

	// --- back it up through the production writer ---------------------------
	var out bytes.Buffer
	a := &PXARArchive{ArchiveName: "test.pxar"}
	a.WriteCB = func(b []byte) error { out.Write(b); return nil }
	a.CatalogWriteCB = func(b []byte) error { return nil }
	a.Create()
	if _, err := a.WriteDir(root, "", true); err != nil {
		t.Fatalf("WriteDir: %v", err)
	}
	must(a.Flush())

	if len(a.ReadErrors) != 0 {
		t.Errorf("read errors (each downgrades a real profile backup to partial): %q", a.ReadErrors)
	}
	skipped := strings.Join(a.SkippedFiles, "\n")
	for _, p := range []string{junction, filepath.Join(root, "dirlink"), filepath.Join(root, "filelink.txt"), alias, tiered} {
		if !strings.Contains(skipped, p) {
			t.Errorf("%s is not listed as skipped; SkippedFiles=%q", p, a.SkippedFiles)
		}
	}

	entries, err := NewPXARReader(out.Bytes()).ListEntries()
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	listed := map[string]PXARTreeEntry{}
	for _, e := range entries {
		listed[strings.TrimPrefix(e.Path, "/")] = e
	}
	for _, name := range []string{"junction", "junction/inner.txt", "dirlink", "dirlink/inner.txt", "filelink.txt", "alias.exe", "online-only.docx"} {
		if e, ok := listed[name]; ok {
			t.Errorf("%q is in the archive (dir=%v size=%d); it must be skipped", name, e.IsDir, e.Size)
		}
	}

	dest := t.TempDir()
	if _, err := NewPXARReader(out.Bytes()).ExtractAll(dest); err != nil {
		t.Fatalf("ExtractAll: %v", err)
	}
	for rel, data := range want {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: not restored: %v", rel, err)
			continue
		}
		if !bytes.Equal(got, data) {
			t.Errorf("%s: restored %d bytes, backed up %d; content differs", rel, len(got), len(data))
		}
	}
}
