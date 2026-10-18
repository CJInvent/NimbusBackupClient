//go:build !service
// +build !service

package main

import (
	"errors"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

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

// runJob is executeJob's way to start a job's backup. The console never runs
// jobs: the scheduler and the control-plane agent that call executeJob run in
// the service only, and nothing in this build starts them. It exists because
// executeJob still compiles into this build until V4-CONSOLE C3 gives the
// console its own module; it refuses rather than pretending.
func (a *App) runJob(string, []string, []string, []string, string, bool, string) error {
	return errors.New(errServiceUnavailable)
}
