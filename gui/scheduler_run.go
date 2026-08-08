//go:build service
// +build service

package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// Job EXECUTION. The service runs jobs; the GUI only edits them.
//
// Split out of scheduler.go when the GUI stopped containing a backup engine
// (docs/V4-PIPELINE.md §3.1). Everything here is called from service.go and
// nowhere else, and leaving it in the shared file left the GUI build carrying
// a scheduler it could never usefully start — which the `unused` linter
// correctly reported once the engine went away.
//
// scheduler.go keeps the job CRUD, the history file and the paths, because
// those back GUI bindings: editing a schedule is a front-end job, running one
// is not.

// RecalculateNextRuns recalculates nextRun for all jobs whose nextRun is stale (in the past).
// This prevents jobs from being permanently stuck after a service restart or missed window.
func (a *App) RecalculateNextRuns() {
	writeDebugLog("RecalculateNextRuns called - fixing stale nextRun values")

	jobs, err := a.GetScheduledJobs()
	if err != nil {
		writeErrorLog(fmt.Sprintf("Error loading scheduled jobs: %v", err))
		return
	}

	now := time.Now()
	modified := false

	for i, job := range jobs {
		if !job.Enabled || job.NextRun == "" || job.ScheduleTime == "" {
			continue
		}

		nextRun, err := time.Parse(time.RFC3339, job.NextRun)
		if err != nil {
			writeErrorLog(fmt.Sprintf("Error parsing nextRun for %s: %v", job.Name, err))
			continue
		}

		// If nextRun is more than 2 minutes in the past, recalculate it
		if now.After(nextRun.Add(2 * time.Minute)) {
			newNextRun := calculateNextRun(job.ScheduleTime)
			writeDebugLog(fmt.Sprintf("[RecalculateNextRuns] Job %s: nextRun was stale (%s), recalculated to %s",
				job.Name, job.NextRun, newNextRun))
			jobs[i].NextRun = newNextRun
			modified = true
		}
	}

	if modified {
		jobsPath, err := getScheduledJobsPath()
		if err != nil {
			writeErrorLog(fmt.Sprintf("Error getting jobs path: %v", err))
			return
		}

		data, err := json.MarshalIndent(jobs, "", "  ")
		if err != nil {
			writeErrorLog(fmt.Sprintf("Error marshaling jobs: %v", err))
			return
		}

		if err := atomicWriteFile(jobsPath, data, 0600); err != nil {
			writeErrorLog(fmt.Sprintf("Error saving recalculated jobs: %v", err))
		} else {
			writeDebugLog("Successfully recalculated stale nextRun values")
		}
	}
}

// StartScheduler starts the background job scheduler
func (a *App) StartScheduler() {
	writeDebugLog("Starting background job scheduler")

	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				a.checkAndRunScheduledJobs()
			case <-a.stopScheduler:
				writeDebugLog("Scheduler stopped")
				return
			}
		}
	}()
}

// StopScheduler stops the background job scheduler
func (a *App) StopScheduler() {
	writeDebugLog("Stopping background job scheduler")
	close(a.stopScheduler)
}

// CleanupAbandonedJobs marks any "running" jobs as abandoned on app startup
func (a *App) CleanupAbandonedJobs() {
	writeDebugLog("CleanupAbandonedJobs called - cleaning up stale running jobs")

	history, err := a.GetJobHistory()
	if err != nil {
		writeErrorLog(fmt.Sprintf("Error loading job history: %v", err))
		return
	}

	modified := false
	for i, entry := range history {
		if entry.Status == "running" {
			writeErrorLog(fmt.Sprintf("Marking abandoned job as failed: %s", entry.Name))
			history[i].Status = "failed"
			history[i].Message = "Aborted (application interrupted)"
			history[i].Timestamp = time.Now().Format(time.RFC3339)
			modified = true
		}
	}

	if modified {
		// Save updated history
		historyPath, err := getJobHistoryPath()
		if err != nil {
			writeErrorLog(fmt.Sprintf("Error getting history path: %v", err))
			return
		}

		data, err := json.MarshalIndent(history, "", "  ")
		if err != nil {
			writeErrorLog(fmt.Sprintf("Error marshaling history: %v", err))
			return
		}

		if err := atomicWriteFile(historyPath, data, 0600); err != nil {
			writeErrorLog(fmt.Sprintf("Error saving updated history: %v", err))
		} else {
			writeDebugLog("Successfully cleaned up abandoned jobs")
		}
	}
}

// HandleStartupRun executes scheduled jobs that have runAtStartup enabled
func (a *App) HandleStartupRun() {
	writeDebugLog("HandleStartupRun called - checking for startup jobs")

	// Wait a bit to avoid conflict with scheduler if app starts at scheduled time
	time.Sleep(5 * time.Second)

	jobs, err := a.GetScheduledJobs()
	if err != nil {
		writeErrorLog(fmt.Sprintf("Error loading scheduled jobs: %v", err))
		return
	}

	for _, job := range jobs {
		if !job.Enabled || !job.RunAtStartup {
			continue
		}

		// Check if this job is already running (mutex protection)
		// If scheduler already started it, the mutex will prevent duplicate execution
		writeDebugLog(fmt.Sprintf("Executing startup job: %s", job.Name))
		go a.executeScheduledJob(job, "") // cron/startup-triggered: no server request
	}
}

// schedulerTickCount tracks ticks for periodic verbose logging
var schedulerTickCount int

// checkAndRunScheduledJobs checks if any jobs need to run
func (a *App) checkAndRunScheduledJobs() {
	// Managed jobs first: they are the org's own work, and evaluating them
	// is independent of whether this machine has any local jobs at all.
	// Doing it before the early return below matters — a fully managed
	// machine has an EMPTY scheduled_jobs.json, and the local path returns
	// straight away on that.
	a.checkManagedJobs()

	jobs, err := a.GetScheduledJobs()
	if err != nil {
		writeErrorLog(fmt.Sprintf("[Scheduler] Error loading scheduled jobs: %v", err))
		return
	}

	schedulerTickCount++
	// Log status every 15 minutes (15 ticks at 1 tick/min) instead of every minute
	verbose := schedulerTickCount%15 == 1

	if len(jobs) == 0 {
		if verbose {
			writeDebugLog("[Scheduler] No scheduled jobs found")
		}
		return
	}

	now := time.Now()
	if verbose {
		writeDebugLog(fmt.Sprintf("[Scheduler] Checking %d jobs at %s", len(jobs), now.Format("15:04:05")))
	}

	for _, job := range jobs {
		if !job.Enabled {
			continue
		}

		// Parse next run time
		if job.NextRun == "" {
			continue
		}

		nextRun, err := time.Parse(time.RFC3339, job.NextRun)
		if err != nil {
			writeErrorLog(fmt.Sprintf("[Scheduler] Error parsing next run time for %s: %v", job.Name, err))
			continue
		}

		shouldRun := now.After(nextRun) && now.Before(nextRun.Add(2*time.Minute))

		if verbose {
			writeDebugLog(fmt.Sprintf("[Scheduler] Job %s: NextRun=%s, Now=%s, ShouldRun=%v",
				job.Name, nextRun.Format("15:04:05"), now.Format("15:04:05"), shouldRun))
		}

		// Check if it's time to run (within 2 minute window to avoid missing)
		if shouldRun {
			writeDebugLog(fmt.Sprintf("[Scheduler] Executing scheduled job: %s", job.Name))
			go a.executeScheduledJob(job, "") // cron-triggered: no server request
		}
	}
}
