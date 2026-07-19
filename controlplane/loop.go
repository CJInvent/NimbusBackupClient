package controlplane

import (
	"log"
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
	mu       sync.Mutex   // serializes forced check-ins with the loop

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
func (a *Agent) Run(stop <-chan struct{}) {
	a.interval.Store(120) // contract default until the server says otherwise
	for {
		a.CheckinNow()
		select {
		case <-stop:
			return
		case <-time.After(time.Duration(a.interval.Load()) * time.Second):
		}
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

	// Policy is applied BEFORE commands run, so a command executes under
	// the policy that shipped alongside it.
	a.policy.Store(resp.Policy)
	a.policyAt.Store(time.Now())
	if a.OnPolicy != nil {
		a.OnPolicy(resp.Policy)
	}

	for _, cmd := range resp.Commands {
		res := CommandResult{OK: false, Result: map[string]interface{}{"error": "no command handler"}}
		if a.HandleCommand != nil {
			res = a.safeHandle(cmd)
		}
		if err := a.Client.PostCommandResult(cmd.ID, res); err != nil {
			log.Printf("[controlplane] command %d result post failed: %v", cmd.ID, err)
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
