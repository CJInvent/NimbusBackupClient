package controlplane

import (
	"testing"
	"time"
)

func TestNextAlignedBasicMath(t *testing.T) {
	// unix 100, interval 50, offset 30: ticks are ...,-20,30,80,130,180...
	// 80 < 100, so the next one strictly after 100 is 130 -> wait 30s.
	now := time.Unix(100, 0).UTC()
	got := NextAligned(now, 50, 30)
	if got != 30*time.Second {
		t.Fatalf("want 30s, got %v", got)
	}
}

func TestNextAlignedExactlyOnATick(t *testing.T) {
	// Sitting exactly on a tick must wait a FULL cycle, not return 0 --
	// otherwise a caller that fires, then immediately asks again, busy-loops.
	now := time.Unix(80, 0).UTC() // 80 % 50 == 30, an exact tick
	got := NextAligned(now, 50, 30)
	if got != 50*time.Second {
		t.Fatalf("want a full 50s cycle when already on a tick, got %v", got)
	}
}

func TestNextAlignedZeroOffset(t *testing.T) {
	now := time.Unix(1234, 0).UTC()
	got := NextAligned(now, 100, 0)
	// next multiple of 100 after 1234 is 1300 -> 66s
	if got != 66*time.Second {
		t.Fatalf("want 66s, got %v", got)
	}
}

func TestNextAlignedOffsetLargerThanInterval(t *testing.T) {
	// offset is taken mod interval, so 130 with interval 50 behaves like 30.
	now := time.Unix(100, 0).UTC()
	got := NextAligned(now, 50, 130)
	if got != 30*time.Second {
		t.Fatalf("want 30s (offset normalized mod interval), got %v", got)
	}
}

func TestNextAlignedZeroInterval(t *testing.T) {
	// A misconfigured/zero interval must never divide by zero or hang --
	// floor at 1s rather than panic.
	now := time.Unix(100, 0).UTC()
	got := NextAligned(now, 0, 0)
	if got <= 0 || got > time.Second {
		t.Fatalf("want a small positive duration for a zero interval, got %v", got)
	}
}

func TestNextAlignedSameOffsetAndIntervalProduceSameGridRegardlessOfNow(t *testing.T) {
	// The whole point: two calls with the SAME (interval, offset) but
	// DIFFERENT "now" values must land on the exact same absolute tick
	// times -- this is what makes a restart resume the existing grid
	// instead of starting a new one.
	interval, offset := 1800, 743
	t1 := time.Unix(1_800_000_000, 0).UTC()
	t2 := t1.Add(37 * time.Second) // simulates a process restarting 37s later

	next1 := t1.Add(NextAligned(t1, interval, offset)).Unix()
	next2 := t2.Add(NextAligned(t2, interval, offset)).Unix()

	// next2 must be either the SAME tick as next1 (if t2 is still before
	// it) or exactly one interval later -- never an arbitrary offset from
	// t2's own restart moment.
	if next2 != next1 && next2 != next1+int64(interval) {
		t.Fatalf("restart broke grid alignment: next1=%d next2=%d interval=%d", next1, next2, interval)
	}
}

func TestNextAlignedManyAgentsSpreadAcrossInterval(t *testing.T) {
	// Sanity check on the INTENDED OUTCOME, not just the math: offsets 0,
	// 200, 400, ... 1600 across a 1800s interval should produce next-tick
	// times spread roughly evenly, not clustered.
	now := time.Unix(1_800_000_000, 0).UTC()
	interval := 1800
	seen := map[time.Duration]bool{}
	for offset := 0; offset < interval; offset += 200 {
		d := NextAligned(now, interval, offset)
		if d < 0 || d > time.Duration(interval)*time.Second {
			t.Fatalf("offset %d produced out-of-range wait %v", offset, d)
		}
		seen[d] = true
	}
	if len(seen) < 5 {
		t.Fatalf("expected agents at different offsets to get meaningfully different wait times, got %d distinct values", len(seen))
	}
}
