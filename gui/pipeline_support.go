//go:build service
// +build service

package main

import (
	"context"
	"fmt"
	"sync"
)

// Cancellation state used to live on App. It moved here with the methods when
// they became service-only: the GUI cannot run a backup, so it has nothing to
// cancel — its StopBackup asks the service over the local API.
var (
	backupMu     sync.Mutex
	backupCancel context.CancelFunc
)

// Pipeline support: the parts of App that only mean something in a process
// that actually runs backups.
//
// Split out when the GUI stopped containing a backup engine
// (docs/V4-PIPELINE.md §3.1). Cancellation of an in-process backup and the
// post-backup Exchange tasks are both meaningless in a front end that cannot
// start one — StopBackup in the GUI asks the SERVICE to cancel, over the
// local API.

// setBackupCancel stores (or clears, with nil) the cancel func of the backup
// running in this process. Held under backupMu so a concurrent StopBackup sees
// a consistent value.
func (a *App) setBackupCancel(cancel context.CancelFunc) {
	backupMu.Lock()
	backupCancel = cancel
	backupMu.Unlock()
}

// CancelActiveBackup signals the backup running in THIS process to stop, if any,
// and reports whether one was running. Cancellation is cooperative: the engine's
// reader loop observes the cancelled context and returns an error BEFORE the PBS
// index is committed (so the incomplete backup is discarded server-side), and the
// deferred VSS Release then deletes the shadow copy and its symlink. A stop
// therefore leaves no scraps, wherever in the stream it lands.
func (a *App) CancelActiveBackup() bool {
	backupMu.Lock()
	cancel := backupCancel
	backupMu.Unlock()
	if cancel != nil {
		cancel()
		writeDebugLog("Backup: cancel requested for in-progress backup")
		return true
	}
	return false
}

// maybeRunExchangePostBackup runs the app-aware Exchange tasks after a
// successful backup, but only when Exchange is present AND the operator enabled
// awareness. Best-effort and fully logged: an Exchange task failure is recorded
// but never retroactively fails an already-successful backup.
func (a *App) maybeRunExchangePostBackup() {
	if a.config == nil || (!a.config.ExchangeAware && !a.config.ExchangeLogTruncation) {
		return
	}
	installed, version := detectExchange()
	if !installed {
		writeWarnLog("[Exchange] app-aware tasks enabled but no Exchange installation detected - skipping")
		return
	}
	writeDebugLog(fmt.Sprintf("[Exchange] Running post-backup tasks for Exchange %s (health=%v, truncateLogs=%v)",
		version, a.config.ExchangeAware, a.config.ExchangeLogTruncation))
	runExchangePostBackup(version, a.config.ExchangeAware, a.config.ExchangeLogTruncation)
}
