package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBreakGlassRequiresBothFlagAndOutage(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-2 * time.Minute) // one missed check-in at most
	stale := now.Add(-30 * time.Minute) // several consecutive misses

	cases := []struct {
		name        string
		requested   bool
		lastSuccess time.Time
		want        bool
	}{
		// The property that makes this an emergency measure rather than an
		// override: with the control plane answering, the flag does nothing.
		{"flag set, server reachable", true, recent, false},
		{"flag set, server gone", true, stale, true},
		{"flag set, never reached server", true, time.Time{}, true},

		// No flag means no override, outage or not. An outage on its own must
		// never widen a capability.
		{"no flag, server reachable", false, recent, false},
		{"no flag, server gone", false, stale, false},
		{"no flag, never reached server", false, time.Time{}, false},
	}
	for _, c := range cases {
		got := BreakGlassActive(c.requested, c.lastSuccess, BreakGlassMinOutage, now)
		if got != c.want {
			t.Errorf("%s: BreakGlassActive = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBreakGlassOutageBoundary(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

	justUnder := now.Add(-BreakGlassMinOutage + time.Second)
	if BreakGlassActive(true, justUnder, BreakGlassMinOutage, now) {
		t.Error("override fired before the outage floor — a brief blip must not open the capability")
	}
	exactly := now.Add(-BreakGlassMinOutage)
	if !BreakGlassActive(true, exactly, BreakGlassMinOutage, now) {
		t.Error("override did not fire at the outage floor")
	}

	// A zero/negative floor falls back to the default rather than meaning
	// "no floor at all", so a caller passing 0 cannot accidentally turn this
	// into an unconditional override.
	if BreakGlassActive(true, justUnder, 0, now) {
		t.Error("minOutage=0 was treated as no floor")
	}
}

// The agent supplies the outage evidence from its own connectivity record, so
// a caller cannot assert an outage that is not happening.
func TestBreakGlassEligibleUsesLiveAgentState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(CheckinResponse{
			CheckinSeconds: 120,
			Policy:         Policy{FileRestore: false}, // org says no
		})
	}))
	defer srv.Close()

	a := &Agent{Client: &Client{BaseURL: srv.URL, AgentID: 1, Secret: "s"}}

	// Before any contact: an outage by definition, so the override applies.
	if !a.BreakGlassEligible(true, BreakGlassMinOutage) {
		t.Error("override should apply before the agent has ever reached the server")
	}

	// After a successful check-in the server is demonstrably reachable, and
	// its answer (file restore off) stands.
	a.CheckinNow()
	if a.BreakGlassEligible(true, BreakGlassMinOutage) {
		t.Error("override applied while the control server was reachable — that is a policy bypass, not a break-glass")
	}
	if a.CurrentPolicy().FileRestore {
		t.Error("policy was widened by the override rather than by the server")
	}

	// And without the local flag, nothing changes either way.
	if a.BreakGlassEligible(false, BreakGlassMinOutage) {
		t.Error("override applied without the local flag")
	}
}
