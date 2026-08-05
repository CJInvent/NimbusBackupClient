package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// Server handles HTTP API requests from the GUI
type Server struct {
	addr  string
	app   BackupHandler
	token string // shared local-auth token required on every route (H-01)
	mux   *http.ServeMux

	// runs is the SINGLE store of backup runs — see runs.go. It replaced a
	// map whose only writer was handleBackup, which is why a scheduled
	// backup was invisible to the GUI (docs/V4-PIPELINE.md §2).
	runs *RunRegistry

	// version is stamped in at service start. It used to be the string
	// literal "0.1.92" with a TODO beside it, which reported a version two
	// minors stale to anyone who asked.
	version string
}

// BackupHandler interface that the service must implement
// NOTE: StartBackup will be called in a goroutine (async), so it must be thread-safe
type BackupHandler interface {
	StartBackup(backupType string, backupDirs, driveLetters, excludeList []string, backupID string, useVSS bool, compression string) error
	GetConfigWithHostname() map[string]interface{}
	GetScheduledJobsForAPI() []map[string]interface{}
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

// NewServer creates a new API server. token is the shared local-auth secret that
// every request must present in the X-Nimbus-Token header (H-01).
func NewServer(addr string, handler BackupHandler, token string) *Server {
	s := &Server{
		addr:  addr,
		app:   handler,
		token: token,
		mux:   http.NewServeMux(),
		runs:  NewRunRegistry(),
	}

	s.setupRoutes()
	return s
}

// Runs exposes the registry so the service can register runs it starts
// WITHOUT going through HTTP — the scheduler and portal-command paths. That is
// the whole point: one store, every trigger.
func (s *Server) Runs() *RunRegistry { return s.runs }

// SetVersion stamps the build version reported by /status.
func (s *Server) SetVersion(v string) { s.version = v }

func (s *Server) setupRoutes() {
	s.mux.HandleFunc("/status", s.handleStatus)
	s.mux.HandleFunc("/backup", s.handleBackup)
	s.mux.HandleFunc("/backup/status/", s.handleBackupStatus)
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
	s.mux.HandleFunc("/config/save", s.handleSaveConfig)
	s.mux.HandleFunc("/controlplane/status", s.handleControlPlaneStatus)
	s.mux.HandleFunc("/controlplane/save", s.handleControlPlaneSave)
}

// Start starts the HTTP server
func (s *Server) Start() error {
	log.Printf("Starting API server on %s", s.addr)
	return http.ListenAndServe(s.addr, s.authMiddleware(s.mux))
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
		Running:       true,
		Version:       version,
		ActiveJobs:    s.runs.ActiveCount(),
		Configuration: config,
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

	// Reload config before backup (config may have been updated by GUI)
	// This ensures service uses latest config without needing restart
	if reloader, ok := s.app.(interface{ ReloadConfig() }); ok {
		reloader.ReloadConfig()
		log.Printf("[API] Config reloaded before backup")
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
			req.DriveLetters,
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

// handleBackupStatus is the DEPRECATED per-job progress route. Superseded by
// /runs/active and /runs/{run_id}; kept for one release so a GUI built before
// the run registry keeps working.
//
// An unknown id still 404s. Answering with "the run actually in flight" was
// tried and reverted: this route's contract is "how is THIS job doing", and
// the smoke suite already pins that an unknown id must not report a phantom
// job. Making a deprecated route guess is how a GUI ends up displaying a
// scheduled run as if it were the one the user started. The fix for a caller
// that does not know the id is /runs/active, which is the point of it.
func (s *Server) handleBackupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract job ID from URL path: /backup/status/{jobID}
	pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/backup/status/"), "/")
	if len(pathParts) == 0 || pathParts[0] == "" {
		s.writeError(w, "Job ID required", http.StatusBadRequest)
		return
	}

	run, ok := s.runs.Get(pathParts[0])
	if !ok {
		s.writeError(w, "Job not found", http.StatusNotFound)
		return
	}

	progress := toBackupProgress(run)
	s.writeJSON(w, &progress, http.StatusOK)
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

	jobsData := s.app.GetScheduledJobsForAPI()

	jobs := make([]JobInfo, 0, len(jobsData))
	for _, j := range jobsData {
		job := JobInfo{
			ID:         fmt.Sprintf("%v", j["id"]),
			Name:       fmt.Sprintf("%v", j["name"]),
			BackupType: fmt.Sprintf("%v", j["backup_type"]),
			Schedule:   fmt.Sprintf("%v", j["schedule"]),
			Status:     "idle", // TODO: track actual status
		}
		if lastRun, ok := j["last_run"].(string); ok {
			job.LastRun = lastRun
		}
		if nextRun, ok := j["next_run"].(string); ok {
			job.NextRun = nextRun
		}
		jobs = append(jobs, job)
	}

	resp := JobsResponse{Jobs: jobs}
	s.writeJSON(w, resp, http.StatusOK)
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

	// Extract job ID from URL path: /jobs/delete/{jobID}
	pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/jobs/delete/"), "/")
	if len(pathParts) == 0 || pathParts[0] == "" {
		s.writeError(w, "Job ID required", http.StatusBadRequest)
		return
	}
	jobID := pathParts[0]

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
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/pbs/delete/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		s.writeError(w, "PBS server id required", http.StatusBadRequest)
		return
	}
	if err := s.app.DeletePBSServerByID(parts[0]); err != nil {
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
