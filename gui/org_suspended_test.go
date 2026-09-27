package main

import (
	"strings"
	"testing"
)

func TestOrgSuspendedLogsOnChangeOnly(t *testing.T) {
	logs := captureServiceLog(t)
	orgSuspended.Store(false)
	t.Cleanup(func() { orgSuspended.Store(false) })

	applyOrgSuspended(false)
	applyOrgSuspended(true)
	applyOrgSuspended(true)
	applyOrgSuspended(true)
	out := logs()
	if n := strings.Count(out, "Backups suspended by your provider"); n != 1 {
		t.Fatalf("the suspension must be said once, said %d times:\n%s", n, out)
	}
	if !strings.Contains(out, "[WARN]") && !strings.Contains(out, "WARN") {
		t.Fatalf("the suspension is a WARN:\n%s", out)
	}
	applyOrgSuspended(false)
	applyOrgSuspended(false)
	if n := strings.Count(logs(), "scheduled backups resume"); n != 1 {
		t.Fatalf("the reactivation must be said once, said %d times", n)
	}
}

func TestOnlyScheduledRunsAreSkippedWhileSuspended(t *testing.T) {
	orgSuspended.Store(true)
	t.Cleanup(func() { orgSuspended.Store(false) })
	if !scheduledRunSuspended("") {
		t.Fatal("a run the scheduler started must be skipped while suspended")
	}
	if scheduledRunSuspended("req-123") {
		t.Fatal("a run somebody asked for is still attempted (PBS refuses it)")
	}
	orgSuspended.Store(false)
	if scheduledRunSuspended("") {
		t.Fatal("nothing is skipped once the organization is active")
	}
}
