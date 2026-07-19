package controlplane

import "time"

// Break-glass: a local, admin-only emergency override for the file-restore
// capability, honoured ONLY while the control plane is genuinely unreachable.
//
// The problem it solves: policy is delivered by the server, and a managed
// agent that cannot reach the server keeps whatever it was last told. If an
// org has file restore switched OFF, that is the correct and intended state —
// but a site in the middle of an incident may need a technician to pull files
// out of a backup at a moment when the control server is also unreachable
// (same outage, same ransomware event, same dead WAN link). Without an
// override the answer is "phone the MSP", and the MSP cannot help either
// because their console cannot reach the agent.
//
// What this is NOT: a way to ignore policy. If the control server is
// reachable and says no, the answer is no — the override is inert. It only
// applies during a real outage, it requires Administrator (the flag lives
// under HKLM), and every use is logged so the decision is auditable after the
// fact.
//
// The outage floor matters: without it, a single missed check-in would open
// the capability, which turns "emergency override" into "override". Several
// consecutive missed check-ins is the signal that the control plane is
// actually gone rather than briefly slow.

// BreakGlassMinOutage is how long the control plane must have been
// unreachable before a local override is honoured. The default check-in
// cadence is 120s, so this is several consecutive missed check-ins.
const BreakGlassMinOutage = 15 * time.Minute

// BreakGlassActive reports whether a local emergency override should be
// honoured right now.
//
// requested is the local flag (an administrator set it). lastSuccess is the
// last time this agent completed a check-in; a zero value means it has never
// reached the server at all, which is itself an outage — a machine restored
// into a dead network is exactly the case this exists for.
//
// Pure and time-injected so the policy decision is testable without waiting
// for real clocks or standing up a server.
func BreakGlassActive(requested bool, lastSuccess time.Time, minOutage time.Duration, now time.Time) bool {
	if !requested {
		return false
	}
	if minOutage <= 0 {
		minOutage = BreakGlassMinOutage
	}
	if lastSuccess.IsZero() {
		return true // never reached the control plane
	}
	return now.Sub(lastSuccess) >= minOutage
}

// BreakGlassEligible applies BreakGlassActive to this agent's live
// connectivity state. Pass the locally-read override flag; the agent supplies
// the outage evidence, so a caller cannot claim an outage that is not
// happening.
func (a *Agent) BreakGlassEligible(requested bool, minOutage time.Duration) bool {
	if !requested {
		return false
	}
	return BreakGlassActive(true, a.Status().LastSuccess, minOutage, time.Now())
}
