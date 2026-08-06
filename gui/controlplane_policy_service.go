//go:build service
// +build service

package main

import (
	"fmt"
	"sync"
	"time"

	"controlplane"
)

// ControlPolicy is THE gate, evaluated where the agent actually lives.
//
// Service-only. The GUI has its own implementation that ASKS this process
// (controlplane_policy_gui.go), because a front end that evaluated the gate
// locally would find no agent and conclude that nothing is restricted.
//
// Original note: ControlPolicy is THE gate the GUI and local API consult. Fail-closed
// semantics are inherited from Agent.CurrentPolicy (zero value = all off);
// a standalone install (no control server configured) is intentionally
// ungoverned and gets everything enabled locally.
//
// A managed agent may additionally honour the local break-glass flag, but
// ONLY while the control plane is genuinely unreachable — see
// controlplane/breakglass.go. A reachable server that says no still means no.
func ControlPolicy() controlplane.Policy {
	if cpAgent == nil {
		return controlplane.Policy{FileRestore: true} // standalone: no MSP policy applies
	}
	p := cpAgent.CurrentPolicy()
	if !p.FileRestore && cpAgent.BreakGlassEligible(emergencyFileRestoreRequested(), 0) {
		noteBreakGlassUse()
		p.FileRestore = true
	}
	return p
}

var breakGlassLogMu sync.Mutex
var breakGlassLoggedAt time.Time

// noteBreakGlassUse records that the emergency override took effect. Throttled
// because ControlPolicy is consulted on every browse and restore call, but
// never silent: an override of an administrator's policy has to leave a trail
// someone can find afterwards.
func noteBreakGlassUse() {
	breakGlassLogMu.Lock()
	defer breakGlassLogMu.Unlock()
	if time.Since(breakGlassLoggedAt) < 5*time.Minute {
		return
	}
	breakGlassLoggedAt = time.Now()
	last := cpAgent.Status().LastSuccess
	when := "never"
	if !last.IsZero() {
		when = last.Format(time.RFC3339)
	}
	msg := fmt.Sprintf("[controlplane] BREAK-GLASS: local EmergencyFileRestore flag is enabling file restore "+
		"because the control server is unreachable (last successful check-in: %s). "+
		"Org policy has file restore DISABLED; clear the flag once the server is reachable again.", when)
	writeDebugLog(msg)
	writeBackupLog(msg)
}
