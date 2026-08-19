package api

// The restore job registry: long-running restore work, observed over the wire.
//
// WHY NOT RunRegistry? Because they answer different questions and merging
// them would corrupt both. RunRegistry (runs.go) is the machine's BACKUP
// history — what ran, when, triggered by what — and it is what the status
// panel and the control server's run reports read. A file download the user
// started in the Browse tab is not a backup run and must not appear as one;
// it has no schedule, no trigger, no report, and it disappears when the
// console stops caring. Two stores, because they have two lifetimes.
//
// The shape mirrors how the GUI already observes backups: the caller gets an
// id, the front end polls, and progress is a snapshot rather than a stream.
// No websocket, no Wails event crossing a process boundary — the console
// already polls every three seconds for runs and this is the same habit.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// restoreJobRetention is how long a finished job's result stays collectable.
//
// It is generous on purpose: the result of a search is the ANSWER, not a
// status line, and a user who alt-tabs away mid-search must still find it when
// they come back. Reaping exists to bound memory over a long-running service,
// not to hurry anyone.
const restoreJobRetention = 30 * time.Minute

// RestoreJobState is what a poller sees.
type RestoreJobState struct {
	ID      string `json:"id"`
	Op      string `json:"op"`
	Running bool   `json:"running"`

	Percent    float64 `json:"percent"`
	Message    string  `json:"message"`
	DoneBytes  int64   `json:"done_bytes,omitempty"`
	TotalBytes int64   `json:"total_bytes,omitempty"`
	BPS        float64 `json:"bps,omitempty"`
	ETASeconds int     `json:"eta_seconds,omitempty"`

	Complete bool   `json:"complete"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`

	// Result is the op's own JSON shape, present only once Complete and
	// Success. A search's hits arrive this way.
	Result json.RawMessage `json:"result,omitempty"`
}

type restoreJob struct {
	id     string
	op     string
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	state    RestoreJobState
	finished time.Time
}

type restoreJobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*restoreJob
	seq  atomic.Uint64
}

func newRestoreJobRegistry() *restoreJobRegistry {
	return &restoreJobRegistry{jobs: map[string]*restoreJob{}}
}

// begin registers a job and returns it, ready to run.
func (r *restoreJobRegistry) begin(op string) *restoreJob {
	ctx, cancel := context.WithCancel(context.Background())
	// Monotonic and process-local. Not a UUID: these ids never leave the
	// machine and never outlive the service, and a readable one is easier to
	// follow in a debug log than 36 characters of hex.
	id := fmt.Sprintf("restore-%d", r.seq.Add(1))
	j := &restoreJob{
		id: id, op: op, ctx: ctx, cancel: cancel,
		state: RestoreJobState{ID: id, Op: op, Running: true, Message: "Starting…"},
	}
	r.mu.Lock()
	r.jobs[id] = j
	r.reapLocked()
	r.mu.Unlock()
	return j
}

func (r *restoreJobRegistry) get(id string) (*restoreJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	return j, ok
}

func (r *restoreJobRegistry) progress(id string, pct float64, msg string, done, total int64, bps float64, eta int) {
	j, ok := r.get(id)
	if !ok {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state.Complete {
		// A late progress callback from an engine that has already returned
		// must not resurrect a finished job — the console would show a
		// completed restore going backwards.
		return
	}
	j.state.Percent = pct
	if msg != "" {
		j.state.Message = msg
	}
	j.state.DoneBytes, j.state.TotalBytes, j.state.BPS, j.state.ETASeconds = done, total, bps, eta
}

// finish records the outcome. err == nil means success; result may be nil for
// ops whose whole answer is "it worked".
func (r *restoreJobRegistry) finish(id string, result any, err error) {
	j, ok := r.get(id)
	if !ok {
		return
	}
	j.cancel() // release the context whatever happened

	j.mu.Lock()
	defer j.mu.Unlock()
	j.state.Running = false
	j.state.Complete = true
	j.finished = time.Now()
	if err != nil {
		j.state.Success = false
		j.state.Error = err.Error()
		if j.state.Message == "" || j.state.Percent < 100 {
			j.state.Message = err.Error()
		}
		return
	}
	j.state.Success = true
	j.state.Percent = 100
	if result != nil {
		raw, merr := json.Marshal(result)
		if merr != nil {
			// The work SUCCEEDED and we cannot hand back its answer. Say
			// exactly that: reporting a failure would send the user to redo
			// an extraction that already wrote its files.
			j.state.Success = false
			j.state.Error = fmt.Sprintf("%s finished but its result could not be encoded: %v", j.op, merr)
			return
		}
		j.state.Result = raw
	}
}

func (r *restoreJobRegistry) state(id string) (RestoreJobState, bool) {
	j, ok := r.get(id)
	if !ok {
		return RestoreJobState{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state, true
}

// reapLocked drops finished jobs past their retention. Called on begin rather
// than from a ticker: a service with no restore activity should own no
// goroutine for it.
func (r *restoreJobRegistry) reapLocked() {
	cutoff := time.Now().Add(-restoreJobRetention)
	for id, j := range r.jobs {
		j.mu.Lock()
		done, at := j.state.Complete, j.finished
		j.mu.Unlock()
		if done && at.Before(cutoff) {
			delete(r.jobs, id)
		}
	}
}
