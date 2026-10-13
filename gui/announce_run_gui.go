//go:build !service
// +build !service

package main

import "github.com/tizbac/proxmoxbackupclient_go/gui/api"

// announceLocalRunAs is a no-op in the GUI build, because in that build the
// run does not start locally.
//
// executeJob compiles into both binaries -- a control-plane run_backup can be
// delivered to whichever process is hosting the agent -- but in the GUI it
// reaches StartBackup, which is an HTTP POST to the service
// (docs/V4-PIPELINE.md §3.1). The service then announces the run on its own
// side, to its own registry and its own reporter. Announcing it here as well
// would open a reporter nothing ever takes and a registry entry nothing ever
// fills: two records of one backup, one of them permanently stuck in
// "preparing".
func announceLocalRunAs(job ScheduledJob, requestID string, trigger api.RunTrigger) {}

// runJob is how executeJob starts a job's backup: in this build, by asking
// the service (StartBackup is the local API call).
func (a *App) runJob(backupType string, backupDirs, diskTargets, excludeList []string, backupID string, useVSS bool, compression string) error {
	return a.StartBackup(backupType, backupDirs, diskTargets, excludeList, backupID, useVSS, compression)
}
