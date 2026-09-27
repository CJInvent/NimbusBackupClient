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
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	vss "github.com/st-matskevich/go-vss"
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

// TestVSSStressThreadMigration is the F-34 reproduction (V4-BETA-FIXES
// §3.2), run by hand on the test VM, never in CI (it takes minutes and needs
// an elevated, VSS-capable machine):
//
//	$env:NIMBUS_VSS_STRESS = "unfixed"   # or "fixed"
//	$env:NIMBUS_VSS_STRESS_CYCLES = "50"  # default 50
//	$env:NIMBUS_VSS_STRESS_OUT = "C:\path\stress-unfixed.json"  # optional evidence file
//	.\snapshot.test.exe -test.run TestVSSStressThreadMigration -test.v -test.timeout 2h
//
// "unfixed" drives go-vss the way CreateVSSSnapshot did before the fix: a
// Snapshotter created and released from a goroutine that is not locked to an
// OS thread, the library's own CoInitialize/CoUninitialize around the create,
// Release afterwards possibly on another thread. "fixed" calls the fixed
// CreateVSSSnapshot. Both run with 4 x GOMAXPROCS goroutines yielding in a
// loop, so the scheduler migrates goroutines between threads as often as it
// can. The fix must give 0 failures; the unfixed count is recorded, not
// asserted -- if it is 0, the ledger says "risk removed", not "cause found".
func TestVSSStressThreadMigration(t *testing.T) {
	mode := os.Getenv("NIMBUS_VSS_STRESS")
	if mode != "fixed" && mode != "unfixed" {
		t.Skip("manual test: set NIMBUS_VSS_STRESS=fixed or unfixed (see the comment)")
	}
	cycles := 50
	if v, err := strconv.Atoi(os.Getenv("NIMBUS_VSS_STRESS_CYCLES")); err == nil && v > 0 {
		cycles = v
	}
	vol := os.Getenv("SystemDrive")
	if vol == "" {
		vol = "C:"
	}
	vol += "\\"

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4*runtime.GOMAXPROCS(0); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					runtime.Gosched()
				}
			}
		}()
	}

	type failure struct {
		Cycle   int    `json:"cycle"`
		Summary string `json:"summary"`
	}
	var failures []failure
	start := time.Now()
	for c := 1; c <= cycles; c++ {
		var err error
		if mode == "unfixed" {
			sn := vss.Snapshotter{}
			_, err = sn.CreateSnapshot(vol, false, 180)
			if err == nil {
				err = sn.Release()
			}
		} else {
			err = CreateVSSSnapshot([]string{vol}, func(map[string]SnapShot) error { return nil })
		}
		if err != nil {
			failures = append(failures, failure{Cycle: c, Summary: DescribeVSSError(err).Summary()})
			t.Logf("cycle %d: %s", c, DescribeVSSError(err).Summary())
		}
	}
	close(stop)
	wg.Wait()

	result := map[string]any{"mode": mode, "cycles": cycles, "failures": len(failures), "detail": failures,
		"gomaxprocs": runtime.GOMAXPROCS(0), "seconds": int(time.Since(start).Seconds()),
		"finished_utc": time.Now().UTC().Format(time.RFC3339)}
	js, _ := json.MarshalIndent(result, "", "  ")
	t.Logf("%s", js)
	if out := os.Getenv("NIMBUS_VSS_STRESS_OUT"); out != "" {
		if err := os.WriteFile(out, js, 0o600); err != nil {
			t.Errorf("writing %s: %v", out, err)
		}
	}
	if mode == "fixed" && len(failures) > 0 {
		t.Fatalf("the fixed path failed %d of %d cycles", len(failures), cycles)
	}
}
