package controlplane

import (
	"sync"
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
//	rep.Success(type, id, time, totals, tail)   // or VSSFailed / Failed
//
// Every post is fire-and-forget for the CALLER (Client.post already retries
// with backoff); a lost non-terminal report is harmless — the server's
// state machine is forward-only and the terminal report carries everything.
// A lost TERMINAL report leaves the run 'running' server-side until the
// missed-backup expectation flags the job — acceptable and visible, never
// silently wrong.
//
// DELIVERY IS ORDERED. Every post used to be its own goroutine, so a
// milestone emitted right after Preparing() routinely reached the server
// BEFORE the report that creates the run row, got a 404 and was dropped --
// the timeline of runs 63/66 is missing lines for exactly this reason -- and
// two events emitted in order could be stored in the opposite order. Posts
// now go through one per-run queue drained by a single goroutine, so the
// server receives them in the order they were emitted, which is also the
// order it numbers them in (V4-RUN-AUDIT §3).
type RunReporter struct {
	c    *Client
	base RunReport

	mu       sync.Mutex
	terminal bool
	pending  []func()
	draining bool
	// last is the most recent status report accepted for sending. If the
	// server has never heard of this run when an event arrives (the
	// creating report itself failed), it is re-sent once before the event
	// is retried -- re-asserting a status is always permitted server-side.
	last *RunReport
}

// enqueue appends one delivery and starts the drainer if it is idle. The
// drainer exits when the queue empties, so an idle reporter holds no
// goroutine.
func (r *RunReporter) enqueue(f func()) {
	r.mu.Lock()
	r.pending = append(r.pending, f)
	if !r.draining {
		r.draining = true
		go r.drain()
	}
	r.mu.Unlock()
}

func (r *RunReporter) drain() {
	for {
		r.mu.Lock()
		if len(r.pending) == 0 {
			r.draining = false
			r.mu.Unlock()
			return
		}
		f := r.pending[0]
		r.pending = r.pending[1:]
		r.mu.Unlock()
		f()
	}
}

// idle reports whether every enqueued delivery has been attempted. Tests use
// it; production code never waits on delivery.
func (r *RunReporter) idle() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.draining && len(r.pending) == 0
}

// RunTotals is what a terminal report measured.
//
// A struct rather than four more int64 arguments: the call sites already read
// Success(type, id, time, a, b, tail) and adding two more bare numbers to that
// is how a caller eventually swaps two of them. Every field is filled from a
// measurement or left zero because it WAS zero -- none is a placeholder.
type RunTotals struct {
	// BytesTotal is the logical size of what was backed up.
	BytesTotal int64
	// BytesUploaded is what went over the wire: encoded chunk bodies PBS
	// accepted, after dedup, compression and encryption. It is normally a
	// small fraction of BytesTotal and that is the product working.
	BytesUploaded int64
	// ChunksNew/ChunksReused: chunks sent vs chunks the datastore already
	// had. Their sum is the run's chunk count.
	ChunksNew    int64
	ChunksReused int64
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
	r.mu.Lock()
	r.base.PBSServer, r.base.PBSDatastore, r.base.PBSNamespace = server, datastore, namespace
	r.mu.Unlock()
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
	r.mu.Lock()
	r.base.RequestID = requestID
	r.mu.Unlock()
}

// SetTrigger records what STARTED this run -- call before the first post, so
// even a run that never reaches a terminal state says how it began.
//
// The server stores an absent trigger as "service" rather than guessing, so
// the cost of not calling this is an honest "nobody said", not a wrong answer.
func (r *RunReporter) SetTrigger(trigger string) {
	r.mu.Lock()
	r.base.Trigger = trigger
	r.mu.Unlock()
}

// SetJobID links this run to the SERVER's managed job -- backup_jobs.id, as
// delivered in the check-in that handed this machine the job. Call before the
// first post. Leave unset for a run that belongs to no managed job.
func (r *RunReporter) SetJobID(jobID int64) {
	r.mu.Lock()
	r.base.JobID = jobID
	r.mu.Unlock()
}

// i64 is the "measured, and it is this" pointer. Its whole purpose is to make
// a zero survive the wire -- see RunReport's comment on BytesTotal.
func i64(v int64) *int64 { return &v }

// into stamps the measured totals onto a report. All four together, so a new
// measurement cannot be added to RunTotals and then forgotten on the wire.
func (t RunTotals) into(rep *RunReport) {
	rep.BytesTotal, rep.BytesUploaded = i64(t.BytesTotal), i64(t.BytesUploaded)
	rep.ChunksNew, rep.ChunksReused = i64(t.ChunksNew), i64(t.ChunksReused)
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
func (r *RunReporter) Success(pbsBackupType, pbsBackupID string, pbsBackupTime int64, totals RunTotals, logTail string) {
	r.post(StatusSuccess, func(rep *RunReport) {
		rep.PBSBackupType, rep.PBSBackupID = pbsBackupType, pbsBackupID
		rep.PBSBackupTime = i64(pbsBackupTime)
		totals.into(rep)
		rep.LogTail = clip(logTail, 16<<10)
		rep.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	})
}

// Warning is terminal: the backup exists but with caveats (skipped files…).
func (r *RunReporter) Warning(pbsBackupType, pbsBackupID string, pbsBackupTime int64, totals RunTotals, errSummary, logTail string) {
	r.post(StatusWarning, func(rep *RunReport) {
		rep.PBSBackupType, rep.PBSBackupID = pbsBackupType, pbsBackupID
		rep.PBSBackupTime = i64(pbsBackupTime)
		totals.into(rep)
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
	ev := RunEvent{Checkpoint: checkpoint, Level: level, Message: clip(message, 2000)}
	r.enqueue(func() {
		err := r.c.PostRunEvent(r.base.RunUUID, ev)
		var he *httpError
		if asHTTPError(err, &he) && he.status == 404 {
			// The server does not know this run: the report that creates the
			// row failed or has not landed. Re-assert the latest status once
			// (idempotent server-side) and retry the event, rather than
			// dropping the line.
			r.mu.Lock()
			last := r.last
			r.mu.Unlock()
			if last != nil {
				if rerr := r.c.ReportRun(*last); rerr == nil {
					err = r.c.PostRunEvent(r.base.RunUUID, ev)
				}
			}
		}
		if err != nil {
			logf(LogWarn, "run %s milestone event (%s) not delivered: %v", r.base.RunUUID, checkpoint, err)
		}
	})
}

func (r *RunReporter) post(status RunStatus, mutate func(*RunReport)) {
	r.mu.Lock()
	if r.terminal {
		r.mu.Unlock()
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
	r.last = &rep
	r.mu.Unlock()
	r.enqueue(func() {
		if err := r.c.ReportRun(rep); err != nil {
			// A lost TERMINAL report leaves the run 'running' on the server,
			// which an operator will see and chase -- ERROR. A lost
			// intermediate one is repaired by the next report -- WARN.
			lvl := LogWarn
			if r.isTerminal(rep.Status) {
				lvl = LogError
			}
			logf(lvl, "run %s report (%s) not delivered: %v", rep.RunUUID, rep.Status, err)
		}
	})
}

func (r *RunReporter) isTerminal(s RunStatus) bool {
	switch s {
	case StatusSuccess, StatusWarning, StatusFailed, StatusVSSFailed:
		return true
	}
	return false
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:] // keep the TAIL — the end of a log is the useful part
}
