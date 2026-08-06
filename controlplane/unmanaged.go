package controlplane

import "time"

// Unmanaged backups: work the MACHINE authored — its local scheduler's jobs,
// and anything started at the console — as opposed to work this control plane
// sent it.
//
// The gate exists because the client GUI no longer contains a backup engine
// (client docs/V4-PIPELINE.md §3.1). Execution collapsed into the service, so
// for the first time there is a single place that can ask whether locally
// authored work should run at all. `restrict_unmanaged_backups` is the org
// policy that answers it.
//
// THE DEFAULT IS PERMISSIVE, and that is the opposite of the fail-closed
// default used for file restore. It is deliberate on both ends (see the key's
// note in NimbusControl Core\Policy). For restore, a wrong default means
// somebody reads a file they should not. Here it means machines silently stop
// backing up — the failure the product exists to prevent — and an agent that
// has never reached the server would refuse to protect anything. So a Policy
// zero value permits, and only an explicit `true` from a server restricts.
//
// The local override mirrors break-glass exactly, because it is the same
// problem: an org may legitimately restrict a site to managed jobs only, and
// that site may then need a technician to take a backup at the moment the
// control server is also unreachable — same outage, same dead WAN link. And
// as with break-glass: being set locally is necessary but NOT sufficient. A
// reachable server that says restrict still means restrict. The flag lives
// under HKLM so setting it requires Administrator, and every use is logged.

// UnmanagedOverrideMinOutage is how long the control plane must have been
// unreachable before the local override is honoured.
//
// Shorter than BreakGlassMinOutage on purpose. That floor guards a capability
// that reads data out of backups, where waiting is an inconvenience; this one
// guards taking a backup, where waiting is the window in which the data being
// protected can be lost. Still several consecutive missed check-ins at the
// default 120 s cadence.
const UnmanagedOverrideMinOutage = 5 * time.Minute

// UnmanagedBackupsAllowed decides whether a locally authored backup may run.
//
// managed is whether this agent has a control plane at all: an install with no
// control server configured is ungoverned by design (the project ships and is
// supported that way), so nothing restricts it.
//
// restricted is the org policy as last delivered. overrideRequested is the
// local Administrator flag. lastSuccess is the last completed check-in; a zero
// value means this agent has never reached the server, which counts as an
// outage — a machine restored into a dead network is exactly the case the
// override exists for.
//
// The second return value reports whether the answer came from the override
// rather than from policy, so the caller can log it. A silent override of an
// administrator's decision is not acceptable; an audited one is.
//
// Pure and time-injected so the decision is testable without waiting for real
// clocks or standing up a server.
func UnmanagedBackupsAllowed(
	managed bool,
	restricted bool,
	overrideRequested bool,
	lastSuccess time.Time,
	minOutage time.Duration,
	now time.Time,
) (allowed bool, viaOverride bool) {
	if !managed {
		return true, false // no control plane configured: ungoverned by design
	}
	if !restricted {
		return true, false // policy permits, which is also the default
	}
	if !overrideRequested {
		return false, false
	}
	if minOutage <= 0 {
		minOutage = UnmanagedOverrideMinOutage
	}
	// The server is reachable and says restrict. The override is inert —
	// this is the line that makes it an emergency override rather than an
	// override.
	if !lastSuccess.IsZero() && now.Sub(lastSuccess) < minOutage {
		return false, false
	}
	return true, true
}

// UnmanagedBackupsEligible applies UnmanagedBackupsAllowed to this agent's
// live state. Pass the locally-read override flag; the agent supplies the
// outage evidence, so a caller cannot claim an outage that is not happening.
func (a *Agent) UnmanagedBackupsEligible(overrideRequested bool, minOutage time.Duration) (bool, bool) {
	return UnmanagedBackupsAllowed(
		true,
		a.CurrentPolicy().RestrictUnmanagedBackups,
		overrideRequested,
		a.Status().LastSuccess,
		minOutage,
		time.Now(),
	)
}
