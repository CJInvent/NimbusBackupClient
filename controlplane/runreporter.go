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
