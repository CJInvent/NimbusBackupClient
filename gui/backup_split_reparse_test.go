//go:build !windows

package main

// R4 (F-73) on the split plan. Not built for Windows: it stands a FIFO in for
// a WOF-compressed file (a non-regular entry that is data), and Windows has
// no FIFOs. The helpers are in backup_reparse_test.go.

import (
	"os"
	"path/filepath"
	"pbscommon"
	"syscall"
	"testing"
)

// R4 (F-73): a root that directly holds a file which is not IsRegular but is
// still data (on Windows: WOF-compressed, a cloud file on the device) gets its
// remainder job; a root holding only links does not.
func TestSplitPlanRootRemainderFollowsTheWriterRule(t *testing.T) {
	base := t.TempDir()
	irregular := filepath.Join(base, "irregular")
	mkfile(t, filepath.Join(irregular, "sub", "x.bin"), 10)
	// Stand-in for a WOF file: a non-regular, non-directory entry that the
	// classifier (injected, like Windows would report it) says is data.
	if err := syscall.Mkfifo(filepath.Join(irregular, "wof.bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	linksOnly := filepath.Join(base, "links")
	mkfile(t, filepath.Join(linksOnly, "sub", "y.bin"), 10)
	if err := os.Symlink(filepath.Join(linksOnly, "sub"), filepath.Join(linksOnly, "My Documents")); err != nil {
		t.Fatal(err)
	}
	onlineOnly := filepath.Join(base, "online")
	mkfile(t, filepath.Join(onlineOnly, "sub", "z.bin"), 10)
	mkfile(t, filepath.Join(onlineOnly, "cloud.docx"), 10)
	fakeClassify(t, map[string]pbscommon.EntryDisposition{
		"wof.bin":    pbscommon.ArchiveEntry,
		"cloud.docx": pbscommon.SkipOnlineOnly,
	})

	a, err := AnalyzeBackupDirs([]string{irregular, linksOnly, onlineOnly}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range a.RootsWithFiles {
		got[r] = true
	}
	if !got[irregular] {
		t.Errorf("root with a non-regular data file has no remainder job: its file would be in no job")
	}
	if got[linksOnly] {
		t.Errorf("root holding only a link got a remainder job")
	}
	if !got[onlineOnly] {
		t.Errorf("root with an online-only file has no remainder job: the run would not list it")
	}
}
