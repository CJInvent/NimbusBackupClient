package controlplane

import (
	"testing"
	"time"
)

// UnmanagedBackupsAllowed — the decision that lets an org restrict a machine
// to control-plane-sent work, and lets an administrator take a backup anyway
// during a genuine outage.
//
// Every case below is a sentence about the product, not a truth table filled
// in for coverage. The two that matter most are the polarity of the default
// (a machine that has never reached the server must still back up) and the
// inertness of the override while the server is answering (otherwise it is
// not an emergency override, it is an override).

func TestUnmanagedBackupsAllowed(t *testing.T) {
	now := time.Date(2026, 8, 6, 3, 0, 0, 0, time.UTC)
	recent := now.Add(-30 * time.Second) // check-in a moment ago
	stale := now.Add(-2 * time.Hour)     // long outage
	var never time.Time                  // has never reached the server

	cases := []struct {
		name        string
		managed     bool
		restricted  bool
		override    bool
		lastSuccess time.Time
		wantAllowed bool
		wantVia     bool
	}{
		{
			name:        "no control plane configured is ungoverned by design",
			managed:     false,
			restricted:  true, // cannot happen, but must not matter
			override:    false,
			lastSuccess: never,
			wantAllowed: true,
		},
		{
			name:        "policy permits, which is also what an unset policy means",
			managed:     true,
			restricted:  false,
			lastSuccess: recent,
			wantAllowed: true,
		},
		{
			name:        "a machine that has NEVER checked in still backs up",
			managed:     true,
			restricted:  false, // the Policy zero value
			lastSuccess: never,
			wantAllowed: true,
		},
		{
			name:        "policy restricts and no override: refused",
			managed:     true,
			restricted:  true,
			override:    false,
			lastSuccess: recent,
			wantAllowed: false,
		},
		{
			name:        "a REACHABLE server saying restrict makes the override inert",
			managed:     true,
			restricted:  true,
			override:    true,
			lastSuccess: recent,
			wantAllowed: false,
		},
		{
			name:        "override during a real outage: allowed, and reported as an override",
			managed:     true,
			restricted:  true,
			override:    true,
			lastSuccess: stale,
			wantAllowed: true,
			wantVia:     true,
		},
		{
			name:        "restricted, never reached the server, override set: allowed",
			managed:     true,
			restricted:  true,
			override:    true,
			lastSuccess: never,
			wantAllowed: true,
			wantVia:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowed, via := UnmanagedBackupsAllowed(
				tc.managed, tc.restricted, tc.override, tc.lastSuccess, 0, now)
			if allowed != tc.wantAllowed {
				t.Errorf("allowed = %v, want %v", allowed, tc.wantAllowed)
			}
			if via != tc.wantVia {
				t.Errorf("viaOverride = %v, want %v", via, tc.wantVia)
			}
		})
	}
}

func TestUnmanagedOverrideNeedsASustainedOutage(t *testing.T) {
	now := time.Date(2026, 8, 6, 3, 0, 0, 0, time.UTC)

	// One missed check-in is not an outage. Without a floor, a server that
	// blinks turns "emergency override" into "override".
	justUnder := now.Add(-UnmanagedOverrideMinOutage + time.Second)
	if allowed, _ := UnmanagedBackupsAllowed(true, true, true, justUnder, 0, now); allowed {
		t.Error("the override fired one second short of the outage floor")
	}

	atFloor := now.Add(-UnmanagedOverrideMinOutage)
	if allowed, via := UnmanagedBackupsAllowed(true, true, true, atFloor, 0, now); !allowed || !via {
		t.Errorf("at the floor: allowed=%v via=%v, want true/true", allowed, via)
	}
}

func TestUnmanagedOverrideFloorIsShorterThanBreakGlass(t *testing.T) {
	// Deliberate, and the reason is worth pinning so nobody "tidies" them
	// into one constant: break-glass guards reading data OUT of backups,
	// where waiting is an inconvenience. This guards TAKING a backup, where
	// waiting is the window in which the data can be lost.
	if UnmanagedOverrideMinOutage >= BreakGlassMinOutage {
		t.Errorf("UnmanagedOverrideMinOutage (%v) should be shorter than BreakGlassMinOutage (%v)",
			UnmanagedOverrideMinOutage, BreakGlassMinOutage)
	}
}

func TestUnmanagedOverrideDefaultsItsFloor(t *testing.T) {
	now := time.Date(2026, 8, 6, 3, 0, 0, 0, time.UTC)
	last := now.Add(-UnmanagedOverrideMinOutage - time.Minute)

	// A caller passing 0 must get the constant, not "no floor at all" — which
	// would let the override fire against a server that answered a second ago.
	if allowed, _ := UnmanagedBackupsAllowed(true, true, true, now.Add(-time.Second), 0, now); allowed {
		t.Error("minOutage=0 was treated as no floor")
	}
	if allowed, _ := UnmanagedBackupsAllowed(true, true, true, last, 0, now); !allowed {
		t.Error("minOutage=0 did not apply the default floor")
	}
}

func TestPolicyZeroValuePermitsUnmanagedBackups(t *testing.T) {
	// The polarity check, stated as its own test because it is the one thing
	// here that contradicts the convention used by every other capability:
	// Policy's zero value denies file restore and PERMITS backups.
	var p Policy
	if p.FileRestore {
		t.Error("Policy zero value grants file restore; it must fail closed")
	}
	if p.RestrictUnmanagedBackups {
		t.Error("Policy zero value restricts backups; a machine that cannot " +
			"reach the server must still protect its data")
	}
}
