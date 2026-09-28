package controlplane

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Durable run records (V4-BETA-FIXES §13.3, F-75).
//
// Run reports used to live only in memory, so a run interrupted by a restart,
// a power loss, a crash or a service upgrade stayed "running" on the server
// forever, and a terminal report lost to a network outage was never sent
// again. The store below keeps one record per announced run until the server
// has that run's terminal report:
//
//	R5  the first report records the run; the terminal report is stored in
//	    the record BEFORE it is sent; a delivered terminal removes it.
//	R6  after every start and successful check-in, pending records are
//	    delivered: a stored terminal is re-sent as it is; a run with none,
//	    not active in this process, is closed as failed "Interrupted".
//	R7  stopping the service closes its active runs itself, and the engine's
//	    own terminal report that follows is then not sent.

// Operator-facing texts for runs this process did not see finish.
const (
	InterruptedAtStartText = "Interrupted: the backup service ended during the run without finishing it (restart, power loss or crash)"
	InterruptedByStopText  = "Interrupted: the backup service stopped during the run (service stop, restart, shutdown or upgrade)"
)

// inflightMaxAge: a record this old is dropped rather than delivered. A run
// the server has not heard about in 30 days is history nobody will act on,
// and the file must not grow without bound on a machine that never reconnects.
const inflightMaxAge = 30 * 24 * time.Hour

type inflightRecord struct {
	// Report is the run's identity as first reported (non-terminal).
	Report RunReport `json:"report"`
	// Terminal is the terminal report, stored before it is sent. nil while
	// the run has not finished (or its process never saw it finish).
	Terminal   *RunReport `json:"terminal,omitempty"`
	RecordedAt time.Time  `json:"recorded_at"`
	// Owner is the identity the run was reported under (server URL and
	// agent id). A record is delivered only by the same identity: after a
	// re-enrollment or a move to another server, the old runs belong to an
	// agent that no longer exists and are dropped, never sent to a server
	// that has never heard of them.
	Owner string `json:"owner"`
}

// owner is the identity a client reports under.
func (c *Client) owner() string { return fmt.Sprintf("%s#%d", c.BaseURL, c.AgentID) }

// InflightRuns is the durable record store. One per file per process: a
// second Open of the same path returns the same store, so a control-plane
// restart inside one process (the GUI can host the agent) shares the set of
// active runs instead of closing its own live runs as interrupted.
type InflightRuns struct {
	path string

	mu         sync.Mutex
	active     map[string]bool // runs of THIS process with no terminal yet
	delivering bool
	writeWarn  string // last write failure logged, so an outage logs once
	now        func() time.Time
}

var (
	inflightMu     sync.Mutex
	inflightStores = map[string]*InflightRuns{}
)

// OpenInflightRuns returns the process's store for path.
func OpenInflightRuns(path string) *InflightRuns {
	inflightMu.Lock()
	defer inflightMu.Unlock()
	if s, ok := inflightStores[path]; ok {
		return s
	}
	s := &InflightRuns{path: path, active: map[string]bool{}, now: time.Now}
	inflightStores[path] = s
	return s
}

func (s *InflightRuns) load() (map[string]*inflightRecord, error) {
	recs := map[string]*inflightRecord{}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return recs, nil
	}
	if err != nil {
		return recs, err
	}
	if len(b) == 0 {
		return recs, nil
	}
	if err := json.Unmarshal(b, &recs); err != nil {
		return map[string]*inflightRecord{}, fmt.Errorf("run records %s unreadable: %w", s.path, err)
	}
	return recs, nil
}

// save writes atomically (temp file + rename) so a crash mid-write leaves
// the previous version, never half a file.
func (s *InflightRuns) save(recs map[string]*inflightRecord) error {
	b, err := json.Marshal(recs)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// update applies f to the records under the lock and persists when f
// reports a change. A storage failure never fails a backup: it is logged once
// per distinct error and the run is reported exactly as before this store
// existed.
func (s *InflightRuns) update(f func(map[string]*inflightRecord) bool) {
	recs, err := s.load()
	if err != nil {
		s.warnOnce(err)
	}
	if !f(recs) {
		return
	}
	if err := s.save(recs); err != nil {
		s.warnOnce(err)
		return
	}
	s.writeWarn = ""
}

func (s *InflightRuns) warnOnce(err error) {
	if msg := err.Error(); msg != s.writeWarn {
		s.writeWarn = msg
		logf(LogWarn, "run records: %v (runs are still reported; one interrupted by a restart may stay running on the server)", err)
	}
}

// begin records a run on its first report (R5) and marks it active.
func (s *InflightRuns) begin(rep RunReport, owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[rep.RunUUID] = true
	s.update(func(recs map[string]*inflightRecord) bool {
		if _, ok := recs[rep.RunUUID]; ok {
			return false
		}
		recs[rep.RunUUID] = &inflightRecord{Report: rep, RecordedAt: s.now().UTC(), Owner: owner}
		return true
	})
}

// finish stores a run's terminal report before it is sent. It returns false
// when the run already has one (R7 closed it first): the caller must not send.
func (s *InflightRuns) finish(rep RunReport, owner string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, rep.RunUUID)
	first := true
	s.update(func(recs map[string]*inflightRecord) bool {
		r, ok := recs[rep.RunUUID]
		if !ok {
			r = &inflightRecord{Report: rep, RecordedAt: s.now().UTC(), Owner: owner}
			recs[rep.RunUUID] = r
		}
		if r.Terminal != nil {
			first = false
			return false
		}
		t := rep
		r.Terminal = &t
		return true
	})
	return first
}

// delivered removes a run whose terminal report the server has.
func (s *InflightRuns) delivered(runUUID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.update(func(recs map[string]*inflightRecord) bool {
		if _, ok := recs[runUUID]; !ok {
			return false
		}
		delete(recs, runUUID)
		return true
	})
}

// InterruptActive gives every active run of this process the terminal
// report reason (R7), stores it, and returns the reports to send.
func (s *InflightRuns) InterruptActive(reason string) []RunReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RunReport
	now := s.now().UTC().Format(time.RFC3339)
	s.update(func(recs map[string]*inflightRecord) bool {
		for uuid := range s.active {
			r, ok := recs[uuid]
			if !ok || r.Terminal != nil {
				continue
			}
			t := r.Report
			t.Status, t.ErrorSummary, t.FinishedAt = StatusFailed, reason, now
			r.Terminal = &t
			out = append(out, t)
		}
		return len(out) > 0
	})
	s.active = map[string]bool{}
	return out
}

// pending returns what R6 must send now for owner: stored terminals as they
// are, and a failed "Interrupted" terminal (stored first) for every run with
// none that is not active here. Records past inflightMaxAge, and records of
// another identity, are dropped.
func (s *InflightRuns) pending(owner string) []RunReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RunReport
	now := s.now().UTC()
	s.update(func(recs map[string]*inflightRecord) bool {
		changed := false
		for uuid, r := range recs {
			if now.Sub(r.RecordedAt) > inflightMaxAge {
				logf(LogWarn, "run %s: record older than 30 days dropped undelivered (status %s)", uuid, statusOf(r))
				delete(recs, uuid)
				changed = true
				continue
			}
			if r.Owner != owner {
				logf(LogWarn, "run %s: recorded under a previous control-server identity; dropped undelivered (status %s)", uuid, statusOf(r))
				delete(recs, uuid)
				changed = true
				continue
			}
			if r.Terminal == nil {
				if s.active[uuid] {
					continue
				}
				t := r.Report
				t.Status, t.ErrorSummary, t.FinishedAt = StatusFailed, InterruptedAtStartText, now.Format(time.RFC3339)
				r.Terminal = &t
				changed = true
			}
			out = append(out, *r.Terminal)
		}
		return changed
	})
	return out
}

func statusOf(r *inflightRecord) RunStatus {
	if r.Terminal != nil {
		return r.Terminal.Status
	}
	return r.Report.Status
}

// SetInflight attaches the durable store to this client's run reports.
func (c *Client) SetInflight(s *InflightRuns) { c.inflight = s }

// terminalSettled: the server has the report (2xx), or refused it for a
// reason retrying cannot change (4xx other than 429).
func terminalSettled(err error) bool {
	if err == nil {
		return true
	}
	var he *httpError
	return asHTTPError(err, &he) && he.status != 429 && he.status >= 400 && he.status < 500
}

// DeliverPendingRuns is R6. Safe to call from any goroutine; a second call
// while one is delivering returns at once. Returns how many were settled.
func (c *Client) DeliverPendingRuns() int {
	s := c.inflight
	if s == nil {
		return 0
	}
	s.mu.Lock()
	if s.delivering {
		s.mu.Unlock()
		return 0
	}
	s.delivering = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.delivering = false; s.mu.Unlock() }()

	settled := 0
	for _, rep := range s.pending(c.owner()) {
		err := c.ReportRun(rep)
		if !terminalSettled(err) {
			logf(LogWarn, "run %s: pending %s report not delivered, kept for the next check-in: %v", rep.RunUUID, rep.Status, err)
			continue
		}
		if err != nil {
			logf(LogWarn, "run %s: server refused the pending %s report, record dropped: %v", rep.RunUUID, rep.Status, err)
		} else if rep.ErrorSummary == InterruptedAtStartText {
			logf(LogWarn, "run %s (%s) was interrupted before it finished; reported failed", rep.RunUUID, rep.JobName)
		} else {
			logf(LogInfo, "run %s: %s report delivered late", rep.RunUUID, rep.Status)
		}
		s.delivered(rep.RunUUID)
		settled++
	}
	return settled
}

// InterruptActiveRuns is R7, for the service's stop path: every active run
// of this process gets the terminal report reason, stored first, then sent
// in the background. What does not land before the process exits is sent by
// R6 on the next start. Returns how many runs it closed.
func (c *Client) InterruptActiveRuns(reason string) int {
	if c.inflight == nil {
		return 0
	}
	reps := c.inflight.InterruptActive(reason)
	for _, rep := range reps {
		rep := rep
		go func() {
			if err := c.ReportRun(rep); terminalSettled(err) {
				c.inflight.delivered(rep.RunUUID)
			}
		}()
	}
	return len(reps)
}

// InflightPath is the store's file beside the agent's config.
func InflightPath(configDir string) string { return filepath.Join(configDir, "runs-inflight.json") }
