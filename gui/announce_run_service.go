//go:build service
// +build service

package main

import (
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
func registerScheduledRun(jobID, jobName, backupID, backupType string) {
	reg := currentRunRegistry()
	if reg == nil {
		return
	}
	reg.Begin(api.TriggerSchedule, jobID, jobName, backupID, normalizeRunKind(backupType))
}

// registerRunReporter is called by executeScheduledJob (which knows the
// job's display name) BEFORE StartBackup. backupID may be "".
func registerRunReporter(backupID, jobName, backupType, requestID string) {
	if cpClient == nil {
		return
	}
	rep := cpClient.NewRun(jobName, backupType)
	if requestID != "" {
		rep.SetRequestID(requestID) // before Preparing(), so even the FIRST report carries it
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

func announceLocalRun(job ScheduledJob, requestID string) {
	registerRunReporter(job.BackupID, job.Name, job.BackupType, requestID)
	registerScheduledRun(job.ID, job.Name, job.BackupID, job.BackupType)
}
