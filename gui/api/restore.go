package api

// Restore over the local API (docs/V4-RESTORE.md, "the restore rewire").
//
// THE RULE THIS FILE IMPLEMENTS: the service owns every active piece of the
// client. The GUI is a client of the service and nothing else, and every call
// it makes is gated on whether the server permits this machine to make it.
// Backup already worked that way — gui/backup_pipeline.go is `service`-tagged
// and the console POSTs /backup. Restore did not: browse, search, metadata,
// file restore, volume browse and download all executed IN THE GUI PROCESS,
// with the policy check evaluated there too. This is the seam that closes it.
//
// WHY THAT MATTERED, CONCRETELY:
//
//   - The key. Phase G made a restore need the snapshot's encryption key, and
//     the key file is deliberately readable only by SYSTEM and Administrators
//     (gui/keyacl_windows.go). An engine in the GUI process therefore either
//     could not read it, or forced the ACL open. With the engine in the
//     service the key never leaves the service at all.
//   - The gate. A policy checked inside the process being restrained is a
//     suggestion. `file_restore` is now enforced HERE, on the far side of an
//     authenticated socket, by a predicate the service owns.
//   - PBS credentials. The GUI used to resolve them out of config.json to
//     build a reader. It no longer sees them.
//
// SHAPE: three routes, not thirty. A per-operation route for each of the
// dozen restore entry points would put the gate in a dozen places, which is
// the arrangement that lost three of them to begin with (see the phase-G
// policy fix). Instead every call names an op from ONE table, and the gate
// runs before dispatch — a new op is refused until it is declared, and the
// declaration is where its permission is written down.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// RestoreOpKind separates the three ways a restore call behaves on the wire.
type RestoreOpKind int

const (
	// OpQuery answers immediately: a listing, a metadata read, a space check.
	// Bounded work, so the HTTP request carries the answer.
	OpQuery RestoreOpKind = iota
	// OpJob is long-running and cancellable: an extraction, a download, a
	// search over months of snapshots. Returns a job id; progress and the
	// final result are polled.
	OpJob
	// OpControl acts on work already running (cancel). Immediate, no result.
	OpControl
)

// RestoreOp declares one operation the local API will accept.
//
// EVERY FIELD IS A PERMISSION STATEMENT. `Right` is what the control server
// must grant for this call to be made at all; there is one right today
// (`file_restore`) and the field exists so a second one — `volume_browse`,
// say, gating whole-disk backups separately from directory ones — is a line in
// this table rather than a new gate.
type RestoreOp struct {
	Name  string
	Kind  RestoreOpKind
	Right string
	// Doc is not decoration: this table is the enumeration of everything the
	// GUI is allowed to ask the service to do, and it should read as one.
	Doc string
}

// RightFileRestore is the control-plane capability `file_restore`
// (controlplane.Policy.FileRestore).
const RightFileRestore = "file_restore"

// restoreOps is THE list. A call naming anything not here is refused before
// any handler sees it, so forgetting to declare an op fails closed.
var restoreOps = []RestoreOp{
	// --- archive (directory backup) ---
	{"snapshots", OpQuery, RightFileRestore, "list the snapshots of a backup group"},
	{"contents", OpQuery, RightFileRestore, "list a snapshot's file tree"},
	{"meta", OpQuery, RightFileRestore, "read a snapshot's .nimbus_backup_meta.json sidecar"},
	{"restore", OpJob, RightFileRestore, "extract a snapshot, or a selection from it, to disk"},
	{"download", OpJob, RightFileRestore, "extract a selection and package it at a chosen path"},
	{"search", OpJob, RightFileRestore, "search file names and paths across snapshots"},
	{"cancel-search", OpControl, RightFileRestore, "stop an in-flight search"},

	// --- volume backup: FILES out of a stored disk image ---
	{"volume-partitions", OpQuery, RightFileRestore, "enumerate the partitions of a stored disk image"},
	{"volume-files", OpQuery, RightFileRestore, "scan a partition's file table and list its root"},
	{"volume-directory", OpQuery, RightFileRestore, "list one directory from a scanned partition"},
	{"volume-download", OpJob, RightFileRestore, "package a selection out of a partition as a zip"},
	{"volume-file-restore", OpJob, RightFileRestore, "restore a selection out of a partition to a folder"},
	{"cancel-volume-file-restore", OpControl, RightFileRestore, "stop an in-flight volume-file download or restore"},
}

// lookupRestoreOp finds a declared op by name.
func lookupRestoreOp(name string) (RestoreOp, bool) {
	for _, op := range restoreOps {
		if op.Name == name {
			return op, true
		}
	}
	return RestoreOp{}, false
}

// RestoreOps exposes the table for tests and for documentation generation.
// Returned by value so a caller cannot edit the gate.
func RestoreOps() []RestoreOp {
	out := make([]RestoreOp, len(restoreOps))
	copy(out, restoreOps)
	return out
}

// RestoreProgress reports progress from a running job.
//
// The signature is the byte-aware one the image browser already emits
// (gui/ibemit_gui.go), because a narrower one would have to be widened the
// first time a delegated download wanted to show a rate. Zero done/total mean
// "indeterminate"; etaSec < 0 means "unknown".
type RestoreProgress func(pct float64, msg string, done, total int64, bps float64, etaSec int)

// RestoreHandler is the service side of the seam: it owns the engine.
//
// It is deliberately NOT part of BackupHandler. The GUI process constructs an
// api.Server too (it does not serve on it), and a handler that has to satisfy
// a restore interface in both builds would put the engine's type signatures
// back into the GUI — precisely what this rewire removes. The server type-
// asserts for it and answers 501 when it is absent.
type RestoreHandler interface {
	// RestoreQuery answers a bounded question. params is the op's own JSON
	// shape; the handler owns it, because these are the engine's types and
	// the API layer must not grow a copy of them to keep in step.
	RestoreQuery(op string, params json.RawMessage) (any, error)

	// RestoreJob runs long work. It must return when ctx is cancelled.
	RestoreJob(ctx context.Context, op string, params json.RawMessage, progress RestoreProgress) (any, error)

	// RestoreControl acts on running work.
	RestoreControl(op string, params json.RawMessage) error
}

// restoreCall is the request body shared by /restore/query and /restore/job.
type restoreCall struct {
	Op     string          `json:"op"`
	Params json.RawMessage `json:"params,omitempty"`
}

// restoreQueryResponse carries a query's answer.
type restoreQueryResponse struct {
	Result json.RawMessage `json:"result"`
}

// restoreJobStartResponse carries a job's id.
type restoreJobStartResponse struct {
	JobID string `json:"job_id"`
}

// restoreAllowedState carries the predicate that answers "may this machine
// restore?". Separate from lockedState because the two are different
// questions: lockdown is about the console being a status page, `file_restore`
// is about restore being permitted on this machine at all.
type restoreAllowedState struct {
	restoreMu      sync.RWMutex
	restoreAllowed func(right string) bool
}

// SetRestoreAllowedFunc installs the permission predicate.
//
// A FUNCTION, NOT A VALUE, for the same reason SetLockedFunc is: the answer
// arrives on a check-in and an org that revokes restore expects the next call
// to be refused, not the next service restart.
//
// NIL MEANS REFUSE. This is the opposite default from lockdown, and
// deliberately so — it is the same fail-closed choice
// gui/controlplane_policy_gui.go makes and for the same reason: refusing a
// restore is an inconvenience, permitting one that should not have happened is
// the data leaving the building. The service always installs one at startup,
// so nil means "wired wrong", and wired-wrong must not mean "allowed".
func (s *Server) SetRestoreAllowedFunc(f func(right string) bool) {
	s.restoreMu.Lock()
	s.restoreAllowed = f
	s.restoreMu.Unlock()
}

// restorePermitted reports whether the control plane grants `right`.
func (s *Server) restorePermitted(right string) bool {
	s.restoreMu.RLock()
	f := s.restoreAllowed
	s.restoreMu.RUnlock()
	if f == nil {
		return false
	}
	return f(right)
}

// ErrRestoreForbiddenText is the refusal the GUI shows verbatim. It matches
// the wording gui/controlplane_glue.go used when the check lived in-process,
// so the console reads the same either side of this rewire.
const ErrRestoreForbiddenText = "file restore is disabled on this machine by your administrator"

// restoreGate resolves and authorises an op. It is the ONLY door: query, job
// and control all come through here before any engine code runs.
func (s *Server) restoreGate(w http.ResponseWriter, name string, want RestoreOpKind) (RestoreOp, bool) {
	op, ok := lookupRestoreOp(name)
	if !ok {
		// Named-but-undeclared is a client bug or an attempt, and either way
		// the answer is the same: this API does not do that.
		s.writeError(w, fmt.Sprintf("unknown restore operation %q", name), http.StatusBadRequest)
		return RestoreOp{}, false
	}
	if op.Kind != want {
		s.writeError(w, fmt.Sprintf("restore operation %q is not called that way", name), http.StatusBadRequest)
		return RestoreOp{}, false
	}
	if !s.restorePermitted(op.Right) {
		s.writeError(w, ErrRestoreForbiddenText, http.StatusForbidden)
		return RestoreOp{}, false
	}
	return op, true
}

// restoreHandler returns the engine-owning handler, or writes 501.
//
// 501 rather than 500: a GUI build's own api.Server has no engine behind it
// and never will. "Not implemented here" is the truth and it is diagnosable;
// "internal error" would send someone looking for a crash.
func (s *Server) restoreHandler(w http.ResponseWriter) (RestoreHandler, bool) {
	h, ok := s.app.(RestoreHandler)
	if !ok {
		s.writeError(w, "this agent does not serve restore operations", http.StatusNotImplemented)
		return nil, false
	}
	return h, true
}

func (s *Server) handleRestoreQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var call restoreCall
	if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
		s.writeError(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}
	op, ok := s.restoreGate(w, call.Op, OpQuery)
	if !ok {
		return
	}
	h, ok := s.restoreHandler(w)
	if !ok {
		return
	}

	result, err := h.RestoreQuery(op.Name, call.Params)
	if err != nil {
		s.writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		s.writeError(w, fmt.Sprintf("could not encode the result of %s: %v", op.Name, err),
			http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, restoreQueryResponse{Result: raw}, http.StatusOK)
}

func (s *Server) handleRestoreJobStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var call restoreCall
	if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
		s.writeError(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}
	op, ok := s.restoreGate(w, call.Op, OpJob)
	if !ok {
		return
	}
	h, ok := s.restoreHandler(w)
	if !ok {
		return
	}

	job := s.restoreJobs.begin(op.Name)
	params := call.Params
	go func() {
		result, err := h.RestoreJob(job.ctx, op.Name, params, func(pct float64, msg string, done, total int64, bps float64, eta int) {
			s.restoreJobs.progress(job.id, pct, msg, done, total, bps, eta)
		})
		s.restoreJobs.finish(job.id, result, err)
	}()

	s.writeJSON(w, restoreJobStartResponse{JobID: job.id}, http.StatusOK)
}

// handleRestoreJobState serves GET /restore/job/<id>.
//
// THE POLICY CHECK IS HERE TOO. A job that was permitted when it started can
// be running when the org revokes restore, and letting its results be
// collected because the check happened once, minutes ago, would make the
// revocation cosmetic. Revoking mid-run stops the answer reaching the console.
func (s *Server) handleRestoreJobState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.restorePermitted(RightFileRestore) {
		s.writeError(w, ErrRestoreForbiddenText, http.StatusForbidden)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/restore/job/")
	id = strings.TrimSuffix(id, "/")
	if id == "" {
		s.writeError(w, "job id is required", http.StatusBadRequest)
		return
	}
	st, ok := s.restoreJobs.state(id)
	if !ok {
		s.writeError(w, "no such restore job", http.StatusNotFound)
		return
	}
	s.writeJSON(w, st, http.StatusOK)
}

func (s *Server) handleRestoreControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var call restoreCall
	if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
		s.writeError(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}
	op, ok := s.restoreGate(w, call.Op, OpControl)
	if !ok {
		return
	}
	h, ok := s.restoreHandler(w)
	if !ok {
		return
	}
	if err := h.RestoreControl(op.Name, call.Params); err != nil {
		s.writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeJSON(w, map[string]bool{"ok": true}, http.StatusOK)
}
