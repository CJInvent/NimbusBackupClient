package controlplane

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

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

	policy   atomic.Value // Policy
	interval atomic.Int64 // seconds, server-driven
	mu       sync.Mutex   // serializes forced check-ins with the loop
}

// CurrentPolicy returns the last policy the server delivered. Before the
// first successful check-in it returns the SAFE defaults (everything off) —
// fail closed, never open.
func (a *Agent) CurrentPolicy() Policy {
	if p, ok := a.policy.Load().(Policy); ok {
		return p
	}
	return Policy{} // zero value: FileRestore=false — deny by default
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
