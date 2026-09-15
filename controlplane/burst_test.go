package controlplane

import (
	"testing"
	"time"
)

// The burst window — see CheckinResponse.BurstUntil and Agent.nextWait.
//
// What is pinned here is that the window ENDS BY ITSELF. A fast cadence that
// can only be cancelled by the server that set it is a machine polling every
// second forever the first time a portal crashes mid-browse.

func TestABurstWindowMakesTheNextCheckinSoon(t *testing.T) {
	a := &Agent{}
	a.interval.Store(120)
	a.burstSeconds.Store(2)
	now := time.Now()
	a.burstUntil.Store(now.Add(time.Minute).Unix())

	if got := a.nextWait(now); got != 2*time.Second {
		t.Fatalf("got %v, want 2s — a browse click must not cost a check-in cycle", got)
	}
}

func TestADeadlineInThePastRestoresTheNormalCadence(t *testing.T) {
	// No server contact anywhere in this test: that is the point. The agent
	// must leave the window on its own.
	a := &Agent{}
	a.interval.Store(120)
	a.burstSeconds.Store(1)
	now := time.Now()
	a.burstUntil.Store(now.Add(-time.Second).Unix())

	got := a.nextWait(now)
	if got <= 2*time.Second {
		t.Fatalf("got %v — an expired burst window kept the fast cadence", got)
	}
	if want := NextAligned(now, 120, 0); got != want {
		t.Fatalf("got %v, want the ordinary grid slot %v", got, want)
	}
}

func TestACadenceWithoutADeadlineIsRefused(t *testing.T) {
	// The one shape that must never be held: fast, with nothing to end it.
	// A response carrying only half of the pair clears the window rather
	// than guessing the other half.
	for _, resp := range []CheckinResponse{
		{CheckinSeconds: 120, BurstSeconds: 2},                                    // no deadline
		{CheckinSeconds: 120, BurstUntil: time.Now().Add(time.Minute).Unix()},     // no interval
		{CheckinSeconds: 120, BurstSeconds: 2, BurstUntil: time.Now().Unix() - 5}, // already over
	} {
		a := &Agent{}
		a.interval.Store(120)
		a.burstSeconds.Store(2)
		a.burstUntil.Store(time.Now().Add(time.Hour).Unix()) // a window in force

		a.applyBurst(&resp)

		if a.burstUntil.Load() != 0 {
			t.Fatalf("response %+v left a burst window in force", resp)
		}
	}
}
