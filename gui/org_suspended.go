package main

import (
	"fmt"
	"sync/atomic"
)

// The organization is suspended in the portal (NimbusControl F-22; CJ's
// ruling 2026-09-27). The server has disabled the org's PBS tokens, which is
// the real control; this side only stops the machine from starting SCHEDULED
// runs that PBS would refuse, and says why. A run somebody asked for (a
// run_backup from the portal, a manual run) is still attempted: PBS refuses
// it, and the person who asked sees that.
//
// In memory only: after a service restart the flag is false until the first
// check-in (about two minutes). A scheduled run in that window reaches PBS and
// is refused there, which is the control working, not a hole in it.
var orgSuspended atomic.Bool

const orgSuspendedMessage = "Backups suspended by your provider: scheduled backups do not run until your organization is reactivated"

// applyOrgSuspended is the check-in hook. It logs on CHANGE only: one WARN
// when the suspension starts, one INFO when it ends.
func applyOrgSuspended(suspended bool) {
	if orgSuspended.Swap(suspended) == suspended {
		return
	}
	if suspended {
		writeWarnLog("[ControlPlane] " + orgSuspendedMessage)
	} else {
		writeInfoLog("[ControlPlane] Organization reactivated by your provider: scheduled backups resume")
	}
}

// scheduledRunSuspended says whether a run must be skipped: the scheduler
// started it (no request id) and the organization is suspended.
func scheduledRunSuspended(requestID string) bool {
	return requestID == "" && orgSuspended.Load()
}

// skippedForSuspension is the one line a skipped scheduled run leaves.
func skippedForSuspension(jobName string) string {
	return fmt.Sprintf("Job %s not started: backups suspended by your provider", jobName)
}
