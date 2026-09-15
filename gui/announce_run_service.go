//go:build service
// +build service

package main

import (
	"strconv"
	"strings"
	"sync"

	"controlplane"
	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// The reporter handoff slots. Service-only, with the functions that use them:
// only the service starts a backup locally, so only the service has a run to
// hand off between the site that DECIDES to run and the site that builds
// BackupOptions.
var (
	cpReporters   = map[string]*controlplane.RunReporter{}
	cpPendingRep  *controlplane.RunReporter
	cpReportersMu sync.Mutex
)

// announceLocalRun opens the two records for a backup that is about to start
// IN THIS PROCESS: the control plane's run reporter, and the local run
// registry the status panel reads.
//
// Both are opened at the instant the scheduler decides, not when the first
// chunk uploads — on a large volume that gap is minutes, and a panel blank
// through it is the bug docs/V4-PIPELINE.md §2 exists to close.

// registerScheduledRun records a run at the moment the SCHEDULER decides to
// start it, which is the whole point: from here the run is visible to
// /runs/active, minutes before the first chunk uploads.
//
// Called beside registerRunReporter so the two records of the same run are
// opened together and cannot drift apart.
func registerScheduledRun(trigger api.RunTrigger, jobID, jobName, backupID, backupType string) {
	reg := currentRunRegistry()
	if reg == nil {
		return
	}
	reg.Begin(trigger, jobID, jobName, backupID, normalizeRunKind(backupType))
}

// registerRunReporter is called by executeScheduledJob (which knows the
// job's display name) BEFORE StartBackup. backupID may be "".
func registerRunReporter(backupID, jobName, backupType, requestID string, trigger api.RunTrigger, serverJobID int64) {
	if cpClient == nil {
		return
	}
	rep := cpClient.NewRun(jobName, backupType)
	if requestID != "" {
		rep.SetRequestID(requestID) // before Preparing(), so even the FIRST report carries it
	}
	// Both before Preparing() for the same reason: a run that never reaches a
	// terminal state must still be able to say what started it and which job
	// it belongs to. Those are the runs an operator most needs to identify.
	rep.SetTrigger(string(trigger))
	if serverJobID > 0 {
		rep.SetJobID(serverJobID)
	}
	rep.Preparing()
	cpReportersMu.Lock()
	defer cpReportersMu.Unlock()
	if backupID != "" {
		cpReporters[backupID] = rep
		return
	}
	// CAVEAT (documented, accepted): jobs without a fixed BackupID share
	// one pending slot; two such jobs starting in the same instant could
	// swap labels. Scheduled jobs in practice carry a BackupID — this
	// fallback exists for ad-hoc/manual runs.
	cpPendingRep = rep
}

// managedJobServerID returns the SERVER's backup_jobs.id behind a managed
// job's local id, or 0 when the id does not name a managed job.
//
// It lives HERE rather than beside managedToScheduledJob because the only
// caller is the service build -- the GUI build's announceLocalRun is a
// deliberate no-op, and an unused function fails golangci-lint's unused
// check in that build. managedJobIDPrefix stays the single owner of the
// literal, which is what keeps the two halves of the encoding in step. A run reports this number
// so its history attaches to the job rather than to the job's NAME -- see
// docs/V4-RUN-AUDIT.md section 1.2.
func managedJobServerID(id string) int64 {
	if !isManagedJobID(id) {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(id, managedJobIDPrefix), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func announceLocalRun(job ScheduledJob, requestID string) {
	// WHAT STARTED THIS. executeScheduledJob is reached two ways: a calendar
	// event firing, and the portal's run_backup command -- which is the one
	// that arrives carrying a Backup Request ID. Deriving the trigger from
	// that here, at the only place that knows both, keeps the two records of
	// this run (control plane and local registry) from disagreeing.
	//
	// registerScheduledRun used to hard-code TriggerSchedule, so a run the
	// portal asked for was labelled scheduled in the machine's own status
	// panel -- the same defect as on the server, in the other codebase.
	trigger := api.TriggerSchedule
	if requestID != "" {
		trigger = api.TriggerPortal
	}
	serverJobID := managedJobServerID(job.ID)

	registerRunReporter(job.BackupID, job.Name, job.BackupType, requestID, trigger, serverJobID)
	registerScheduledRun(trigger, job.ID, job.Name, job.BackupID, job.BackupType)
}
