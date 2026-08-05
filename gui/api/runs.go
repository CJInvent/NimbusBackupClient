package api

import (
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// RunRegistry — the service's single record of what has run, what is running,
// and how far along it is.
//
// WHY THIS EXISTS: the GUI could not see a scheduled backup (docs/V4-PIPELINE.md
// §2). Four independent reasons, and the one this type addresses is the root:
// there was no store of runs at all. There was a map keyed by job ID whose only
// writer was handleBackup, so the only run observable through the local API was
// one the GUI itself had just started over HTTP. A backup begun by the
// scheduler, or by a portal command, existed nowhere the API could see it.
//
// The rule this type exists to enforce: **every backup the service executes is
// registered here, whatever started it, and every observer reads from here.**
// The Start button stops being special. That is not a tidiness preference — it
// is the only way to guarantee a scheduled run displays identically to a manual
// one, because there is one code path rather than two that must be kept in step.
//
// OWNS: run identity, live progress, terminal outcome, retention.
// DOES NOT OWN: executing a backup (the engine), deciding whether a backup may
// run (policy), or the per-snapshot status sidecar (BackupStatus, written by the
// engine's OnResult hook).
//
// CONCURRENCY: the engine's progress callbacks fire from upload worker
// goroutines while HTTP handlers read. Every accessor returns a COPY of the Run
// struct — handing out the pointer would let a handler serialize a struct whose
// fields are being written under no lock the caller holds. That is a data race
// that -race would catch in CI and a user would experience as a garbled number.
type RunRegistry struct {
	mu    sync.RWMutex
	runs  map[string]*Run
	order []string // insertion order; newest last

	// now is injectable so retention and duration are testable without
	// sleeping. Production always uses time.Now.
	now func() time.Time

	retain  time.Duration
	maxRuns int
	idSeq   atomic.Uint64
}

// RunState is where a run is in its life. A run is observable from the moment
// it is created, which matters: the gap between "the scheduler decided to run"
// and "the first chunk uploads" can be minutes on a large volume, and a status
// panel showing nothing during it is the bug all over again.
type RunState string

const (
	RunPreparing  RunState = "preparing"
	RunRunning    RunState = "running"
	RunFinalizing RunState = "finalizing"
	RunDone       RunState = "done"
)

// RunTrigger records what started a run. Reported, never acted on — it exists
// so an administrator looking at a status panel can tell a scheduled run from
// somebody pressing the button, which is the first question asked when a
// backup appears at an unexpected hour.
type RunTrigger string

const (
	TriggerManual   RunTrigger = "manual"
	TriggerSchedule RunTrigger = "schedule"
	TriggerPortal   RunTrigger = "portal"
	// TriggerService is what a run gets when it was executed by the service
	// but the caller did not say what started it. Honest placeholder: better
	// than labelling a scheduled run "manual" because manual is the default.
	TriggerService RunTrigger = "service"
)

// Run is one backup execution, live or finished.
type Run struct {
	RunID      string     `json:"run_id"`
	JobID      string     `json:"job_id,omitempty"` // scheduled job, empty for ad-hoc
	JobName    string     `json:"job_name,omitempty"`
	Trigger    RunTrigger `json:"trigger"`
	BackupID   string     `json:"backup_id,omitempty"`
	BackupType string     `json:"backup_type,omitempty"` // "directory" | "machine"

	State     RunState  `json:"state"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`

	Percent    float64 `json:"percent"` // 0-100
	Message    string  `json:"message,omitempty"`
	CurrentDir string  `json:"current_dir,omitempty"`

	BytesDone    uint64 `json:"bytes_done"`
	BytesTotal   uint64 `json:"bytes_total"` // 0 until the size scan finishes
	NewChunks    uint64 `json:"new_chunks"`
	ReusedChunks uint64 `json:"reused_chunks"`
	FailedChunks uint64 `json:"failed_chunks"`

	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// Running reports whether this run is still in flight.
func (r Run) Running() bool { return r.State != RunDone }

// DurationSec is elapsed time for a live run, total time for a finished one.
func (r Run) DurationSec(now time.Time) float64 {
	end := r.EndedAt
	if end.IsZero() {
		end = now
	}
	return end.Sub(r.StartedAt).Seconds()
}

const (
	// defaultRetain covers the seven-day panel with margin, so a run at the
	// far edge of the window does not vanish mid-render.
	defaultRetain = 9 * 24 * time.Hour
	// defaultMaxRuns bounds memory for a machine backing up every 15 minutes:
	// 96/day * 7 days = 672, and this is roughly triple that.
	defaultMaxRuns = 2000
)

// NewRunRegistry returns an empty registry with production retention.
func NewRunRegistry() *RunRegistry {
	return &RunRegistry{
		runs:    make(map[string]*Run),
		now:     time.Now,
		retain:  defaultRetain,
		maxRuns: defaultMaxRuns,
	}
}

// Begin registers a new run and returns its id. Called at the single point in
// the service where a backup starts, whatever started it.
func (r *RunRegistry) Begin(trigger RunTrigger, jobID, jobName, backupID, backupType string) string {
	if trigger == "" {
		trigger = TriggerService
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	start := r.now()
	id := r.mintID(start)
	r.runs[id] = &Run{
		RunID:      id,
		JobID:      jobID,
		JobName:    jobName,
		Trigger:    trigger,
		BackupID:   backupID,
		BackupType: backupType,
		State:      RunPreparing,
		StartedAt:  start,
		Message:    "Starting backup...",
	}
	r.order = append(r.order, id)
	r.pruneLocked()
	return id
}

// mintID builds a collision-free id. Caller holds the lock.
//
// The seconds stamp alone is not enough: Windows' coarse clock makes two runs
// beginning in the same second entirely ordinary, and the old job-ID scheme hit
// exactly that, which is why it carried a counter too.
func (r *RunRegistry) mintID(t time.Time) string {
	n := r.idSeq.Add(1)
	return "run-" + strconv.FormatInt(t.Unix(), 10) + "-" + strconv.FormatUint(n, 10)
}

// Progress records a percent/message update. Ignored for an unknown id, and
// ignored once a run is done: engines can emit a late callback after the
// completion callback has fired, and resurrecting a finished run would leave a
// permanently "running" entry that the status panel never clears.
func (r *RunRegistry) Progress(runID string, percent float64, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok || run.State == RunDone {
		return
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	run.Percent = percent
	if message != "" {
		run.Message = message
	}
	if run.State == RunPreparing && percent > 0 {
		run.State = RunRunning
	}
}

// Stats records structured live counters from the engine.
func (r *RunRegistry) Stats(runID string, bytesDone, bytesTotal, newChunks, reusedChunks uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok || run.State == RunDone {
		return
	}
	run.BytesDone = bytesDone
	run.BytesTotal = bytesTotal
	run.NewChunks = newChunks
	run.ReusedChunks = reusedChunks
}

// SetCurrentDir records which directory or volume is being read.
func (r *RunRegistry) SetCurrentDir(runID, dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run, ok := r.runs[runID]; ok && run.State != RunDone {
		run.CurrentDir = dir
	}
}

// SetState moves a run between non-terminal states. Terminal state is reached
// only through Complete, so a caller cannot mark a run done without recording
// an outcome.
func (r *RunRegistry) SetState(runID string, state RunState) {
	if state == RunDone {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if run, ok := r.runs[runID]; ok && run.State != RunDone {
		run.State = state
	}
}

// Complete records the terminal outcome. Idempotent: a second call is ignored,
// so a completion delivered by both the engine's OnComplete and the caller's
// error return does not overwrite success with a stale value.
func (r *RunRegistry) Complete(runID string, success bool, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok || run.State == RunDone {
		return
	}
	run.State = RunDone
	run.EndedAt = r.now()
	run.Success = success
	if message != "" {
		run.Message = message
	}
	if !success {
		run.Error = message
	}
	if success {
		run.Percent = 100
	}
}

// Get returns a copy of one run.
func (r *RunRegistry) Get(runID string) (Run, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	run, ok := r.runs[runID]
	if !ok {
		return Run{}, false
	}
	return *run, true
}

// Active returns every run still in flight, newest first.
func (r *RunRegistry) Active() []Run {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Run, 0, 2)
	for _, id := range r.order {
		if run, ok := r.runs[id]; ok && run.State != RunDone {
			out = append(out, *run)
		}
	}
	sortNewestFirst(out)
	return out
}

// ActiveCount is what /status reports, without copying every run.
func (r *RunRegistry) ActiveCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, run := range r.runs {
		if run.State != RunDone {
			n++
		}
	}
	return n
}

// Recent returns runs started within the window, newest first, live ones
// included — the seven-day panel should show today's in-flight backup at the
// top rather than only after it finishes.
func (r *RunRegistry) Recent(window time.Duration) []Run {
	cutoff := r.now().Add(-window)
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Run, 0, len(r.order))
	for _, id := range r.order {
		run, ok := r.runs[id]
		if !ok || run.StartedAt.Before(cutoff) {
			continue
		}
		out = append(out, *run)
	}
	sortNewestFirst(out)
	return out
}

// pruneLocked drops runs that are past retention or beyond the count cap.
// Caller holds the write lock.
//
// A RUNNING run is never pruned, whatever its age or position. A full-machine
// image backup of a multi-terabyte volume over a slow link can outlive any
// sensible retention window, and dropping it mid-flight would make the status
// panel report that nothing is running while the disk is visibly busy — the
// exact failure this whole type exists to end.
func (r *RunRegistry) pruneLocked() {
	cutoff := r.now().Add(-r.retain)

	kept := r.order[:0]
	for _, id := range r.order {
		run, ok := r.runs[id]
		if !ok {
			continue
		}
		if run.State != RunDone {
			kept = append(kept, id)
			continue
		}
		if run.StartedAt.Before(cutoff) {
			delete(r.runs, id)
			continue
		}
		kept = append(kept, id)
	}
	r.order = kept

	// Count cap: drop oldest FINISHED runs until under the cap.
	for len(r.order) > r.maxRuns {
		removed := false
		for i, id := range r.order {
			if run, ok := r.runs[id]; ok && run.State == RunDone {
				delete(r.runs, id)
				r.order = append(r.order[:i], r.order[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			// Everything left is running. Refusing to prune is correct;
			// looping forever is not.
			break
		}
	}
}

func sortNewestFirst(runs []Run) {
	sort.SliceStable(runs, func(i, j int) bool {
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
}
