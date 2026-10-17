package main

import (
	"strings"
	"testing"

	"controlplane"
)

func resetSelfUpdateState() {
	selfUpdateMu.Lock()
	agentLineState, agentLineNeeds, selfUpdatePending = "ok", "", ""
	selfUpdateMu.Unlock()
}

func TestAnAgentOffTheLineSkipsOnlyScheduledRuns(t *testing.T) {
	resetSelfUpdateState()
	defer resetSelfUpdateState()

	if off, _ := scheduledRunOffLine(""); off {
		t.Fatal("a fresh agent must run its schedule")
	}
	applyUpdateFromCheckin(nil, &controlplane.AgentLine{State: "older", Needs: "1.02"})
	off, why := scheduledRunOffLine("")
	if !off || !strings.Contains(why, "1.02") {
		t.Fatalf("an older agent must skip scheduled runs and say what it needs: %v %q", off, why)
	}
	if off, _ := scheduledRunOffLine("req-1"); off {
		t.Fatal("a run somebody asked for is never gated here")
	}
	st := selfUpdateStatus()
	if st["agent_line"] != "older" || st["agent_line_needs"] != "1.02" {
		t.Fatalf("status: %v", st)
	}

	// A server too old to send agent_line counts as compatible.
	applyUpdateFromCheckin(nil, nil)
	if off, _ := scheduledRunOffLine(""); off {
		t.Fatal("no agent_line means compatible")
	}
}

func TestANewerAgentNamesTheServerVersion(t *testing.T) {
	if m := agentLineMessage("newer", "1.03"); !strings.Contains(m, "server") || !strings.Contains(m, "1.03") {
		t.Fatalf("message: %q", m)
	}
}

func TestAnOfferIsHandedToTheInstallerWhenOneIsInstalled(t *testing.T) {
	resetSelfUpdateState()
	defer resetSelfUpdateState()
	prev := startSelfUpdate
	defer func() { startSelfUpdate = prev }()

	var got []string
	startSelfUpdate = func(o controlplane.UpdateOffer) { got = append(got, o.Version) }
	applyUpdateFromCheckin(&controlplane.UpdateOffer{Version: "1.01.04"}, &controlplane.AgentLine{State: "ok"})
	applyUpdateFromCheckin(nil, &controlplane.AgentLine{State: "ok"})
	if len(got) != 1 || got[0] != "1.01.04" {
		t.Fatalf("offers handed over: %v", got)
	}
}
