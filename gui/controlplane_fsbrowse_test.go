package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Live filesystem browsing for the portal's target pickers — see
// controlplane_fsbrowse.go. What is pinned here is the CONFINEMENT and the
// listing order, because those are the two things an operator cannot check
// for themselves from a browser.

func TestAPathMustBeOnThisMachine(t *testing.T) {
	for _, bad := range []string{
		`\\fileserver\share`, // someone else's machine, via this service's credentials
		`//fileserver/share`,
		`\\.\PhysicalDrive0`, // raw device: the image engine's business, not browsable
		`\\?\C:\Users`,
		"",
		"   ",
	} {
		if _, err := cpBrowsePath(bad); err == nil {
			t.Fatalf("%q was accepted as a browsable path", bad)
		}
	}
}

func TestARelativePathIsRefused(t *testing.T) {
	// It would resolve against the service's working directory, which is not
	// a place anybody chose — and the stored job target would then mean
	// something different to every process that read it.
	for _, rel := range []string{"Users", "./Users", "..", `..\Windows`} {
		if _, err := cpBrowsePath(rel); err == nil {
			t.Fatalf("%q was accepted as a browsable path", rel)
		}
	}
}

func TestAPathIsCleanedBeforeItIsRead(t *testing.T) {
	in := "/tmp/a/../b"
	if runtime.GOOS == "windows" {
		in = `C:\Users\..\Windows`
	}
	got, err := cpBrowsePath(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != filepath.Clean(in) {
		t.Fatalf("got %q, want %q — the portal must see the path it will store", got, filepath.Clean(in))
	}
}

func TestADirectoryListsFoldersFirst(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"zebra.txt", "apple.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"Zulu", "alpha"} {
		if err := os.Mkdir(filepath.Join(dir, n), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	entries, truncated, err := cpListDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("a four-entry directory reported truncated")
	}
	var order []string
	for _, e := range entries {
		order = append(order, filepath.Base(e["p"].(string)))
	}
	want := []string{"alpha", "Zulu", "apple.txt", "zebra.txt"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v — folders first, then files, each alphabetically", order, want)
	}
}

func TestListingReportsMetadataAndNotContents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("classified"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _, err := cpListDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := entries[0]
	if e["s"].(int64) != int64(len("classified")) {
		t.Fatalf("size came through as %v", e["s"])
	}
	// Reading a customer's file bytes is the restore path, with its own
	// authorization and its own audit trail. Nothing here may carry them.
	for k, v := range e {
		if s, ok := v.(string); ok && strings.Contains(s, "classified") {
			t.Fatalf("key %q carried file contents: %q", k, s)
		}
	}
}
