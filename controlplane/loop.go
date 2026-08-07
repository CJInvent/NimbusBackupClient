package controlplane

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Status is a point-in-time snapshot of control-plane connectivity for
// display surfaces (GUI status card, local API). Connected means the most
// recent check-in attempt succeeded.
type Status struct {
	Connected     bool      `json:"connected"`
	LastAttempt   time.Time `json:"last_attempt"`
	LastSuccess   time.Time `json:"last_success"`
	LastError     string    `json:"last_error,omitempty"`
	CheckinPeriod int       `json:"checkin_seconds"`
	Policy        Policy    `json:"policy"`
}

// Agent runs the check-in loop and owns the current Policy. It is the only
// long-lived control-plane object; construct one in the service and keep it
// for the process lifetime.
//
//	agent := &controlplane.Agent{
//	    Client:         client,                  // enrolled Client
//	    BuildInventory: buildInventoryFromJobs,  // called each check-in
//	    HandleCommand:  dispatchCommand,         // idempotent!
//	    OnPolicy:       applyPolicy,             // optional push notification
//	    OnPBSPollSchedule: applyPBSPollSchedule, // optional push notification
//	}
//	go agent.Run(stopCh)
type Agent struct {
	Client *Client

	// BuildInventory produces the current job list every cycle so the
	// server's missed-backup expectations always track reality.
	BuildInventory func() Inventory

	// HandleCommand executes one server command and returns its result.
	// MUST be idempotent — see Command docs. Runs on the loop goroutine;
	// long work (run_backup) should dispatch and return promptly.
	HandleCommand func(Command) CommandResult

	// OnPolicy is invoked whenever a check-in delivers the policy set
	// (i.e. every cycle). Optional; CurrentPolicy() is always available.
	OnPolicy func(Policy)

	// OnPBSPollSchedule is invoked whenever a check-in delivers a PBS-poll
	// interval/offset (i.e. every cycle) — the GUI's independent PBS
	// poller (gui/controlplane_pbspoll.go) wires this up to stay aligned
	// with the server-assigned schedule without the check-in loop itself
	// knowing anything about PBS. Same "push notification" shape as
	// OnPolicy, same reason: whoever owns the reacting behavior should own
	// the callback, not this loop.
	OnPBSPollSchedule func(intervalSeconds, offsetSeconds int)

	// OnManagedJobs is invoked whenever a check-in delivers the
	// server-defined job set (i.e. every cycle). Same push shape as
	// OnPolicy, same reason: whoever owns the reacting behaviour owns the
	// callback, not this loop.
	//
	// The slice is the COMPLETE set. A handler must replace what it holds,
	// not merge — deletion has no representation other than absence.
	OnManagedJobs func([]ManagedJob)

	AgentVersion string

	// PolicyMaxAge optionally bounds how long a delivered policy stays in
	// force without confirmation. Zero (the default) keeps the historical
	// behavior: the last policy the server delivered stays in force
	// indefinitely while the server is unreachable.
	//
	// That default is fail-closed only at STARTUP. Once a capability has been
	// granted, an agent that can no longer reach the control server keeps it
	// forever — so an attacker who blocks egress to the control plane (trivial
	// from the same LAN) freezes the policy at whatever was last granted, and
	// a revocation issued afterwards never lands. Setting PolicyMaxAge makes
	// the grant expire instead, at the cost of losing server-granted
	// capabilities during a genuine outage. That is a real availability
	// tradeoff, which is why it is a knob and not a hardcoded timeout.
	PolicyMaxAge time.Duration

	policy   atomic.Value // Policy
	policyAt atomic.Value // time.Time — when policy was last confirmed
	interval atomic.Int64 // seconds, server-driven
	// checkinOffset is this agent's assigned slot in the check-in grid
	// (see NextAligned). Defaults to 0 (aligned to the epoch itself) until
	// the first check-in response assigns a real value — an agent that has
	// never talked to the server has nothing to stagger against yet.
	checkinOffset atomic.Int64
	mu            sync.Mutex // serializes forced check-ins with the loop

	statusMu sync.Mutex
	status   Status
}

// Status returns the latest connectivity snapshot (safe from any goroutine).
func (a *Agent) Status() Status {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	st := a.status
	st.CheckinPeriod = int(a.interval.Load())
	st.Policy = a.CurrentPolicy()
	return st
}

func (a *Agent) recordAttempt(err error) {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	a.status.LastAttempt = time.Now()
	if err != nil {
		a.status.Connected = false
		a.status.LastError = err.Error()
		return
	}
	a.status.Connected = true
	a.status.LastError = ""
	a.status.LastSuccess = a.status.LastAttempt
}

// CurrentPolicy returns the policy currently in force.
//
// Before the first successful check-in it returns the SAFE defaults
// (everything off) — deny by default. If PolicyMaxAge is set and the last
// confirmation is older than that, it reverts to those same safe defaults
// rather than continuing to honor a grant the server has had no chance to
// revoke. With PolicyMaxAge unset, the last delivered policy stays in force
// for as long as the server is unreachable (see the field docs).
func (a *Agent) CurrentPolicy() Policy {
	p, ok := a.policy.Load().(Policy)
	if !ok {
		return Policy{} // zero value: FileRestore=false — deny by default
	}
	if a.PolicyMaxAge > 0 {
		at, ok := a.policyAt.Load().(time.Time)
		if !ok || time.Since(at) > a.PolicyMaxAge {
			return Policy{}
		}
	}
	return p
}

// PolicyIsStale reports whether the in-force policy has been downgraded to the
// safe defaults because it could not be reconfirmed. Surfaced so the UI can
// explain WHY a capability disappeared instead of looking broken.
func (a *Agent) PolicyIsStale() bool {
	if a.PolicyMaxAge <= 0 {
		return false
	}
	if _, ok := a.policy.Load().(Policy); !ok {
		return false
	}
	at, ok := a.policyAt.Load().(time.Time)
	return !ok || time.Since(at) > a.PolicyMaxAge
}

// Run blocks, checking in on the server-provided cadence until stop closes.
// Failures never kill the loop: the agent keeps working offline and the
// next successful check-in resynchronizes everything (policy, commands).
//
// The FIRST check-in fires immediately (unchanged from before this schedule
// existed) — a freshly (re)started agent should announce itself and pick up
// pending commands right away, not sit idle for up to a full interval.
// EVERY check-in after that waits on NextAligned rather than a flat
// interval-after-last-run sleep, so the loop self-corrects onto the
// server-assigned grid within one cycle even after an arbitrary-length
// restart delay — see schedule.go's doc comment for why this matters for a
// fleet that reboots together (e.g. after Windows Update).
func (a *Agent) Run(stop <-chan struct{}) {
	a.interval.Store(120) // contract default until the server says otherwise
	a.CheckinNow()
	for {
		wait := NextAligned(time.Now(), int(a.interval.Load()), int(a.checkinOffset.Load()))
		select {
		case <-stop:
			return
		case <-time.After(wait):
		}
		a.CheckinNow()
	}
}

// CheckinNow performs one check-in cycle (also callable out-of-band, e.g.
// right after a config change, without waiting for the ticker).
func (a *Agent) CheckinNow() {
	a.mu.Lock()
	defer a.mu.Unlock()

	req := CheckinRequest{AgentVersion: a.AgentVersion}
	if a.BuildInventory != nil {
		inv := a.BuildInventory()
		req.Inventory = &inv
	}

	resp, err := a.Client.Checkin(req)
	a.recordAttempt(err)
	if err != nil {
		log.Printf("[controlplane] check-in failed (will retry next cycle): %v", err)
		return
	}

	if resp.CheckinSeconds >= 30 { // refuse absurd values; floor at 30 s
		a.interval.Store(int64(resp.CheckinSeconds))
	}
	// Offset has no meaningful floor/ceiling of its own — NextAligned already
	// normalizes any value mod the current interval, so an offset larger
	// than (or equal to) the interval is harmless, not a value to reject.
	a.checkinOffset.Store(int64(resp.CheckinOffsetSeconds))

	// Policy is applied BEFORE commands run, so a command executes under
	// the policy that shipped alongside it.
	a.policy.Store(resp.Policy)
	a.policyAt.Store(time.Now())
	if a.OnPolicy != nil {
		a.OnPolicy(resp.Policy)
	}
	// Managed jobs land with policy, before commands, for the same reason:
	// a run_backup command naming a managed job must find that job already
	// applied rather than racing the check-in that delivered it.
	if a.OnManagedJobs != nil {
		a.OnManagedJobs(resp.ManagedJobs)
	}
	if a.OnPBSPollSchedule != nil {
		a.OnPBSPollSchedule(resp.PBSPollIntervalSeconds, resp.PBSPollOffsetSeconds)
	}

	if n := len(resp.Commands); n > 0 {
		// This log line is the one thing standing between "a command went
		// missing" and "nobody can tell whether it was ever received" — see
		// the incident this was added for: three run_backup commands sat at
		// 'sent' for 15+ hours with no trace anywhere that the client had
		// (or hadn't) seen them. Cheap and only fires when there's actually
		// something to report — silent on every empty check-in.
		ids := make([]string, 0, n)
		for _, c := range resp.Commands {
			ids = append(ids, fmt.Sprintf("%d:%s", c.ID, c.Command))
		}
		log.Printf("[controlplane] check-in delivered %d command(s): %s", n, strings.Join(ids, ", "))
	}

	for _, cmd := range resp.Commands {
		res := CommandResult{OK: false, Result: map[string]interface{}{"error": "no command handler"}}
		if a.HandleCommand != nil {
			res = a.safeHandle(cmd)
		}
		// Log the DISPATCH outcome (ok/fail + why) regardless of what
		// happens next. This is the log line that would have shown, in real
		// time, that a run_backup command was accepted and handed off —
		// distinct from and logged BEFORE the separate PostCommandResult
		// call below, so a process restart between the two still leaves a
		// trace of how far the command got.
		if res.OK {
			log.Printf("[controlplane] command %d (%s) dispatched OK", cmd.ID, cmd.Command)
		} else {
			log.Printf("[controlplane] command %d (%s) dispatch FAILED: %v", cmd.ID, cmd.Command, res.Result)
		}
		if err := a.Client.PostCommandResult(cmd.ID, res); err != nil {
			log.Printf("[controlplane] command %d result post failed: %v", cmd.ID, err)
		} else {
			log.Printf("[controlplane] command %d result posted (ok=%v)", cmd.ID, res.OK)
		}
	}
}

// safeHandle isolates handler panics — a bad command payload must not take
// down the check-in loop (or the service around it).
func (a *Agent) safeHandle(cmd Command) (res CommandResult) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[controlplane] command %d handler panicked: %v", cmd.ID, r)
			res = CommandResult{OK: false, Result: map[string]interface{}{"error": "handler panic"}}
		}
	}()
	return a.HandleCommand(cmd)
}
