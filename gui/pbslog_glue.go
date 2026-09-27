package main

import (
	"pbscommon"
	"snapshot"
)

// pbslog_glue.go — routes pbscommon's diagnostic hook into the app log, for
// both processes. Kept in its own file so the wiring is obvious and single.

func init() {
	pbscommonSetDebugLog()
}

func pbscommonSetDebugLog() {
	pbscommon.DebugLogFn = func(msg string) { writeInfoLog("[pbs] " + msg) }
	snapshot.LogFn = func(msg string) { writeInfoLog(msg) }
	// Leveled, so writer failures and snapshot errors reach the WARN/ERROR
	// queue the server receives and carry the run's uuid (ledger F-36: they
	// were INFO and never left the machine).
	snapshot.WarnFn = func(msg string) { writeWarnLog(msg) }
	snapshot.ErrorFn = func(msg string) { writeErrorLog(msg) }

	// The crypto audit trail goes to the BACKUP log, not the service log, and
	// that choice is the whole point of the hook being separate.
	//
	// writeBackupLog is NOT level-gated and prefers the per-run logger, so
	// these lines land in the record of the specific backup they describe and
	// survive an agent quietened to WARN. "Was this snapshot encrypted, under
	// which key" is asked at restore time, during an incident, or by an
	// auditor — months after whoever set LogLevel has forgotten doing it.
	pbscommon.AuditLogFn = func(msg string) { writeBackupLog("[crypt] " + msg) }
}
