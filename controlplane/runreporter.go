package controlplane

import (
	"log"
	"time"
)

// RunReporter tracks one backup run's lifecycle and posts phase changes.
// Usage inside the backup path:
//
//	rep := agentClient.NewRun(jobName, "directory")
//	rep.Preparing()                 // job accepted, before VSS
//	... VSS snapshot created OK ...
//	rep.Running()                   // ONLY after the shadow copy exists
//	... upload ...
//	rep.Success(finalStats)         // or rep.VSSFailed(err) / rep.Failed(err, tail)
//
// Every post is fire-and-forget on a goroutine (Client.post already retries
// with backoff); a lost non-terminal report is harmless — the server's
// state machine is forward-only and the terminal report carries everything.
// A lost TERMINAL report leaves the run 'running' server-side until the
// missed-backup expectation flags the job — acceptable and visible, never
// silently wrong.
type RunReporter struct {
	c        *Client
	base     RunReport
	terminal bool
}

// NewRun starts tracking a run. backupType: "directory" | "machine".
func (c *Client) NewRun(jobName, backupType string) *RunReporter {
	return &RunReporter{
		c: c,
		base: RunReport{
			RunUUID:    NewRunUUID(),
			JobName:    jobName,
			BackupType: backupType,
			StartedAt:  time.Now().UTC().Format(time.RFC3339),
		},
	}
}

// SetPBSTarget records where this run lands; call before Success so the
// server can later reconcile the snapshot against PBS GC/prune.
func (r *RunReporter) SetPBSTarget(server, datastore, namespace string) {
	r.base.PBSServer, r.base.PBSDatastore, r.base.PBSNamespace = server, datastore, namespace
}

// RunUUID returns this run's Backup Job ID — the same value every
// RunReport for this run carries on the wire. Exposed so a caller can tag
// the PBS snapshot itself with it (see pbscommon.SetSnapshotNotes),
// completing the correlation chain down to the artifact PBS actually
// stores, not just the server's own records of the run.
func (r *RunReporter) RunUUID() string {
	return r.base.RunUUID
}

// SetRequestID links this run back to a server-issued Backup Request ID —
// call before the first post (Preparing) for a run that originated from a
// run_backup command carrying one. Leave unset for scheduled and
// unattributed-manual runs; the server treats an absent request_id as a
// legitimate origin, not missing data.
func (r *RunReporter) SetRequestID(requestID string) {
	r.base.RequestID = requestID
}

func (r *RunReporter) Preparing() { r.post(StatusPreparing, nil) }

// Running MUST only be called after VSS confirmed the shadow copy (or, for
// non-VSS jobs, after the source is opened for reading). This is the
// product-level definition of "backing up" — do not move it earlier.
func (r *RunReporter) Running() { r.post(StatusRunning, nil) }

// VSSFailed is terminal and triggers the VSS-specific alert runbook
// (chkdsk / vssadmin writers) server-side. Include the raw VSS error.
func (r *RunReporter) VSSFailed(errSummary string) {
	r.post(StatusVSSFailed, func(rep *RunReport) {
		rep.ErrorSummary = clip(errSummary, 500)
		rep.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	})
}

// Success is terminal. The PBS snapshot triple is REQUIRED here — without
// it the server can never detect PBS-side prune of this snapshot.
func (r *RunReporter) Success(pbsBackupType, pbsBackupID string, pbsBackupTime int64, bytesTotal, bytesUploaded int64, logTail string) {
	r.post(StatusSuccess, func(rep *RunReport) {
		rep.PBSBackupType, rep.PBSBackupID, rep.PBSBackupTime = pbsBackupType, pbsBackupID, pbsBackupTime
		rep.BytesTotal, rep.BytesUploaded = bytesTotal, bytesUploaded
		rep.LogTail = clip(logTail, 16<<10)
		rep.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	})
}

// Warning is terminal: the backup exists but with caveats (skipped files…).
func (r *RunReporter) Warning(pbsBackupType, pbsBackupID string, pbsBackupTime int64, bytesTotal, bytesUploaded int64, errSummary, logTail string) {
	r.post(StatusWarning, func(rep *RunReport) {
		rep.PBSBackupType, rep.PBSBackupID, rep.PBSBackupTime = pbsBackupType, pbsBackupID, pbsBackupTime
		rep.BytesTotal, rep.BytesUploaded = bytesTotal, bytesUploaded
		rep.ErrorSummary = clip(errSummary, 500)
		rep.LogTail = clip(logTail, 16<<10)
		rep.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	})
}

// Failed is terminal (non-VSS failure: network, PBS, IO…).
func (r *RunReporter) Failed(errSummary, logTail string) {
	r.post(StatusFailed, func(rep *RunReport) {
		rep.ErrorSummary = clip(errSummary, 500)
		rep.LogTail = clip(logTail, 16<<10)
		rep.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	})
}

// Checkpoint(s) a milestone event can be filed under — must match the
// server's exact vocabulary (AgentApi::runEvent's whitelist) or the post
// is rejected outright. Compile-time constants so a typo here is a build
// error, not a silently-swallowed 400 discovered later.
const (
	CheckpointBackupStart     = "backup_start"
	CheckpointSnapshotVSS     = "snapshot_vss"
	CheckpointDisksPartitions = "disks_partitions"
	CheckpointFinalization    = "finalization"
)

// Event posts one granular milestone line to this run's checkpoint
// timeline — a specific partition finishing, a VSS sub-step. Unlike
// Preparing/Running/Success/etc., this is NOT gated by r.terminal: a
// milestone is additional timeline detail, not a status transition, and a
// late-arriving one (e.g. a partition-completion message that lands after
// the terminal report already went out) is still meaningful, not a
// regression to guard against.
//
// Fire-and-forget on its own goroutine, matching every other report in
// this file — a lost milestone is harmless (the coarse checkpoint mapping
// already covers the run's overall outcome regardless), so this never
// blocks the caller on network I/O.
func (r *RunReporter) Event(checkpoint, level, message string) {
	go func() {
		if err := r.c.PostRunEvent(r.base.RunUUID, RunEvent{
			Checkpoint: checkpoint, Level: level, Message: clip(message, 2000),
		}); err != nil {
			log.Printf("[controlplane] run %s milestone event (%s) failed: %v", r.base.RunUUID, checkpoint, err)
		}
	}()
}

func (r *RunReporter) post(status RunStatus, mutate func(*RunReport)) {
	if r.terminal {
		return // never report past a terminal state (mirrors server rule)
	}
	switch status {
	case StatusSuccess, StatusWarning, StatusFailed, StatusVSSFailed:
		r.terminal = true
	}
	rep := r.base
	rep.Status = status
	if mutate != nil {
		mutate(&rep)
		// Terminal details (PBS triple etc.) belong to the final report
		// only; keep base clean for the improbable case of reuse.
	}
	go func() {
		if err := r.c.ReportRun(rep); err != nil {
			log.Printf("[controlplane] run %s report (%s) failed: %v", rep.RunUUID, rep.Status, err)
		}
	}()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:] // keep the TAIL — the end of a log is the useful part
}
