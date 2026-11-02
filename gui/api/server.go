package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Server handles HTTP API requests from the GUI
type Server struct {
	app BackupHandler
	mux *http.ServeMux

	// runs is the SINGLE store of backup runs — see runs.go. It replaced a
	// map whose only writer was handleBackup, which is why a scheduled
	// backup was invisible to the GUI (docs/V4-PIPELINE.md §2).
	runs *RunRegistry

	// lockedState carries the read-only predicate (readonly.go).
	lockedState
	// restoreAllowedState carries the `file_restore` predicate (restore.go).
	restoreAllowedState

	// restoreJobs holds long-running restore work started by the console.
	// Separate from `runs` on purpose — see restore_jobs.go.
	restoreJobs *restoreJobRegistry
	// connectionsState carries the panel's data provider (connections.go).
	connectionsState

	// version is stamped in at service start. It used to be the string
	// literal "0.1.92" with a TODO beside it, which reported a version two
	// minors stale to anyone who asked.
	version string
}

// BackupHandler interface that the service must implement
// NOTE: StartBackup will be called in a goroutine (async), so it must be thread-safe
type BackupHandler interface {
	StartBackup(backupType string, backupDirs, diskTargets, excludeList []string, backupID string, useVSS bool, compression string) error
	GetConfigWithHostname() map[string]interface{}
	ListScheduledJobs() ([]ScheduledJob, error)
	SaveScheduledJobFromMap(job map[string]interface{}) error
	UpdateScheduledJobFromMap(job map[string]interface{}) error
	DeleteScheduledJobFromMap(jobID string) error
	PinServerFingerprint(id, fingerprint string) error
	// Config writes delegated by the unprivileged GUI so the service stays the
	// single writer of config.json (Phase 1: GUI as a frontend to the service).
	SavePBSServerFromMap(server map[string]interface{}) error
	DeletePBSServerByID(id string) error
	SetDefaultPBSByID(id string) error
	SaveConfigFromMap(config map[string]interface{}) error
	// Control plane (NimbusControl): connectivity snapshot for the GUI and
	// settings write (service-side, single-writer rule as everything else).
	ControlPlaneStatusMap() map[string]interface{}
	SaveControlPlaneFromMap(m map[string]interface{}) error
}

// postureReporter is the optional source of /status's security_warnings: the
// machine's security posture, computed by the service (docs/V4-CONSOLE.md
// §4.1) rather than probed from the user's session.
type postureReporter interface {
	GetSecurityWarnings() []string
}

// NewServer creates a new API server. Who may call it is not its
// constructor's business: every request must arrive on a connection that
// carries a Windows identity (Serve, authMiddleware).
func NewServer(handler BackupHandler) *Server {
	s := &Server{
		app:  handler,
		mux:  http.NewServeMux(),
		runs: NewRunRegistry(),

		restoreJobs: newRestoreJobRegistry(),
	}

	s.setupRoutes()
	return s
}

// Runs exposes the registry so the service can register runs it starts
// WITHOUT going through HTTP — the scheduler and portal-command paths. That is
// the whole point: one store, every trigger.
func (s *Server) Runs() *RunRegistry { return s.runs }

// ActiveRestoreCount is the number of console restore jobs still running.
func (s *Server) ActiveRestoreCount() int { return s.restoreJobs.running() }

// SetVersion stamps the build version reported by /status.
func (s *Server) SetVersion(v string) { s.version = v }

func (s *Server) setupRoutes() {
	s.mux.HandleFunc("/status", s.handleStatus)
	// Who the service thinks is calling (caller.go).
	s.mux.HandleFunc("/caller", s.handleCaller)
	s.mux.HandleFunc("/storage/identity", s.handleStorageIdentity)
	s.mux.HandleFunc("/backup", s.handleBackup)
	s.mux.HandleFunc("/connections", s.handleConnections)
	s.mux.HandleFunc("/runs/active", s.handleRunsActive)
	s.mux.HandleFunc("/runs/recent", s.handleRunsRecent)
	s.mux.HandleFunc("/runs/", s.handleRunByID)
	s.mux.HandleFunc("/backup/cancel", s.handleBackupCancel)
	s.mux.HandleFunc("/jobs", s.handleJobs)
	s.mux.HandleFunc("/jobs/create", s.handleJobCreate)
	s.mux.HandleFunc("/jobs/update", s.handleJobUpdate)
	s.mux.HandleFunc("/jobs/delete/", s.handleJobDelete)
	s.mux.HandleFunc("/pbs/fingerprint", s.handlePinFingerprint)
	s.mux.HandleFunc("/pbs/save", s.handleSavePBSServer)
	s.mux.HandleFunc("/pbs/delete/", s.handleDeletePBSServer)
	s.mux.HandleFunc("/pbs/default", s.handleSetDefaultPBS)
	// Read and probe PBS through the service (pbs.go), so the console needs
	// neither config.json nor the token (docs/V4-CONSOLE.md §4.1).
	s.mux.HandleFunc("/pbs/servers", s.handleListPBSServers)
	s.mux.HandleFunc("/pbs/test", s.handleTestPBS)
	s.mux.HandleFunc("/pbs/discover", s.handleDiscoverPBS)
	// Facts about this machine the console used to establish itself
	// (machine.go).
	s.mux.HandleFunc("/disks", s.handleDisks)
	s.mux.HandleFunc("/exchange", s.handleExchange)
	s.mux.HandleFunc("/config/save", s.handleSaveConfig)
	s.mux.HandleFunc("/controlplane/status", s.handleControlPlaneStatus)
	s.mux.HandleFunc("/controlplane/save", s.handleControlPlaneSave)

	// Restore lives behind the service, like backup. Three routes and one
	// declared op table — see restore.go for why it is shaped that way.
	s.mux.HandleFunc("/restore/query", s.handleRestoreQuery)
	s.mux.HandleFunc("/restore/job", s.handleRestoreJobStart)
	s.mux.HandleFunc("/restore/job/", s.handleRestoreJobState)
	s.mux.HandleFunc("/restore/control", s.handleRestoreControl)
}

// Handler is the whole server as served: the routes behind the read-only
// lockdown, behind authentication. Serve serves it; a test can serve it on
// its own listener and drive the real stack, giving the listener an identity
// with FixedCallerContext.
//
// readOnlyMiddleware sits INSIDE auth: an unauthenticated caller gets 401 and
// learns nothing, rather than 403 and learns this machine is managed and
// locked.
func (s *Server) Handler() http.Handler {
	return s.authMiddleware(s.readOnlyMiddleware(s.mux))
}

// Serve answers the local API on ln, the named pipe ListenPipe opened, until
// ln is closed.
//
// One request per connection (keep-alives off): the service reads the caller
// from the connection, and a connection that carried a second request would
// carry it under the first one's identity check. The header timeout bounds a
// client that connects and never writes, which would otherwise hold a pipe
// instance and a goroutine for ever.
func (s *Server) Serve(ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ConnContext:       connContext,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(log.Writer(), "[API] ", 0),
	}
	srv.SetKeepAlivesEnabled(false)
	log.Printf("Local API serving on %s", ln.Addr())
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	config := s.app.GetConfigWithHostname()

	version := s.version
	if version == "" {
		// Never invent one. A wrong version sends a support engineer
		// looking at the wrong changelog, which is worse than "unknown".
		version = "unknown"
	}

	status := StatusResponse{
		Running:          true,
		Version:          version,
		ActiveJobs:       s.runs.ActiveCount(),
		Configuration:    config,
		SecurityWarnings: []string{},
	}
	if p, ok := s.app.(postureReporter); ok {
		if w := p.GetSecurityWarnings(); w != nil {
			status.SecurityWarnings = w
		}
	}

	s.writeJSON(w, status, http.StatusOK)
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req BackupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	// Validate request
	if req.BackupID == "" {
		s.writeError(w, "backup_id is required", http.StatusBadRequest)
		return
	}

	// Start backup asynchronously (don't block HTTP request).
	//
	// This is currently the only caller of Begin. Step 3 of the rewire moves
	// it into the service's single StartBackup so scheduler- and
	// portal-triggered runs register too; the registry API is the same
	// either way, so that is a change of call site rather than of contract.
	jobID := s.runs.Begin(TriggerManual, "", "", req.BackupID, req.BackupType)
	log.Printf("[API] Run registered: %s (active: %d)", jobID, s.runs.ActiveCount())

	go func() {
		log.Printf("[API] Starting async backup: %s", jobID)

		// Set up progress callbacks to update the progress map
		handler, ok := s.app.(interface {
			SetProgressCallbacks(jobID string, onProgress func(string, float64, string), onStats func(string, uint64, uint64, uint64, uint64), onComplete func(string, bool, string))
		})
		if ok {
			log.Printf("[API] SetProgressCallbacks interface found, registering callbacks for %s", jobID)
			handler.SetProgressCallbacks(
				jobID,
				func(jid string, percent float64, message string) {
					s.runs.Progress(jid, percent, message)
				},
				func(jid string, bytesDone, bytesTotal, newChunks, reusedChunks uint64) {
					s.runs.Stats(jid, bytesDone, bytesTotal, newChunks, reusedChunks)
				},
				func(jid string, success bool, message string) {
					s.runs.Complete(jid, success, message)
					log.Printf("[API] Run %s complete: success=%v, %s", jid, success, message)
				},
			)
		} else {
			log.Printf("[API] WARNING: SetProgressCallbacks interface not implemented by handler")
		}

		// Call StartBackup (service App is in standalone mode to execute directly)
		// Default to "fastest" if compression not specified
		compression := req.Compression
		if compression == "" {
			compression = "fastest"
		}

		err := s.app.StartBackup(
			req.BackupType,
			req.BackupDirs,
			req.DiskTargets,
			req.ExcludeList,
			req.BackupID,
			req.UseVSS,
			compression,
		)

		// Complete() is idempotent, so this is a safety net rather than a
		// second writer: if the engine's OnComplete callback fired, this is
		// ignored. If it did NOT fire — RunMachineBackup historically never
		// emitted one — the run would otherwise stay "running" forever and
		// the status panel would show a backup that finished hours ago.
		if err != nil {
			s.runs.Complete(jobID, false, fmt.Sprintf("Backup failed: %v", err))
			log.Printf("[API] Backup %s failed: %v", jobID, err)
		} else {
			s.runs.Complete(jobID, true, "Backup completed successfully")
		}
	}()

	// Return immediately with job ID
	resp := BackupResponse{
		Success: true,
		Message: "Backup started successfully (running in background)",
		JobID:   jobID,
	}

	s.writeJSON(w, resp, http.StatusOK)
}

// handleBackupCancel stops the running backup. Backups are serialized (one at a
// time per destination), so this cancels "the active backup" rather than taking
// a job ID. It marks any still-running job terminal up front; because the async
// runner only writes a final status when the job is not already complete, the
// "cancelled" outcome sticks even though the engine also returns an error as it
// unwinds.
func (s *Server) handleBackupCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cancelled := false
	if canceller, ok := s.app.(interface{ CancelActiveBackup() bool }); ok {
		cancelled = canceller.CancelActiveBackup()
	}

	// Mark every in-flight run terminal up front. Complete() is idempotent
	// and first-writer-wins, so the "cancelled" outcome sticks even though
	// the engine also returns an error as it unwinds and the async runner
	// will call Complete again with that error.
	for _, run := range s.runs.Active() {
		s.runs.Complete(run.RunID, false, "Backup cancelled")
	}

	log.Printf("[API] Backup cancel requested (active backup running: %v)", cancelled)
	s.writeJSON(w, map[string]interface{}{"success": true, "cancelled": cancelled}, http.StatusOK)
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jobs, err := s.app.ListScheduledJobs()
	if err != nil {
		s.writeError(w, fmt.Sprintf("Failed to read the scheduled jobs: %v", err), http.StatusInternalServerError)
		return
	}
	if jobs == nil {
		jobs = []ScheduledJob{}
	}
	s.writeJSON(w, JobsResponse{Jobs: jobs}, http.StatusOK)
}

func (s *Server) handleJobCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var job map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
		s.writeError(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if err := s.app.SaveScheduledJobFromMap(job); err != nil {
		s.writeError(w, fmt.Sprintf("Failed to create job: %v", err), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"success": true,
		"message": "Job created successfully",
	}
	s.writeJSON(w, resp, http.StatusOK)
}

func (s *Server) handleJobUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var job map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
		s.writeError(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}

	if err := s.app.UpdateScheduledJobFromMap(job); err != nil {
		s.writeError(w, fmt.Sprintf("Failed to update job: %v", err), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"success": true,
		"message": "Job updated successfully",
	}
	s.writeJSON(w, resp, http.StatusOK)
}

func (s *Server) handleJobDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	jobID, ok := pathID(r, "/jobs/delete/")
	if !ok {
		s.writeError(w, "Job ID required", http.StatusBadRequest)
		return
	}

	if err := s.app.DeleteScheduledJobFromMap(jobID); err != nil {
		s.writeError(w, fmt.Sprintf("Failed to delete job: %v", err), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"success": true,
		"message": "Job deleted successfully",
	}
	s.writeJSON(w, resp, http.StatusOK)
}

// handlePinFingerprint lets the unprivileged GUI delegate a TOFU certificate pin to
// the privileged service, which is the single writer of config.json.
func (s *Server) handlePinFingerprint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID          string `json:"id"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}
	if req.ID == "" || req.Fingerprint == "" {
		s.writeError(w, "id and fingerprint are required", http.StatusBadRequest)
		return
	}

	if err := s.app.PinServerFingerprint(req.ID, req.Fingerprint); err != nil {
		s.writeError(w, fmt.Sprintf("Failed to pin fingerprint: %v", err), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"success": true,
		"message": "Fingerprint pinned successfully",
	}
	s.writeJSON(w, resp, http.StatusOK)
}

// handleSavePBSServer upserts a PBS server. The unprivileged GUI delegates the
// write here so the privileged service remains the single writer of config.json.
func (s *Server) handleSavePBSServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var server map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&server); err != nil {
		s.writeError(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}
	if err := s.app.SavePBSServerFromMap(server); err != nil {
		s.writeError(w, fmt.Sprintf("Failed to save PBS server: %v", err), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, map[string]interface{}{"success": true, "message": "PBS server saved"}, http.StatusOK)
}

// handleDeletePBSServer removes a PBS server by id (path: /pbs/delete/{id}).
func (s *Server) handleDeletePBSServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, ok := pathID(r, "/pbs/delete/")
	if !ok {
		s.writeError(w, "PBS server id required", http.StatusBadRequest)
		return
	}
	if err := s.app.DeletePBSServerByID(id); err != nil {
		s.writeError(w, fmt.Sprintf("Failed to delete PBS server: %v", err), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, map[string]interface{}{"success": true, "message": "PBS server deleted"}, http.StatusOK)
}

// handleSetDefaultPBS sets the default PBS server (body: {"id": "..."}).
func (s *Server) handleSetDefaultPBS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		s.writeError(w, "id is required", http.StatusBadRequest)
		return
	}
	if err := s.app.SetDefaultPBSByID(req.ID); err != nil {
		s.writeError(w, fmt.Sprintf("Failed to set default PBS server: %v", err), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, map[string]interface{}{"success": true, "message": "Default PBS server set"}, http.StatusOK)
}

// handleSaveConfig persists the full configuration (global settings).
func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var cfg map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		s.writeError(w, fmt.Sprintf("Invalid request: %v", err), http.StatusBadRequest)
		return
	}
	if err := s.app.SaveConfigFromMap(cfg); err != nil {
		s.writeError(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, map[string]interface{}{"success": true, "message": "Configuration saved"}, http.StatusOK)
}

// pathID is the one path segment after prefix, unescaped: /jobs/delete/{id}.
//
// Read from the ESCAPED path. The decoded one turns an id containing "%2F" into
// two segments, and the delete routes used to act on the first: a request to
// delete "a/b" deleted "a". Anything after the id is refused for the same
// reason.
func pathID(r *http.Request, prefix string) (string, bool) {
	rest, found := strings.CutPrefix(r.URL.EscapedPath(), prefix)
	if !found || rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	id, err := url.PathUnescape(rest)
	if err != nil || id == "" {
		return "", false
	}
	return id, true
}

func (s *Server) writeJSON(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) writeError(w http.ResponseWriter, message string, status int) {
	errResp := ErrorResponse{
		Error: message,
		Code:  status,
	}
	s.writeJSON(w, errResp, status)
}

// handleControlPlaneStatus returns the control-server connectivity snapshot
// (never includes secrets — the handler map is built sanitized at source).
func (s *Server) handleControlPlaneStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.writeJSON(w, s.app.ControlPlaneStatusMap(), http.StatusOK)
}

// handleControlPlaneSave applies control-server settings via the service
// (single config writer), then the service restarts its check-in loop.
func (s *Server) handleControlPlaneSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var m map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := s.app.SaveControlPlaneFromMap(m); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeJSON(w, map[string]interface{}{"ok": true}, http.StatusOK)
}
