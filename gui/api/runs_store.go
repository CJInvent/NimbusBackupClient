package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// The run history survives a restart (docs/V4-CONSOLE.md §4.2).
//
// The registry lived in memory only, so after a service restart /runs/recent
// was empty, and the history table could not move onto it without losing every
// run before the last reboot. The locked console's panel already had that gap.
// Now the registry writes itself to one file, which replaces
// job_history.json: one history instead of two that disagree.
//
// WHEN IT WRITES: when a run begins and when it ends. Never on progress: a
// machine backup reports progress many times a second, and the file only has
// to answer "what ran and how did it end". Writing at Begin as well as at the
// end is what lets a run that died with the service be found at the next
// start: it is in the file, still running, and is closed there as interrupted
// (what CleanupAbandonedJobs did to job_history.json).
//
// HOW IT WRITES: the whole history, through the writer the service hands in
// (its atomic temp-file-and-rename), outside the registry lock so a slow disk
// never stalls a progress update, and one write at a time with the snapshot
// taken inside that turn, so the last file written is always the newest state.

// InterruptedRunText is the outcome given to a run the history file still
// shows as running at service start: the process that ran it is gone.
const InterruptedRunText = "Interrupted: the service stopped before this run finished"

// runHistoryVersion is the file's format. A file from a different version is
// refused rather than half-read.
const runHistoryVersion = 1

// runHistoryFile is the file. An object, so it can gain a field.
type runHistoryFile struct {
	Version int       `json:"version"`
	SavedAt time.Time `json:"saved_at"`
	Runs    []Run     `json:"runs"`
}

type runStore struct {
	mu   sync.Mutex // one write at a time
	save func([]byte) error
}

// Persist loads the history file's content and makes the registry write it
// from now on. data is the file as it stands (nil or empty when there is none);
// save writes a new one, atomically. Called once, at service start, before the
// registry is shared.
//
// Runs the file still shows as in flight are closed as interrupted, and the
// count is returned. Runs past retention are dropped. An unreadable file is an
// error, and nothing from it is loaded; the registry still writes from now on,
// so the caller should move the unreadable file aside before anything begins.
func (r *RunRegistry) Persist(data []byte, save func([]byte) error) (int, error) {
	var file runHistoryFile
	var loadErr error
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &file); err != nil {
			loadErr = fmt.Errorf("the run history file is unreadable: %w", err)
		} else if file.Version != runHistoryVersion {
			loadErr = fmt.Errorf("the run history file is version %d; this build reads version %d",
				file.Version, runHistoryVersion)
		}
	}

	interrupted := 0
	r.mu.Lock()
	if loadErr == nil {
		now := r.now()
		for _, run := range file.Runs {
			if run.RunID == "" {
				continue
			}
			if _, exists := r.runs[run.RunID]; exists {
				continue
			}
			if run.State != RunDone {
				run.State = RunDone
				run.EndedAt = now
				run.Success = false
				run.Message = InterruptedRunText
				run.Error = InterruptedRunText
				interrupted++
			}
			loaded := run
			r.runs[run.RunID] = &loaded
			r.order = append(r.order, run.RunID)
		}
		// Oldest first, as Begin appends, so pruning drops the oldest.
		sort.SliceStable(r.order, func(i, j int) bool {
			return r.runs[r.order[i]].StartedAt.Before(r.runs[r.order[j]].StartedAt)
		})
		r.pruneLocked()
	}
	r.store = &runStore{save: save}
	r.mu.Unlock()

	if loadErr != nil {
		return 0, loadErr
	}
	// Write once now: the interrupted runs are closed and the expired ones
	// gone in the file too, not only in memory.
	if len(file.Runs) > 0 {
		r.persist()
	}
	return interrupted, nil
}

// persist writes the history. A failure is logged and the registry carries on:
// a full disk must not stop the status panel, and the next write tries again.
func (r *RunRegistry) persist() {
	r.mu.RLock()
	st := r.store
	r.mu.RUnlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()

	data, err := r.historyJSON()
	if err == nil {
		err = st.save(data)
	}
	if err != nil {
		log.Printf("[API] the run history file was not written: %v", err)
	}
}

// historyJSON is the file's content for the registry as it is now.
func (r *RunRegistry) historyJSON() ([]byte, error) {
	r.mu.RLock()
	file := runHistoryFile{
		Version: runHistoryVersion,
		SavedAt: r.now(),
		Runs:    make([]Run, 0, len(r.order)),
	}
	for _, id := range r.order {
		if run, ok := r.runs[id]; ok {
			file.Runs = append(file.Runs, *run)
		}
	}
	r.mu.RUnlock()
	return json.Marshal(file)
}
