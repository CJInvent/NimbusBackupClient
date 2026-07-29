package main

// controlplane_pbspoll.go — independent, server-scheduled PBS connectivity
// polling.
//
// Decoupled from the check-in loop's own ~120s cadence specifically so a
// live PBS network round-trip does not run on every check-in across a
// whole fleet — that was the ACTUAL PRE-EXISTING behavior this file
// replaces: cpCheckPBSReachability (controlplane_glue.go, unchanged) has
// always been the function that performs the check, but check-in used to
// call it live, every cycle. This file adds a schedule between them: an
// independent loop calls it on a server-assigned interval (default 1800s
// / 30 min) and caches the result; check-in now reads that cache instead
// of triggering a fresh PBS network call itself.
//
// This is NOT the manual "Test" button in the PBS settings UI
// (App.TestPBSConnection -> App.TestConnection in main.go). That remains
// a separate, heavier, on-demand check (full auth + datastore access, not
// just the lightweight reachability ping cpCheckPBSReachability performs)
// for validating a just-edited configuration, and is unchanged by this
// file. The two exist for different reasons: the button answers "is what
// I just typed correct, right now", this loop answers "is my configured
// PBS still reachable, as of a recent-ish background check".
//
// Scheduling itself is NOT reimplemented here: see controlplane/schedule.go's
// NextAligned, the SAME epoch-aligned-grid primitive the check-in loop
// uses, and NimbusControl's Nimbus\Agents\PollSchedule for how the server
// computes each agent's offset (also shared with check-in's own offset —
// one function, two callers, server-side too).

import (
	"controlplane"
	"fmt"
	"sync"
	"time"
)

var (
	// pbsPollMu guards the schedule (interval/offset), written rarely --
	// at most once per check-in response, via updatePBSPollSchedule.
	pbsPollMu       sync.Mutex
	pbsPollStop     chan struct{}
	pbsPollInterval = 1800 // seconds; contract default until a check-in response says otherwise
	pbsPollOffset   = 0

	// pbsResultMu guards the cached result, a SEPARATE lock from pbsPollMu:
	// the schedule and the result are written at different rates (once per
	// check-in vs. once per poll) and read from different places (the poll
	// loop vs. cpBuildInventory), so there's no reason to serialize them
	// against each other.
	pbsResultMu      sync.Mutex
	pbsLastResult    *bool
	pbsLastCheckedAt time.Time
)

// startPBSPoller launches the independent PBS-connectivity poll loop.
// Called once from StartControlPlane, alongside cpAgent.Run — this loop
// only matters when there's a control server to report to, so it shares
// that function's early-return when none is configured.
func (a *App) startPBSPoller() {
	pbsPollMu.Lock()
	pbsPollStop = make(chan struct{})
	stop := pbsPollStop
	pbsPollMu.Unlock()
	go a.runPBSPollLoop(stop)
}

// stopPBSPoller halts the poll loop. Called from StopControlPlane,
// mirroring cpStop's own close-and-nil pattern exactly.
func (a *App) stopPBSPoller() {
	pbsPollMu.Lock()
	defer pbsPollMu.Unlock()
	if pbsPollStop != nil {
		close(pbsPollStop)
		pbsPollStop = nil
	}
}

// updatePBSPollSchedule applies a server-assigned interval/offset. Wired
// as controlplane.Agent.OnPBSPollSchedule (controlplane_glue.go), so it
// fires on every check-in exactly like the existing OnPolicy callback
// does. A too-small interval from a misbehaving or very old server is
// refused in favor of keeping the previous value — same floor-not-trust
// pattern CheckinNow already applies to CheckinSeconds.
func updatePBSPollSchedule(intervalSeconds, offsetSeconds int) {
	pbsPollMu.Lock()
	defer pbsPollMu.Unlock()
	if intervalSeconds >= 60 { // refuse absurd/zero values; floor at 60s
		pbsPollInterval = intervalSeconds
	}
	pbsPollOffset = offsetSeconds
}

func currentPBSPollSchedule() (interval, offset int) {
	pbsPollMu.Lock()
	defer pbsPollMu.Unlock()
	return pbsPollInterval, pbsPollOffset
}

// runPBSPollLoop is the epoch-aligned analogue of controlplane.Agent.Run,
// reusing the exact same NextAligned primitive so "wait for my next
// scheduled turn" is implemented in exactly one place in this codebase.
//
// Unlike check-in, this loop does NOT fire immediately on start. An agent
// that just (re)started already has last cycle's cached result (or none
// yet, which check-in already reports as "no reading this cycle" — see
// PBSReachable's own doc comment in controlplane/types.go, unchanged).
// Firing immediately on every process start would reintroduce, for PBS
// specifically, the exact restart-synchronization risk this whole design
// exists to avoid: a fleet that reboots together (a mass Windows Update,
// for instance) would otherwise all hit PBS in the same instant regardless
// of how well-distributed their assigned offsets are. The tradeoff is a
// freshly configured agent can show "unknown" PBS status for up to one
// interval — the manual Test button (App.TestPBSConnection) remains the
// right tool for "show me it's working right now" immediately after
// editing PBS settings; this loop's job is steady-state background
// reporting, not first-configuration feedback.
func (a *App) runPBSPollLoop(stop <-chan struct{}) {
	for {
		interval, offset := currentPBSPollSchedule()
		wait := controlplane.NextAligned(time.Now(), interval, offset)
		select {
		case <-stop:
			return
		case <-time.After(wait):
		}
		result := cpCheckPBSReachability(a.config)
		checkedAt := time.Now()
		pbsResultMu.Lock()
		pbsLastResult = result
		pbsLastCheckedAt = checkedAt
		pbsResultMu.Unlock()
		writeDebugLog(fmt.Sprintf("[pbspoll] scheduled PBS connectivity check at %s: reachable=%v",
			checkedAt.Format(time.RFC3339), result))
	}
}

// cachedPBSReachable returns the most recent scheduled poll's result, for
// cpBuildInventory to report at check-in WITHOUT performing a live PBS
// call itself — see this file's own top comment for why that call moved
// here. nil means "no reading yet": a fresh install with no PBS configured
// yet, or one whose first scheduled poll (up to pbsPollInterval away, see
// runPBSPollLoop) simply hasn't fired. Same semantics PBSReachable's own
// doc comment already establishes server-side.
func cachedPBSReachable() *bool {
	pbsResultMu.Lock()
	defer pbsResultMu.Unlock()
	return pbsLastResult
}

// cachedPBSCheckedAt returns when the cached result was produced (zero
// time if no scheduled poll has run yet) — surfaced in
// ControlPlaneStatusMap so the GUI can show "PBS last checked N ago"
// without exposing the poll internals themselves.
func cachedPBSCheckedAt() time.Time {
	pbsResultMu.Lock()
	defer pbsResultMu.Unlock()
	return pbsLastCheckedAt
}
