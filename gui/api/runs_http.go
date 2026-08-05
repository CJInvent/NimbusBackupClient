package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTP surface over RunRegistry.
//
// These are the endpoints that were missing and whose absence is why the GUI
// could not see a scheduled backup (docs/V4-PIPELINE.md §2.1): there was no way
// to ask the service "what is running", only "how is THIS job id doing", which
// the caller could answer only for a run it had started itself.
//
//	GET /runs/active         everything in flight, newest first
//	GET /runs/recent?days=7  the status panel's window, live runs included
//	GET /runs/{run_id}       one run
//
// All read-only. Starting a backup stays on /backup until the pipeline rewire
// moves it, so a locked client is served entirely by this file.

// runsWindowDefaultDays is what the read-only status panel shows.
const runsWindowDefaultDays = 7

// runsWindowMaxDays caps the window. Retention is nine days (defaultRetain), so
// asking for more than that returns the same data while implying the service
// has history it does not. Refusing is clearer than silently under-answering.
const runsWindowMaxDays = 9

// RunsResponse wraps a list so the body is an object rather than a bare array.
// A top-level array cannot gain a field later without breaking every consumer.
type RunsResponse struct {
	Runs  []Run `json:"runs"`
	Count int   `json:"count"`
	// ServerTime lets a client compute elapsed time for a live run without
	// trusting its own clock, which on a workstation with bad NTP can be
	// minutes off the service's and would render a negative duration.
	ServerTime time.Time `json:"server_time"`
}

func (s *Server) handleRunsActive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runs := s.runs.Active()
	s.writeJSON(w, RunsResponse{Runs: runs, Count: len(runs), ServerTime: time.Now()}, http.StatusOK)
}

func (s *Server) handleRunsRecent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	days := runsWindowDefaultDays
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > runsWindowMaxDays {
			s.writeError(w, "days must be between 1 and "+strconv.Itoa(runsWindowMaxDays), http.StatusBadRequest)
			return
		}
		days = n
	}

	runs := s.runs.Recent(time.Duration(days) * 24 * time.Hour)
	s.writeJSON(w, RunsResponse{Runs: runs, Count: len(runs), ServerTime: time.Now()}, http.StatusOK)
}

func (s *Server) handleRunByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// "/runs/" is registered as a subtree, so it also catches "/runs" and
	// "/runs/" themselves. Neither names a run.
	id := strings.TrimPrefix(r.URL.Path, "/runs/")
	if id == "" || strings.Contains(id, "/") {
		s.writeError(w, "Run ID required", http.StatusBadRequest)
		return
	}

	run, ok := s.runs.Get(id)
	if !ok {
		s.writeError(w, "Run not found", http.StatusNotFound)
		return
	}
	s.writeJSON(w, run, http.StatusOK)
}

// toBackupProgress converts a Run to the pre-v4 wire shape.
//
// Kept only for /backup/status/{jobID}, which a GUI built before the registry
// still calls. New consumers read Run, which carries the trigger, the state and
// the current directory that BackupProgress has no room for.
func toBackupProgress(run Run) BackupProgress {
	p := BackupProgress{
		JobID:        run.RunID,
		Running:      run.Running(),
		Progress:     run.Percent,
		Message:      run.Message,
		Success:      run.Success,
		Complete:     run.State == RunDone,
		Error:        run.Error,
		StartTime:    run.StartedAt.Format(time.RFC3339),
		BytesDone:    run.BytesDone,
		BytesTotal:   run.BytesTotal,
		NewChunks:    run.NewChunks,
		ReusedChunks: run.ReusedChunks,
	}
	return p
}
