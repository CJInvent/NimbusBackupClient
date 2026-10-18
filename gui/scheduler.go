package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// runningJobs tracks currently executing jobs to prevent duplicates
var runningJobs = make(map[string]bool)
var runningJobsMutex sync.Mutex

// ScheduledJob is a local scheduled job. The type is the local API's
// (gui/api ScheduledJob): what scheduled_jobs.json stores is what GET /jobs
// returns.
type ScheduledJob = api.ScheduledJob

func getScheduledJobsPath() (string, error) {
	// Use ProgramData on Windows (shared between GUI and Service)
	var configDir string

	if programData := os.Getenv("ProgramData"); programData != "" {
		// Windows: C:\ProgramData\NimbusBackup
		configDir = filepath.Join(programData, "NimbusBackup")
	} else if systemDrive := os.Getenv("SystemDrive"); systemDrive != "" {
		// Windows fallback: if ProgramData not set, use C:\ProgramData hardcoded
		configDir = filepath.Join(systemDrive, "ProgramData", "NimbusBackup")
	} else {
		// Unix-like: use ~/.proxmox-backup-guardian
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(homeDir, ".proxmox-backup-guardian")
	}

	// #nosec G703 -- ProgramData is a trusted Windows system environment variable, not user input
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return "", err
	}

	return filepath.Join(configDir, "scheduled_jobs.json"), nil
}

// SaveScheduledJob saves a new scheduled job
func (a *App) SaveScheduledJob(job ScheduledJob) error {
	writeInfoLog(fmt.Sprintf("SaveScheduledJob called for: %s", job.Name))

	// Load existing jobs
	jobs, err := a.GetScheduledJobs()
	if err != nil {
		writeErrorLog(fmt.Sprintf("Error loading existing jobs: %v", err))
	}

	// Set enabled by default
	job.Enabled = true

	// Calculate next run
	job.NextRun = calculateNextRun(job.ScheduleTime)

	// Add new job
	jobs = append(jobs, job)

	// Save to file
	jobsPath, err := getScheduledJobsPath()
	if err != nil {
		return fmt.Errorf("failed to get jobs path: %w", err)
	}

	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal jobs: %w", err)
	}

	if err := atomicWriteFile(jobsPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write jobs file: %w", err)
	}

	writeInfoLog(fmt.Sprintf("Scheduled job saved: %s (next run: %s)", job.Name, job.NextRun))

	// Note: For automatic execution after reboot, use the MSI installer
	// which installs NimbusBackup as a Windows Service

	return nil
}

// GetScheduledJobs returns all scheduled jobs
func (a *App) GetScheduledJobs() ([]ScheduledJob, error) {
	jobsPath, err := getScheduledJobsPath()
	if err != nil {
		return []ScheduledJob{}, err
	}

	data, err := os.ReadFile(jobsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []ScheduledJob{}, nil // No jobs yet
		}
		return nil, err
	}

	var jobs []ScheduledJob
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, err
	}

	return jobs, nil
}

// ListScheduledJobs is GET /jobs: the local jobs, whole.
func (a *App) ListScheduledJobs() ([]ScheduledJob, error) {
	return a.GetScheduledJobs()
}

// UpdateScheduledJob updates an existing scheduled job
func (a *App) UpdateScheduledJob(job ScheduledJob) error {
	writeInfoLog(fmt.Sprintf("UpdateScheduledJob called for: %s", job.Name))

	// Load existing jobs
	jobs, err := a.GetScheduledJobs()
	if err != nil {
		return fmt.Errorf("failed to load jobs: %w", err)
	}

	// Find and update the job
	found := false
	for i, j := range jobs {
		if j.ID == job.ID {
			// Preserve enabled state
			job.Enabled = j.Enabled
			// Recalculate next run with new schedule time
			job.NextRun = calculateNextRun(job.ScheduleTime)
			jobs[i] = job
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("job with ID %s not found", job.ID)
	}

	// Save to file
	jobsPath, err := getScheduledJobsPath()
	if err != nil {
		return fmt.Errorf("failed to get jobs path: %w", err)
	}

	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal jobs: %w", err)
	}

	if err := atomicWriteFile(jobsPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write jobs file: %w", err)
	}

	writeInfoLog(fmt.Sprintf("Scheduled job updated: %s (next run: %s)", job.Name, job.NextRun))
	return nil
}

// DeleteScheduledJob removes a scheduled job by ID
func (a *App) DeleteScheduledJob(jobID string) error {
	writeInfoLog(fmt.Sprintf("DeleteScheduledJob called for ID: %s", jobID))

	jobs, err := a.GetScheduledJobs()
	if err != nil {
		return err
	}

	// Filter out the job to delete
	filtered := []ScheduledJob{}
	for _, job := range jobs {
		if job.ID != jobID {
			filtered = append(filtered, job)
		}
	}

	// Save updated list
	jobsPath, err := getScheduledJobsPath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return err
	}

	return atomicWriteFile(jobsPath, data, 0600)
}

// calculateNextRun calculates the next run time based on schedule time (HH:MM)
func calculateNextRun(scheduleTime string) string {
	parts := strings.Split(scheduleTime, ":")
	if len(parts) != 2 {
		return ""
	}

	now := time.Now()
	var hour, min int
	if _, err := fmt.Sscanf(scheduleTime, "%d:%d", &hour, &min); err != nil {
		writeErrorLog(fmt.Sprintf("Error parsing schedule time %s: %v", scheduleTime, err))
		return ""
	}

	// Schedule for today at the specified time
	nextRun := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, now.Location())

	// If time has already passed today, schedule for tomorrow
	if nextRun.Before(now) {
		nextRun = nextRun.Add(24 * time.Hour)
	}

	return nextRun.Format(time.RFC3339)
}

// executeJob runs one job's backup. requestID is empty when the LOCAL
// scheduler decided (trigger schedule), and set for a control-plane
// run_backup: the portal's button (trigger portal), or a run the SERVER
// dispatched from a schedule (trigger schedule, docs/V4-DATAPATH-PROXY.md
// §11.3).
func (a *App) executeJob(job ScheduledJob, requestID string, trigger api.RunTrigger) {
	// An empty requestID means the LOCAL scheduler decided to run this —
	// nobody asked for it. A non-empty one came with a control-plane
	// run_backup command, which is managed work by definition and is never
	// gated: the org restricting a machine to managed jobs must not stop the
	// org from running one.
	// A MANAGED job is the org's own work, whoever started it locally, so
	// the unmanaged gate must not touch it. Without this check the gate
	// would key purely on "the local scheduler started it" and an org that
	// set restrict_unmanaged_backups would silently stop its OWN jobs on
	// every machine it restricted — turning a lockdown into an outage.
	if requestID == "" && !isManagedJobID(job.ID) && !UnmanagedBackupsPermitted() {
		writeInfoLog(fmt.Sprintf("Job %s not started: %v", job.Name, ErrUnmanagedBackupsDisabled))
		return
	}
	// The organization is suspended (F-22): the scheduler starts nothing,
	// managed or local. A run somebody asked for carries a request id and is
	// still attempted; PBS refuses it with the org's tokens disabled.
	if scheduledRunSuspended(requestID) {
		writeInfoLog(skippedForSuspension(job.Name))
		return
	}
	// The server refuses an agent on another release line (V4-CLIENT-RELEASES
	// §1.4); a scheduled run would be refused at the data path.
	if off, why := scheduledRunOffLine(requestID); off {
		writeInfoLog(fmt.Sprintf("Job %s not started: %s", job.Name, why))
		return
	}

	// Check if job is already running
	runningJobsMutex.Lock()
	if runningJobs[job.ID] {
		writeWarnLog(fmt.Sprintf("Job %s is already running, skipping", job.Name))
		runningJobsMutex.Unlock()
		return
	}
	runningJobs[job.ID] = true
	runningJobsMutex.Unlock()

	// Ensure we mark as not running when done
	defer func() {
		runningJobsMutex.Lock()
		delete(runningJobs, job.ID)
		runningJobsMutex.Unlock()
	}()

	// PROXY MODE (docs/V4-DATAPATH-PROXY.md §13.4-13.5). Managed jobs are
	// the server's to dispatch, so the local schedule never fires one; a
	// local job asks the server first, because its grant is the only way
	// this machine reaches PBS. Refused means not run (the server audits
	// the refusal).
	if requestID == "" && datapathProxy() {
		if isManagedJobID(job.ID) {
			writeInfoLog(fmt.Sprintf("Job %s not started here: the control server dispatches managed jobs in data path mode", job.Name))
			return
		}
		rid, err := requestLocalRun(job.Name, directorySources(job.BackupType, job.BackupDirs))
		if err != nil {
			writeWarnLog(fmt.Sprintf("Job %s not started: %v", job.Name, err))
			return
		}
		requestID = rid
	}

	writeInfoLog(fmt.Sprintf("Executing scheduled job: %s", job.Name))

	// Default to "fastest" if compression not set in job
	compression := job.Compression
	if compression == "" {
		compression = "fastest"
	}

	// Control plane: open a run under the job's DISPLAY name (must match the
	// inventory name so a success clears the server's missed-backup latch).
	// attachControlPlaneHooks picks this up at BackupOptions construction.
	announceLocalRunAs(job, requestID, trigger)

	err := a.runJob(
		job.BackupType,
		job.BackupDirs,
		job.DiskTargets, // persistent targets for machine backups; empty for directory backups
		job.ExcludeList,
		job.BackupID,
		job.UseVSS,
		compression,
	)

	// The service pipeline records the single authoritative history entry.
	// A scheduler must never add a second "success" to an asynchronous call.
	if err != nil {
		writeErrorLog(fmt.Sprintf("Scheduled job error: %v", err))
	}

	// Update job's last run and calculate next run
	// Re-read from file to pick up any changes made by the GUI while backup was running
	jobs, _ := a.GetScheduledJobs()
	for i, j := range jobs {
		if j.ID == job.ID {
			jobs[i].LastRun = time.Now().Format(time.RFC3339)
			jobs[i].NextRun = calculateNextRun(j.ScheduleTime)
			break
		}
	}

	// Save updated jobs
	jobsPath, _ := getScheduledJobsPath()
	data, _ := json.MarshalIndent(jobs, "", "  ")
	if err := atomicWriteFile(jobsPath, data, 0600); err != nil {
		writeWarnLog(fmt.Sprintf("Warning: Failed to save updated jobs: %v", err))
	}
}
