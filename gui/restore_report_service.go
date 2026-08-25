//go:build service
// +build service

package main

// restore_report_service.go — telling the control server that a restore
// happened (V4-SPEC §9.7 step 2).
//
// WHAT THIS IS FOR. A restore fetches a backup key and no backup run follows
// it, so the server's key-release audit sees it as unmatched by construction.
// Before this, the only thing separating "a person restored a file" from
// "something took the key and vanished" was the `mode=restore` field on the
// fetch — one value chosen by this process. Reporting the restore itself means
// the two records have to agree.
//
// IT IS NOT PROOF AND MUST NOT BE SOLD AS ONE: the same agent token authorises
// the key fetch and this report. The server grades a self-report BELOW a
// portal action taken by a named user, which is the correct ordering and the
// reason the two claims are not collapsed.
//
// THE RULE THAT OVERRIDES EVERYTHING HERE: a restore must never fail because
// its report did. Recovery is the moment this product exists for. Every call
// below logs and continues.

import (
	"errors"
	"fmt"
	"time"

	"controlplane"
)

// currentControlPlaneClient reads the shared client under its mutex.
//
// Local rather than reaching for cpClient directly: this file runs on the
// restore path, which is reached from the local API's goroutines while the
// control loop may be restarting the client underneath it. Every other reader
// in this package takes cpMu; a bare read here would be the one data race.
func currentControlPlaneClient() *controlplane.Client {
	cpMu.Lock()
	defer cpMu.Unlock()
	return cpClient
}

// restoreReporter is one restore's reporting lifetime. The zero value is
// usable and does nothing, which is what an unmanaged machine gets.
type restoreReporter struct {
	uuid    string
	kind    controlplane.RestoreKind
	started time.Time
	// live is false when there is no control server, no uuid, or the opening
	// report never landed. A terminal report for a restore the server never
	// heard start is not useful -- it would arrive as a fresh row claiming to
	// have finished, with a started_at we made up.
	live bool
}

// beginRestoreReport announces a restore that is about to run.
//
// Returns a reporter whose finish method is safe to call unconditionally,
// including on every path where this one failed.
func beginRestoreReport(kind controlplane.RestoreKind, snapshot string) *restoreReporter {
	r := &restoreReporter{kind: kind, started: time.Now()}

	c := currentControlPlaneClient()
	if c == nil {
		// Not an error and not worth a log line at warn: an unmanaged
		// machine has nobody to tell, and restores on it are ordinary.
		return r
	}

	uuid, err := controlplane.NewRestoreUUID()
	if err != nil {
		writeWarnLog(fmt.Sprintf("[RestoreReport] could not generate a restore id: %v", err))
		return r
	}
	r.uuid = uuid

	err = c.ReportRestore(controlplane.RestoreReport{
		RestoreUUID: uuid,
		Kind:        kind,
		// Always console from here. This seam is the LOCAL API, which is
		// what the machine's own GUI talks to; a server-driven browse or
		// extract arrives as an agent command on a different path and will
		// name its command_id when that path reports too.
		Source:    controlplane.RestoreConsole,
		Status:    controlplane.RestoreRunning,
		StartedAt: controlplane.RestoreStartedAt(r.started),
		Snapshot:  snapshot,
	})
	if err != nil {
		writeWarnLog(fmt.Sprintf("[RestoreReport] the control server did not record the START of restore %s: %v", uuid, err))
		return r
	}
	r.live = true
	return r
}

// finish reports how the restore ended. Never returns an error: there is
// nothing a caller could usefully do with one, and the temptation to surface
// it to the user during a recovery is exactly the harm this file avoids.
func (r *restoreReporter) finish(restoreErr error, items int, bytes int64) {
	if r == nil || !r.live {
		return
	}
	// The only judgement made here is "was this a cancellation", which is a
	// sentinel comparison against an engine error this file can see. What a
	// cancellation MEANS -- that it is a decision and not a failure -- lives
	// in controlplane.RestoreOutcome, where it is testable without the
	// service build tag.
	status, summary := controlplane.RestoreOutcome(restoreErr, errors.Is(restoreErr, errVolumeRestoreCancelled))

	c := currentControlPlaneClient()
	if c == nil {
		return
	}
	if err := c.ReportRestore(controlplane.RestoreReport{
		RestoreUUID:  r.uuid,
		Kind:         r.kind,
		Source:       controlplane.RestoreConsole,
		Status:       status,
		StartedAt:    controlplane.RestoreStartedAt(r.started),
		FinishedAt:   controlplane.RestoreStartedAt(time.Now()),
		ItemCount:    items,
		BytesWritten: bytes,
		ErrorSummary: summary,
	}); err != nil {
		writeWarnLog(fmt.Sprintf("[RestoreReport] the control server did not record the END of restore %s: %v", r.uuid, err))
	}
}

// reportedRestore runs fn as a reported restore. The restore's own error is
// returned untouched -- reporting never changes what the caller sees.
func reportedRestore(kind controlplane.RestoreKind, snapshot string, items int, fn func() error) error {
	rep := beginRestoreReport(kind, snapshot)
	err := fn()
	rep.finish(err, items, 0)
	return err
}
