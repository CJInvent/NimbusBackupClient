//go:build !service
// +build !service

package main

// announceLocalRun is a no-op in the GUI build, because in that build the run
// does not start locally.
//
// executeScheduledJob compiles into both binaries — a control-plane
// run_backup command can be delivered to whichever process is hosting the
// agent — but in the GUI it reaches StartBackup, which is an HTTP POST to the
// service (docs/V4-PIPELINE.md §3.1). The service then announces the run on
// its own side, to its own registry and its own reporter. Announcing it here
// as well would open a reporter nothing ever takes and a registry entry
// nothing ever fills: two records of one backup, one of them permanently
// stuck in "preparing".
func announceLocalRun(job ScheduledJob, requestID string) {}
