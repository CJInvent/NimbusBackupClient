package main

import (
	"fmt"
	"sync"

	"controlplane"
)

// Self-update and the line check, the parts both builds share
// (NimbusControl docs/V4-CLIENT-RELEASES.md §1.4 and §7).
//
// The check-in carries `agent_line` (this agent's L.CC against the server's)
// and, when the machine is behind its target, `update`. Both are level
// triggered: they arrive on every check-in. This file keeps the latest of
// each for the status card and the scheduler; the install itself is the
// service's (selfupdate_service.go), which sets startSelfUpdate. The GUI
// build hosting an agent never installs anything: it is not SYSTEM.

var (
	selfUpdateMu      sync.Mutex
	agentLineState    = "ok" // until a check-in says otherwise
	agentLineNeeds    string
	selfUpdatePending string // version being installed, "" when none

	// startSelfUpdate is installed by the service build. nil: offers are
	// recorded and shown, never acted on.
	startSelfUpdate func(controlplane.UpdateOffer)
	// finishSelfUpdateAtStart reports the previous install, once the agent
	// has its identity. Service build only, like startSelfUpdate.
	finishSelfUpdateAtStart func(*controlplane.Client)
)

// applyUpdateFromCheckin is the OnUpdate hook. The line state logs on change
// only, like every level-triggered hook in controlplane_glue.go.
func applyUpdateFromCheckin(offer *controlplane.UpdateOffer, line *controlplane.AgentLine) {
	state, needs := "ok", ""
	if !line.Compatible() {
		state, needs = line.State, line.Needs
	}
	selfUpdateMu.Lock()
	changed := state != agentLineState || needs != agentLineNeeds
	agentLineState, agentLineNeeds = state, needs
	selfUpdateMu.Unlock()
	if changed {
		if state == "ok" {
			writeInfoLog("[ControlPlane] This agent is on the server's release line again: scheduled backups resume")
		} else {
			writeWarnLog("[ControlPlane] " + agentLineMessage(state, needs))
		}
	}
	if offer != nil && startSelfUpdate != nil {
		startSelfUpdate(*offer)
	}
}

func agentLineMessage(state, needs string) string {
	if state == "newer" {
		return fmt.Sprintf("This agent is newer than its control server, which must be updated to %s: scheduled backups do not run until then", needs)
	}
	return fmt.Sprintf("This agent must be updated to %s for its control server: scheduled backups do not run until then", needs)
}

// scheduledRunOffLine says whether a scheduled run must be skipped because the
// server would refuse it (§1.4). A run somebody asked for is never queued by
// a server that refuses this agent, so only the scheduler is gated.
func scheduledRunOffLine(requestID string) (bool, string) {
	if requestID != "" {
		return false, ""
	}
	selfUpdateMu.Lock()
	defer selfUpdateMu.Unlock()
	if agentLineState == "ok" {
		return false, ""
	}
	return true, agentLineMessage(agentLineState, agentLineNeeds)
}

// selfUpdateStatus is what the status card shows: the line state and the
// version being installed.
func selfUpdateStatus() map[string]interface{} {
	selfUpdateMu.Lock()
	defer selfUpdateMu.Unlock()
	return map[string]interface{}{
		"agent_line":       agentLineState,
		"agent_line_needs": agentLineNeeds,
		"update_pending":   selfUpdatePending,
	}
}
