package pbscommon

// V4-BETA-FIXES §13.2 (F-71 to F-74) on every platform: the R1 rule table,
// and the production writer driven with injected Windows attributes, so the
// routing of each class is checked where CI's main test job runs. The real
// NTFS proof is pxar_reparse_windows_test.go (Windows Smoke job).
//
// The attribute and tag values below are the documented winnt.h values, typed
// in from the Windows SDK, not taken from reparse.go. The typical attribute
// sets of a OneDrive placeholder are the documented combination; a capture
// from a real machine is ledger C0 (V4-BETA-REVERIFY ledger) and replaces them
// when it exists (dev rule 25).

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	tArchive      = 0x00000020
	tDirectory    = 0x00000010
	tSparse       = 0x00000200
	tReparse      = 0x00000400
	tOffline      = 0x00001000
	tRecallOpen   = 0x00040000
	tPinned       = 0x00080000
	tUnpinned     = 0x00100000
	tRecallAccess = 0x00400000

	tTagMountPoint  = 0xA0000003
	tTagSymlink     = 0xA000000C
	tTagLxSymlink   = 0xA000001D
	tTagDedup       = 0x80000013
	tTagWof         = 0x80000017
	tTagCloud6      = 0x9000601A
	tTagAppExecLink = 0x8000001B
	tTagAFUnix      = 0x80000023
	tTagThirdParty  = 0x00007A7A
)

func TestClassifyAttrsRuleTable(t *testing.T) {
	cases := []struct {
		name  string
		attrs uint32
		tag   uint32
		want  EntryDisposition
	}{
		{"plain file", tArchive, 0, ArchiveEntry},
		{"plain directory", tDirectory, 0, ArchiveEntry},
		{"junction (profile legacy folder)", tDirectory | tReparse, tTagMountPoint, SkipLink},
		{"directory symlink", tDirectory | tReparse, tTagSymlink, SkipLink},
		{"file symlink", tArchive | tReparse, tTagSymlink, SkipLink},
		{"WSL symlink", tArchive | tReparse, tTagLxSymlink, SkipLink},
		{"app execution alias", tArchive | tReparse, tTagAppExecLink, SkipNotData},
		{"AF_UNIX socket", tArchive | tReparse, tTagAFUnix, SkipNotData},
		{"WOF-compressed file", tArchive | tReparse, tTagWof, ArchiveEntry},
		{"deduplicated file", tArchive | tReparse | tSparse, tTagDedup, ArchiveEntry},
		{"OneDrive online-only file", tArchive | tSparse | tReparse | tOffline | tUnpinned | tRecallAccess, tTagCloud6, SkipOnlineOnly},
		{"OneDrive locally available file", tArchive | tReparse, tTagCloud6, ArchiveEntry},
		{"OneDrive always-keep file", tArchive | tReparse | tPinned, tTagCloud6, ArchiveEntry},
		{"cloud placeholder folder, not yet listed", tDirectory | tReparse | tRecallOpen, tTagCloud6, ArchiveEntry},
		{"tiered file (OFFLINE + third-party tag)", tArchive | tReparse | tOffline, tTagThirdParty, SkipOnlineOnly},
		{"RECALL_ON_OPEN file", tArchive | tRecallOpen, 0, SkipOnlineOnly},
		{"OFFLINE alone: data on disk", tArchive | tOffline, 0, ArchiveEntry},
		{"third-party reparse point with data", tArchive | tReparse, tTagThirdParty, ArchiveEntry},
		{"tag ignored without the reparse attribute", tArchive, tTagMountPoint, ArchiveEntry},
	}
	for _, c := range cases {
		if got := classifyAttrs(c.attrs, c.tag); got != c.want {
			t.Errorf("%s (attrs=%#x tag=%#x): got %d, want %d", c.name, c.attrs, c.tag, got, c.want)
		}
	}
}

// fakeAttrs maps a basename to the Windows attributes and tag the injected
// entryAttrs reports; every other entry is plain.
type fakeAttrs map[string][2]uint32

func (f fakeAttrs) get(path string, fi os.FileInfo) (uint32, uint32, error) {
	if v, ok := f[filepath.Base(path)]; ok {
		return v[0], v[1], nil
	}
	if fi.IsDir() {
		return tDirectory, 0, nil
	}
	return tArchive, 0, nil
}

type archiveRun struct {
	a      *PXARArchive
	stream []byte
	err    error
	opened []string
}

func runWriter(t *testing.T, root string, attrs fakeAttrs, open func(string) (io.ReadCloser, error)) *archiveRun {
	t.Helper()
	r := &archiveRun{}
	var out bytes.Buffer
	a := &PXARArchive{ArchiveName: "test.pxar"}
	a.WriteCB = func(b []byte) error { out.Write(b); return nil }
	a.CatalogWriteCB = func(b []byte) error { return nil }
	if attrs != nil {
		a.entryAttrs = attrs.get
	}
	a.openForRead = func(p string) (io.ReadCloser, error) {
		r.opened = append(r.opened, filepath.Base(p))
		if open != nil {
			return open(p)
		}
		return os.Open(p)
	}
	a.Create()
	_, r.err = a.WriteDir(root, "", true)
	if r.err == nil {
		if err := a.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	r.a, r.stream = a, out.Bytes()
	return r
}

func listed(t *testing.T, stream []byte) map[string]PXARTreeEntry {
	t.Helper()
	entries, err := NewPXARReader(stream).ListEntries()
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	m := map[string]PXARTreeEntry{}
	for _, e := range entries {
		m[strings.TrimPrefix(e.Path, "/")] = e
	}
	return m
}

func extracted(t *testing.T, stream []byte, rel string) []byte {
	t.Helper()
	dest := t.TempDir()
	if _, err := NewPXARReader(stream).ExtractAll(dest); err != nil {
		t.Fatalf("ExtractAll: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("%s not restored: %v", rel, err)
	}
	return b
}

// R1 through the writer: each class ends where the table says, and nothing
// that must not be opened is opened.
func TestWriterRoutesEachEntryClass(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string][]byte{
		"regular.txt":                  []byte("regular"),
		"wof.bin":                      []byte("wof data supplied by its filter"),
		"cloudfolder/local.txt":        []byte("hydrated file in a placeholder folder"),
		"junction-dir/leak.txt":        []byte("reached only by following a junction"),
		"junction-irregular":           []byte("a junction as Go 1.23+ ReadDir reports it: not a directory"),
		"alias.exe":                    []byte("alias"),
		"online.docx":                  []byte("must not be opened"),
		"tiered.vhdx":                  []byte("must not be opened"),
		"offline-local.txt":            []byte("OFFLINE alone"),
		"filelink.txt":                 []byte("a file symlink"),
		"cloudfolder/online-inner.pdf": []byte("must not be opened"),
	})
	attrs := fakeAttrs{
		"wof.bin":            {tArchive | tReparse, tTagWof},
		"cloudfolder":        {tDirectory | tReparse | tRecallOpen, tTagCloud6},
		"local.txt":          {tArchive | tReparse, tTagCloud6},
		"junction-dir":       {tDirectory | tReparse, tTagMountPoint},
		"junction-irregular": {tDirectory | tReparse, tTagMountPoint},
		"alias.exe":          {tArchive | tReparse, tTagAppExecLink},
		"online.docx":        {tArchive | tSparse | tReparse | tOffline | tUnpinned | tRecallAccess, tTagCloud6},
		"online-inner.pdf":   {tArchive | tSparse | tReparse | tOffline | tUnpinned | tRecallAccess, tTagCloud6},
		"tiered.vhdx":        {tArchive | tReparse | tOffline, tTagThirdParty},
		"offline-local.txt":  {tArchive | tOffline, 0},
		"filelink.txt":       {tArchive | tReparse, tTagSymlink},
	}
	r := runWriter(t, root, attrs, nil)
	if r.err != nil {
		t.Fatalf("WriteDir: %v", r.err)
	}
	if len(r.a.ReadErrors) != 0 {
		t.Errorf("no entry here is a read error; got %q", r.a.ReadErrors)
	}

	got := listed(t, r.stream)
	for _, want := range []string{"regular.txt", "wof.bin", "cloudfolder", "cloudfolder/local.txt", "offline-local.txt"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s must be archived; missing", want)
		}
	}
	for _, not := range []string{"junction-dir", "junction-dir/leak.txt", "junction-irregular", "alias.exe", "online.docx", "tiered.vhdx", "filelink.txt", "cloudfolder/online-inner.pdf"} {
		if e, ok := got[not]; ok {
			t.Errorf("%s must not be archived (dir=%v size=%d)", not, e.IsDir, e.Size)
		}
	}
	for _, name := range r.opened {
		switch name {
		case "online.docx", "tiered.vhdx", "online-inner.pdf", "alias.exe", "junction-irregular", "filelink.txt":
			t.Errorf("%s was opened; R1 decides before anything is opened", name)
		}
	}

	wantOnline := []string{filepath.Join(root, "cloudfolder", "online-inner.pdf"), filepath.Join(root, "online.docx"), filepath.Join(root, "tiered.vhdx")}
	if strings.Join(r.a.OnlineOnlyFiles, "|") != strings.Join(wantOnline, "|") {
		t.Errorf("OnlineOnlyFiles = %q, want %q", r.a.OnlineOnlyFiles, wantOnline)
	}
	skipped := strings.Join(r.a.SkippedFiles, "\n")
	for _, p := range []string{"junction-dir", "junction-irregular", "alias.exe", "online.docx", "tiered.vhdx", "filelink.txt"} {
		if !strings.Contains(skipped, filepath.Join(root, p)) {
			t.Errorf("%s is not listed as skipped: %q", p, r.a.SkippedFiles)
		}
	}
	if got := extracted(t, r.stream, "wof.bin"); string(got) != "wof data supplied by its filter" {
		t.Errorf("wof.bin restored as %q", got)
	}
}

// A reparse point whose tag cannot be read is a read error, and is skipped.
func TestWriterUnreadableTagIsAReadError(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string][]byte{"mystery.dat": []byte("x"), "ok.txt": []byte("ok")})
	var out bytes.Buffer
	a := &PXARArchive{ArchiveName: "test.pxar"}
	a.WriteCB = func(b []byte) error { out.Write(b); return nil }
	a.CatalogWriteCB = func(b []byte) error { return nil }
	a.entryAttrs = func(p string, fi os.FileInfo) (uint32, uint32, error) {
		if filepath.Base(p) == "mystery.dat" {
			return tArchive | tReparse, 0, errors.New("FindFirstFile: access denied")
		}
		return fakeAttrs(nil).get(p, fi)
	}
	a.Create()
	if _, err := a.WriteDir(root, "", true); err != nil {
		t.Fatalf("WriteDir: %v", err)
	}
	if err := a.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(a.ReadErrors) != 1 || !strings.Contains(a.ReadErrors[0], "Cannot read reparse point type") {
		t.Errorf("ReadErrors = %q, want one 'Cannot read reparse point type'", a.ReadErrors)
	}
	if _, ok := listed(t, out.Bytes())["mystery.dat"]; ok {
		t.Error("mystery.dat was archived")
	}
}

type failingReader struct {
	data   []byte
	pos    int
	failAt int
	onFail func()
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.pos >= f.failAt {
		if f.onFail != nil {
			f.onFail()
		}
		return 0, errors.New("simulated: the request could not be performed because of an I/O device error")
	}
	n := copy(p, f.data[f.pos:f.failAt])
	f.pos += n
	return n, nil
}
func (f *failingReader) Close() error { return nil }

// R2 (F-72): a read error after the length is committed zero-fills the rest,
// records a read error naming the offset, and the run goes on with the stream
// aligned: the next file restores byte for byte.
func TestWriterMidFileReadErrorZeroFillsAndContinues(t *testing.T) {
	root := t.TempDir()
	big := bytes.Repeat([]byte("0123456789abcdef"), 6400) // 102400 bytes
	writeTree(t, root, map[string][]byte{"a.txt": []byte("first"), "b.bin": big, "c.txt": []byte("after the bad file")})
	r := runWriter(t, root, nil, func(p string) (io.ReadCloser, error) {
		if filepath.Base(p) == "b.bin" {
			return &failingReader{data: big, failAt: 70000}, nil
		}
		return os.Open(p)
	})
	if r.err != nil {
		t.Fatalf("one unreadable file must not end the backup: %v", r.err)
	}
	if len(r.a.ReadErrors) != 1 || !strings.Contains(r.a.ReadErrors[0], "Read failed at byte 70000 of 102400") {
		t.Fatalf("ReadErrors = %q", r.a.ReadErrors)
	}
	got := extracted(t, r.stream, "b.bin")
	if len(got) != len(big) || !bytes.Equal(got[:70000], big[:70000]) || !bytes.Equal(got[70000:], make([]byte, len(big)-70000)) {
		t.Errorf("b.bin: want the first 70000 bytes then zeros to 102400; got %d bytes", len(got))
	}
	if got := extracted(t, r.stream, "c.txt"); string(got) != "after the bad file" {
		t.Errorf("c.txt after the bad file restored as %q: the stream lost alignment", got)
	}
}

// R3 (F-74): when the root itself stops answering, the backup fails with
// ErrSourceUnavailable instead of finishing as "partial" with an empty tail.
// Checked from each place a read error can arise.
func TestWriterFailsWhenTheSourceVanishes(t *testing.T) {
	for _, how := range []string{"open", "mid-read"} {
		t.Run(how, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, map[string][]byte{"a.txt": []byte("a"), "b.bin": bytes.Repeat([]byte{1}, 1000), "sub/c.txt": []byte("c")})
			vanish := func() { _ = os.RemoveAll(root) }
			r := runWriter(t, root, nil, func(p string) (io.ReadCloser, error) {
				if filepath.Base(p) != "b.bin" {
					return os.Open(p)
				}
				if how == "open" {
					vanish()
					return nil, errors.New("simulated: the device is not ready")
				}
				return &failingReader{data: bytes.Repeat([]byte{1}, 1000), failAt: 10, onFail: vanish}, nil
			})
			if !errors.Is(r.err, ErrSourceUnavailable) {
				t.Fatalf("WriteDir = %v, want ErrSourceUnavailable", r.err)
			}
			if len(r.a.ReadErrors) != 0 {
				t.Errorf("a vanished source is one failure, not per-file read errors: %q", r.a.ReadErrors)
			}
		})
	}
}

// R3's other side: with the root intact an unopenable file stays a read
// error on that file, and the backup completes.
func TestWriterOpenFailureWithSourceIntactIsAReadError(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string][]byte{"locked.pst": []byte("x"), "ok.txt": []byte("ok")})
	r := runWriter(t, root, nil, func(p string) (io.ReadCloser, error) {
		if filepath.Base(p) == "locked.pst" {
			return nil, errors.New("simulated: the process cannot access the file because it is being used by another process")
		}
		return os.Open(p)
	})
	if r.err != nil {
		t.Fatalf("WriteDir: %v", r.err)
	}
	if len(r.a.ReadErrors) != 1 || !strings.Contains(r.a.ReadErrors[0], "Cannot open file") {
		t.Errorf("ReadErrors = %q", r.a.ReadErrors)
	}
	if got := extracted(t, r.stream, "ok.txt"); string(got) != "ok" {
		t.Errorf("ok.txt = %q", got)
	}
}
