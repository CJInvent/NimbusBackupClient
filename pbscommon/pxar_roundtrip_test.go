package pbscommon

// S6 — PXAR writer -> reader round trip (ARCHITECTURE.md Part III, Phase 1).
//
// The suite already covered the reader against fixtures and the chunker
// against a corpus, but nothing checked the two halves against EACH OTHER.
// That is the gap that matters most: the writer produces the archive a
// directory backup is made of, and the reader is what a restore runs. If they
// disagree, backups keep succeeding and only the restore fails — which is the
// exact failure mode dev rule 1 exists to prevent, discovered at the worst
// possible moment.
//
// So this writes a real tree through the production writer, reads it back
// through the production reader, and demands the bytes match.

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// writeTree materializes a fixture tree on disk and returns the expected
// file contents keyed by their archive-relative slash path.
func writeTree(t *testing.T, root string, files map[string][]byte) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

// archiveTree runs the production writer over a directory and returns the
// complete pxar stream.
func archiveTree(t *testing.T, root string) []byte {
	t.Helper()
	var out bytes.Buffer
	a := &PXARArchive{ArchiveName: "test.pxar"}
	a.WriteCB = func(b []byte) error { out.Write(b); return nil }
	a.CatalogWriteCB = func(b []byte) error { return nil } // separate stream; not under test here
	a.Create()
	if _, err := a.WriteDir(root, "", true); err != nil {
		t.Fatalf("WriteDir: %v", err)
	}
	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(a.ReadErrors) != 0 {
		t.Fatalf("writer reported read errors on a fixture tree it created: %v", a.ReadErrors)
	}
	return out.Bytes()
}

func TestPXARRoundTripPreservesEveryByte(t *testing.T) {
	// Deliberately awkward: an empty file, a file whose size crosses the
	// writer's internal buffering, nested directories, and names with spaces
	// and dots — the shapes that break naive length or path handling.
	big := make([]byte, 512*1024)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"readme.txt":                []byte("hello pxar\n"),
		"empty.dat":                 {},
		"one.bin":                   {0x00},
		"docs/report.final.txt":     []byte(strings.Repeat("report ", 1000)),
		"docs/notes with spaces.md": []byte("# notes\n"),
		"deep/a/b/c/leaf.txt":       []byte("leaf"),
		"data/large.bin":            big,
		"data/zeros.bin":            make([]byte, 70000),
	}

	src := t.TempDir()
	writeTree(t, src, files)

	archive := archiveTree(t, src)
	if len(archive) == 0 {
		t.Fatal("writer produced an empty archive")
	}

	// --- read the tree back -------------------------------------------------
	entries, err := NewPXARReader(archive).ListEntries()
	if err != nil {
		t.Fatalf("ListEntries on an archive we just wrote: %v", err)
	}

	gotFiles := map[string]uint64{}
	gotDirs := map[string]bool{}
	for _, e := range entries {
		p := strings.TrimPrefix(e.Path, "/")
		if e.IsDir {
			gotDirs[p] = true
			continue
		}
		gotFiles[p] = e.Size
	}

	for rel, content := range files {
		size, ok := gotFiles[rel]
		if !ok {
			var listed []string
			for p := range gotFiles {
				listed = append(listed, p)
			}
			sort.Strings(listed)
			t.Fatalf("file %q written but not listed on read-back; got %v", rel, listed)
		}
		if size != uint64(len(content)) {
			t.Errorf("%s: listed size %d, wrote %d bytes", rel, size, len(content))
		}
	}
	for _, dir := range []string{"docs", "deep", "deep/a", "deep/a/b", "deep/a/b/c", "data"} {
		if !gotDirs[dir] {
			t.Errorf("directory %q is missing from the read-back tree", dir)
		}
	}

	// --- extract and compare byte for byte ----------------------------------
	dest := t.TempDir()
	extracted, err := NewPXARReader(archive).ExtractAll(dest)
	if err != nil {
		t.Fatalf("ExtractAll: %v", err)
	}
	if len(extracted) == 0 {
		t.Fatal("ExtractAll reported nothing extracted")
	}

	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: not extracted: %v", rel, err)
			continue
		}
		if !bytes.Equal(got, want) {
			// Never dump the payloads; on a 512 KiB file that is unreadable.
			t.Errorf("%s: content differs after round trip (wrote %d bytes, read %d)",
				rel, len(want), len(got))
		}
	}
}

// Selective restore is the common case in the GUI — a user picks a few files
// out of a snapshot. It must return exactly those, and never their siblings.
func TestPXARRoundTripSelectiveExtract(t *testing.T) {
	files := map[string][]byte{
		"keep/wanted.txt":     []byte("wanted"),
		"keep/also.txt":       []byte("also"),
		"skip/unwanted.txt":   []byte("unwanted"),
		"skip/deep/other.bin": []byte("other"),
	}
	src := t.TempDir()
	writeTree(t, src, files)
	archive := archiveTree(t, src)

	dest := t.TempDir()
	if _, err := NewPXARReader(archive).ExtractFiltered(dest, []string{"/keep/wanted.txt"}, true); err != nil {
		t.Fatalf("ExtractFiltered: %v", err)
	}

	if got, err := os.ReadFile(filepath.Join(dest, "keep", "wanted.txt")); err != nil {
		t.Errorf("the requested file was not extracted: %v", err)
	} else if string(got) != "wanted" {
		t.Errorf("requested file content = %q, want %q", got, "wanted")
	}

	for _, unwanted := range []string{
		filepath.Join(dest, "keep", "also.txt"),
		filepath.Join(dest, "skip", "unwanted.txt"),
		filepath.Join(dest, "skip", "deep", "other.bin"),
	} {
		if _, err := os.Stat(unwanted); err == nil {
			t.Errorf("%s was extracted but not requested — a selective restore must not spill siblings", unwanted)
		}
	}
}

// A virtual file is how the backup's own metadata sidecar rides inside the
// archive, so it has to survive the same round trip.
func TestPXARRoundTripVirtualFile(t *testing.T) {
	var out bytes.Buffer
	a := &PXARArchive{ArchiveName: "test.pxar"}
	a.WriteCB = func(b []byte) error { out.Write(b); return nil }
	a.CatalogWriteCB = func(b []byte) error { return nil }
	a.Create()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.WriteDir(src, "", true); err != nil {
		t.Fatalf("WriteDir: %v", err)
	}
	if err := a.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, err := NewPXARReader(out.Bytes()).ReadVirtualFile("real.txt")
	if err != nil {
		t.Fatalf("ReadVirtualFile on a file we just wrote: %v", err)
	}
	if string(got) != "real" {
		t.Errorf("ReadVirtualFile = %q, want %q", got, "real")
	}

	// A name that is not in the archive must be an error, not empty data that
	// a caller could mistake for an empty sidecar.
	if _, err := NewPXARReader(out.Bytes()).ReadVirtualFile("nope.json"); err == nil {
		t.Error("reading a nonexistent virtual file returned no error")
	}
}
