package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Cross-repository wire-shape check for the audit signal.
//
// The key name and its position are transcribed BY HAND from NimbusControl
// docs/AGENT-API.md rather than derived from the struct, for the same reason
// the server side does it: a test that reads its expectations out of the code
// under test cannot notice the code drifting away from the contract.
func TestInventoryReportsBreakGlassOnTheWire(t *testing.T) {
	raw, err := json.Marshal(CheckinRequest{
		AgentVersion: "0.2.150",
		Inventory: &Inventory{
			Jobs:                  []InventoryJob{{Name: "Nightly", IntervalSeconds: 86400}},
			BreakGlassFileRestore: true,
		},
	})
	if err != nil {
		t.Fatalf("marshal check-in: %v", err)
	}

	var wire struct {
		Inventory map[string]any `json:"inventory"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got, present := wire.Inventory["break_glass_file_restore"]
	if !present {
		t.Fatalf("inventory does not carry break_glass_file_restore: %s", raw)
	}
	if got != true {
		t.Errorf("break_glass_file_restore = %v, want true", got)
	}

	// Always emitted, never omitempty: an absent key would be ambiguous
	// between "no override" and "an agent too old to report one", which
	// defeats the point of an audit signal.
	quiet, err := json.Marshal(&Inventory{Jobs: []InventoryJob{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(quiet), `"break_glass_file_restore":false`) {
		t.Errorf("a non-overriding agent omitted the field entirely: %s", quiet)
	}
}
