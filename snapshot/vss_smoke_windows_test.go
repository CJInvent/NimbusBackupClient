//go:build windows

package snapshot

// S10 — VSS smoke on real Windows (ARCHITECTURE.md Part III, Phase 1).
//
// Two distinct things are under test, and the first matters more than the
// second:
//
//  1. The DIAGNOSTICS emit. Before 0.2.150 every VSS success, failure, writer
//     warning and shadow ID was computed and then fmt.Println'd — which in the
//     Windows service goes nowhere. LogFn now carries them into the app log.
//     Silence here means that regressed, and a silent regression is invisible:
//     backups keep working, and the day one fails there is nothing to read.
//     This assertion holds even on a runner that refuses to snapshot.
//
//  2. The snapshot lifecycle itself, where the runner permits it.
//
// Runs non-blocking in CI (continue-on-error) until VSS on hosted runners has
// a track record — an advisory signal beats a flaky gate people learn to skip.

import (
	"os"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

func TestVSSSmokeEmitsDiagnosticsWindows(t *testing.T) {
	var mu sync.Mutex
	var lines []string

	orig := LogFn
	LogFn = func(msg string) {
		mu.Lock()
		lines = append(lines, msg)
		mu.Unlock()
	}
	t.Cleanup(func() { LogFn = orig })

	vol := os.Getenv("SystemDrive")
	if vol == "" {
		vol = "C:"
	}

	// Exercise the GUID identity used by the image engine, including map keys.
	mount, err := windows.UTF16PtrFromString(vol + "\\")
	if err != nil {
		t.Fatal(err)
	}
	name := make([]uint16, 1024)
	if err := windows.GetVolumeNameForVolumeMountPoint(mount, &name[0], uint32(len(name))); err != nil {
		t.Fatal(err)
	}
	volume := windows.UTF16ToString(name)
	var captured map[string]SnapShot
	err = CreateVSSSnapshot([]string{volume}, func(sn map[string]SnapShot) error {
		captured = sn
		return nil
	})

	mu.Lock()
	got := append([]string(nil), lines...)
	mu.Unlock()
	for _, l := range got {
		t.Logf("LogFn: %s", l)
	}

	// The first LogFn call sits before any snapshot work (only an app-data
	// lookup failure returns earlier), so an attempt that produced no lines at
	// all means the diagnostic path is broken rather than the runner.
	if len(got) == 0 {
		t.Fatal("VSS produced no diagnostics through LogFn — successes, failures, " +
			"writer warnings and shadow IDs would all be invisible in the service log")
	}
	if !strings.Contains(strings.Join(got, "\n"), "VSS") {
		t.Errorf("diagnostics do not look like VSS lines: %q", got)
	}

	if err != nil {
		// Advisory: hosted runners may refuse VSS. Assertion 1 already ran.
		t.Skipf("VSS snapshot unavailable on this runner (diagnostics verified): %v", err)
	}

	if _, ok := captured[volume]; !ok {
		t.Fatalf("snapshot GUID missing from callback: %q", volume)
	}

	if len(captured) == 0 {
		t.Fatal("snapshot callback received no volumes")
	}
	for path, snap := range captured {
		if !snap.Valid {
			t.Errorf("snapshot for %s is not marked valid: %+v", path, snap)
		}
		if snap.Id == "" {
			t.Errorf("snapshot for %s has no shadow id: %+v", path, snap)
		}
		if snap.FullPath == "" {
			t.Errorf("snapshot for %s has no full path: %+v", path, snap)
		}
		if snap.ObjectPath == "" {
			t.Errorf("snapshot for %s has no device object path: %+v", path, snap)
		}
	}

	// Cleanup must be safe to call right after a successful snapshot: the
	// service runs it at every start to clear orphaned shadows.
	if err := VSSCleanup(); err != nil {
		t.Errorf("VSSCleanup after a successful snapshot: %v", err)
	}
}

func TestVSSWriterDiagnostics(t *testing.T) {
	var lines []string
	old := LogFn
	LogFn = func(s string) { lines = append(lines, s) }
	t.Cleanup(func() { LogFn = old })
	healthy := "Writer name: 'System Writer'\nState: [1] Stable\nLast error: No error\n"
	if logWriterWarnings(healthy) || len(lines) != 0 {
		t.Fatalf("healthy writer warned: %q", lines)
	}
	failed := "Writer name: 'Other Writer'\nState: [8] Failed\nLast error: Non-retryable error\n"
	if !logWriterWarnings(healthy + failed) {
		t.Fatal("writer failure omitted")
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "Other Writer") || strings.Contains(strings.Join(lines, "\n"), "System Writer") {
		t.Fatalf("wrong writer attribution: %q", lines)
	}
}
