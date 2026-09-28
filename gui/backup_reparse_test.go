package main

// V4-BETA-FIXES §13.2 on the GUI side: the run outcome when online-only files
// were left out (R1), and the split plan and size estimate applying the
// writer's classification (R4, F-73). The classifier is injected so Windows
// entries can be presented on Linux; the real NTFS proof of the rule itself is
// pbscommon's Windows smoke.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"pbscommon"
	"testing"
	"time"
)

func TestOutcomeTreatsOnlineOnlyAsAKnownAbsence(t *testing.T) {
	cases := []struct {
		name                           string
		failed                         uint64
		dirsFailed                     bool
		readErrs, excluded, onlineOnly int
		want                           BackupOutcome
	}{
		{"clean", 0, false, 0, 0, 0, OutcomeVerifiedSuccess},
		{"online-only only", 0, false, 0, 0, 3, OutcomeSuccessWithExclusions},
		{"online-only and a read error", 0, false, 1, 0, 3, OutcomePartial},
		{"online-only and a failed directory", 0, true, 0, 0, 3, OutcomePartial},
		{"online-only and a failed chunk", 1, false, 0, 0, 3, OutcomeFailed},
		{"policy exclusion", 0, false, 0, 2, 0, OutcomeSuccessWithExclusions},
		{"read error", 0, false, 1, 0, 0, OutcomePartial},
	}
	for _, c := range cases {
		if got := decideOutcome(c.failed, c.dirsFailed, c.readErrs, c.excluded, c.onlineOnly); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	// And the server sees Success for it: online-only files are not read errors.
	s := &BackupStatus{Outcome: OutcomeSuccessWithExclusions,
		SkippedOnlineOnly: onlineOnlyToIssues([]string{`C:\Users\a\OneDrive\big.pst`})}
	if kind, _ := decideRunReport(s, ""); kind != reportSuccess {
		t.Errorf("online-only alone reported as %d, want reportSuccess", kind)
	}
	agg := &BackupStatus{Outcome: OutcomeVerifiedSuccess}
	agg.merge(s)
	if len(agg.SkippedOnlineOnly) != 1 || agg.Outcome != OutcomeSuccessWithExclusions {
		t.Errorf("merge lost the online-only list or outcome: %+v", agg)
	}
}

// fakeClassify presents chosen basenames as Windows entry classes.
func fakeClassify(t *testing.T, kinds map[string]pbscommon.EntryDisposition) {
	t.Helper()
	orig := classifyEntry
	classifyEntry = func(path string, fi os.FileInfo) (pbscommon.EntryDisposition, error) {
		if k, ok := kinds[filepath.Base(path)]; ok {
			return k, nil
		}
		return orig(path, fi)
	}
	t.Cleanup(func() { classifyEntry = orig })
}

func mkfile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// R4: the size estimate counts what the writer archives, not placeholders.
func TestSizeEstimateSkipsWhatTheWriterSkips(t *testing.T) {
	root := t.TempDir()
	mkfile(t, filepath.Join(root, "local.bin"), 1000)
	mkfile(t, filepath.Join(root, "OneDrive", "online.pst"), 50000)
	mkfile(t, filepath.Join(root, "WindowsApps", "alias.exe"), 7000)
	fakeClassify(t, map[string]pbscommon.EntryDisposition{
		"online.pst": pbscommon.SkipOnlineOnly,
		"alias.exe":  pbscommon.SkipNotData,
	})
	size, err := calculateDirSizeCtx(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if size != 1000 {
		t.Errorf("estimate = %d bytes, want 1000 (online-only and alias entries add nothing)", size)
	}
}

// F-76 R8: the session-lost wait ends as soon as the run is stopped.
func TestSessionRetryWaitEndsOnStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := 0
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	err := waitBeforeSessionRetry(ctx, 25*time.Minute, 30*time.Second, func(time.Duration) { ticks++ })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("Stop took %s to end the wait", el)
	}
	if ticks != 1 {
		t.Errorf("progress ticks = %d, want 1", ticks)
	}
	// And a wait nobody stops runs to its end.
	if err := waitBeforeSessionRetry(context.Background(), 30*time.Millisecond, 10*time.Millisecond, nil); err != nil {
		t.Errorf("unstopped wait returned %v", err)
	}
}
