package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestFilterLogByTimeWindow proves the core filtering logic against
// exactly the log line shapes writeLogToLogger produces (see
// logging_service.go) and the exact scenario from the incident that
// motivated this feature: a VSS failure and per-partition detail that
// should land inside a run's window, with unrelated lines before/after
// excluded.
func TestFilterLogByTimeWindow(t *testing.T) {
	f, err := os.CreateTemp("", "logtest-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(f.Name()) }()

	lines := []string{
		"[SERVICE] [2026-07-28 09:00:00] Before the window entirely",
		"[SERVICE] [2026-07-28 09:06:42] Starting machine backup",
		"[SERVICE] [2026-07-28 09:06:44] VSS FAILED: snapshot of C:\\",
		"[SERVICE] [2026-07-28 09:06:44] Partition 1: offset=1MB, length=529MB",
		"not a real log line at all, no timestamp",
		"[SERVICE] [2026-07-28 09:08:42] [controlplane] policy applied: file_restore=false",
		"[SERVICE] [2026-07-28 10:30:00] Way after the window",
	}
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 7, 28, 9, 6, 0, 0, time.Local)
	end := time.Date(2026, 7, 28, 9, 7, 0, 0, time.Local)

	t.Run("window only", func(t *testing.T) {
		out, kept, err := filterLogByTimeWindow(f.Name(), start, end, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if kept != 3 {
			t.Errorf("kept = %d, want 3", kept)
		}
		for _, want := range []string{"Starting machine backup", "VSS FAILED", "Partition 1"} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing expected line containing %q:\n%s", want, out)
			}
		}
		for _, unwanted := range []string{"Before the window", "Way after"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("out-of-window content leaked into output: %q", unwanted)
			}
		}
		if strings.Contains(out, "no timestamp") {
			t.Error("the unparseable line must be skipped, not included")
		}
	})

	t.Run("keyword narrows to the checkpoint", func(t *testing.T) {
		out, kept, err := filterLogByTimeWindow(f.Name(), start, end, "VSS")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if kept != 1 || !strings.Contains(out, "VSS FAILED") {
			t.Errorf("keyword filter did not isolate the VSS line: kept=%d out=%q", kept, out)
		}
	})

	t.Run("nonexistent file returns a real error", func(t *testing.T) {
		_, _, err := filterLogByTimeWindow("/tmp/does-not-exist-xyz-123", start, end, "")
		if err == nil {
			t.Error("expected an error opening a nonexistent file, got nil")
		}
	})
}

// TestFilterLogByTimeWindowCrossTimezone proves the timezone handling
// specifically: a log line is written in the MACHINE'S LOCAL time (see
// writeLogToLogger: time.Now().Format(...), no zone info in the string),
// while the server sends UTC window boundaries. The comparison must be
// correct on the underlying instant regardless of the machine's zone --
// this was verified in isolation before landing here, not assumed.
func TestFilterLogByTimeWindowCrossTimezone(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skip("no tzdata available in this environment:", err)
	}
	origLocal := time.Local
	time.Local = loc
	defer func() { time.Local = origLocal }()

	trueMoment := time.Date(2026, 7, 28, 14, 6, 42, 0, time.UTC) // 09:06:42 in Chicago (CDT, UTC-5)
	localLine := "[SERVICE] [" + trueMoment.In(loc).Format("2006-01-02 15:04:05") + "] Local-time log line"

	f, err := os.CreateTemp("", "logtz-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(localLine + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// A UTC window that legitimately contains trueMoment.
	winStart := trueMoment.Add(-1 * time.Minute)
	winEnd := trueMoment.Add(1 * time.Minute)

	out, kept, err := filterLogByTimeWindow(f.Name(), winStart, winEnd, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kept != 1 || !strings.Contains(out, "Local-time log line") {
		t.Errorf("a Chicago-local-time log line was not recognized as inside a UTC window: kept=%d out=%q", kept, out)
	}
}
