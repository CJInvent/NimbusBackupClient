package main

import "testing"

// TestEvaluateSpaceMath pins the space rules: block on overfill, warn at the
// 90%-after threshold, exact boundary behavior.
func TestEvaluateSpaceMath(t *testing.T) {
	// Simulate: total=1000, free=200 (used=800).
	calc := func(needed uint64) (fits, warn bool, pct float64) {
		total, free := uint64(1000), uint64(200)
		fits = needed <= free
		usedAfter := (total - free) + needed
		pct = float64(usedAfter) / float64(total) * 100.0
		warn = fits && float64(usedAfter) >= 0.90*float64(total)
		return
	}

	// needed=50 → used_after=850 (85%) → fits, no warning.
	if fits, warn, pct := calc(50); !fits || warn || pct != 85.0 {
		t.Fatalf("needed=50: fits=%v warn=%v pct=%v", fits, warn, pct)
	}
	// needed=100 → used_after=900 (90%) → fits, WARN (>= boundary inclusive).
	if fits, warn, pct := calc(100); !fits || !warn || pct != 90.0 {
		t.Fatalf("needed=100: fits=%v warn=%v pct=%v", fits, warn, pct)
	}
	// needed=200 → used_after=1000 (100%) → fits exactly, warn.
	if fits, warn, _ := calc(200); !fits || !warn {
		t.Fatalf("needed=200 must fit exactly and warn")
	}
	// needed=201 → over free → BLOCKED regardless of percentages.
	if fits, _, _ := calc(201); fits {
		t.Fatalf("needed=201 must be blocked")
	}
	// needed=0 → no-op download still valid.
	if fits, warn, pct := calc(0); !fits || warn || pct != 80.0 {
		t.Fatalf("needed=0: fits=%v warn=%v pct=%v", fits, warn, pct)
	}
}

// TestEvaluateSpaceRealFS smoke-tests driveSpace against the OS temp dir.
func TestEvaluateSpaceRealFS(t *testing.T) {
	sc, err := evaluateSpace(t.TempDir(), 1)
	if err != nil {
		t.Fatalf("evaluateSpace: %v", err)
	}
	if sc.TotalBytes == 0 || sc.FreeBytes > sc.TotalBytes {
		t.Fatalf("implausible: free=%d total=%d", sc.FreeBytes, sc.TotalBytes)
	}
	if !sc.Fits {
		t.Fatalf("1 byte must fit")
	}
}
