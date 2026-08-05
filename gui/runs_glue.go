package main

import (
	"sync"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// Run-registry glue: the seam that makes a scheduled backup visible.
//
// The reported bug (docs/V4-PIPELINE.md §2) had four layers. Three were in the
// API package — no endpoint, no tracking, no live poll. This file is the
// fourth: a backup begun by the SCHEDULER never touched the API server's
// progress store at all, because the scheduler calls App.StartBackup directly
// and the engine runs in-process, never passing through the HTTP handler that
// was the store's only writer. Fixing the other three without this one would
// have produced a correct endpoint reporting nothing.
//
// Deliberately shaped as a mirror of registerRunReporter / takeRunReporter /
// attachControlPlaneHooks in controlplane_glue.go. That pattern already solves
// exactly this problem for the control plane — the site that DECIDES to run is
// not the site that constructs BackupOptions — and it is proven in production.
// A second, differently-shaped mechanism for the same handoff would be a thing
// to keep in step forever.
//
// This file carries no build tag, so the service build and the GUI build get
// the same glue. Only the service ever holds a registry (SetRunRegistry is
// called from service.go); in the GUI build runRegistry stays nil and every
// function here is a no-op, which is the correct behaviour for a front end
// that is on its way to having no engine at all.

var (
	runRegistryMu sync.RWMutex
	runRegistry   *api.RunRegistry
)

// SetRunRegistry installs the registry owned by the local API server. Called
// once, from service startup, so the scheduler and the API report into one
// store rather than two.
func SetRunRegistry(r *api.RunRegistry) {
	runRegistryMu.Lock()
	runRegistry = r
	runRegistryMu.Unlock()
}

func currentRunRegistry() *api.RunRegistry {
	runRegistryMu.RLock()
	defer runRegistryMu.RUnlock()
	return runRegistry
}

// registerScheduledRun records a run at the moment the SCHEDULER decides to
// start it, which is the whole point: from here the run is visible to
// /runs/active, minutes before the first chunk uploads.
//
// Called beside registerRunReporter so the two records of the same run are
// opened together and cannot drift apart.
func registerScheduledRun(jobID, jobName, backupID, backupType string) {
	reg := currentRunRegistry()
	if reg == nil {
		return
	}
	reg.Begin(api.TriggerSchedule, jobID, jobName, backupID, normalizeRunKind(backupType))
}

// normalizeRunKind maps the engine's backup-type vocabulary to the two words
// the status panel shows. "vm" is the engine's name for a whole-machine image;
// showing that word to a technician looking at a workstation would be wrong.
func normalizeRunKind(backupType string) string {
	if backupType == "vm" {
		return "machine"
	}
	return "directory"
}

// attachRunRegistry wires the engine's hooks into the registry and returns a
// finalizer to call once the engine returns.
//
// It ADOPTS an existing preparing run — one opened by the scheduler above, or
// by the API server when the GUI posted /backup — rather than opening a second
// one. Only a run nobody announced (a portal command, or a code path added
// later that forgets) gets a fresh entry, and it is labelled TriggerService
// rather than silently called manual.
func attachRunRegistry(opts *BackupOptions) func(error) {
	reg := currentRunRegistry()
	if reg == nil {
		return func(error) {} // GUI build, or service not yet wired
	}

	// Two-tier lookup, mirroring takeRunReporter's cpReporters-then-pending
	// fallback, and needed for the same reason. StartBackup substitutes the
	// hostname when it is handed an empty backup id, so a run the API server
	// opened from a POST with no id carries "" while opts carries the
	// resolved hostname. Matching only on the resolved id would open a
	// SECOND run for every such backup: one stuck in preparing forever, one
	// with the real progress.
	runID, adopted := reg.AdoptPreparing(opts.BackupID)
	if !adopted {
		runID, adopted = reg.AdoptPreparing("")
	}
	if adopted {
		reg.SetBackupID(runID, opts.BackupID)
	} else {
		runID = reg.Begin(api.TriggerService, "", "", opts.BackupID, normalizeRunKind(opts.BackupType))
	}

	prevPhase := opts.OnPhase
	opts.OnPhase = func(phase string) {
		switch phase {
		case "running":
			reg.SetState(runID, api.RunRunning)
		case "finalizing":
			reg.SetState(runID, api.RunFinalizing)
		}
		if prevPhase != nil {
			prevPhase(phase)
		}
	}

	prevProgress := opts.OnProgress
	opts.OnProgress = func(percent float64, message string) {
		// The engine reports 0-1 here; the registry and every wire format
		// downstream use 0-100. Getting this backwards would peg every
		// backup at 1% until the very end.
		reg.Progress(runID, percent*100, message)
		if prevProgress != nil {
			prevProgress(percent, message)
		}
	}

	prevStats := opts.OnStats
	opts.OnStats = func(stats *BackupProgressStats) {
		if stats != nil {
			reg.Stats(runID, stats.BytesDone, stats.BytesTotal, stats.NewChunks, stats.ReusedChunks)
			if stats.CurrentDir != "" {
				reg.SetCurrentDir(runID, stats.CurrentDir)
			}
		}
		if prevStats != nil {
			prevStats(stats)
		}
	}

	prevResult := opts.OnResult
	opts.OnResult = func(s *BackupStatus) {
		if s != nil {
			reg.Complete(runID, s.Outcome != OutcomeFailed, s.Message)
		}
		if prevResult != nil {
			prevResult(s)
		}
	}

	// Complete is idempotent and first-writer-wins, so this finalizer is a
	// safety net rather than a second writer. It has to exist:
	// RunMachineBackup never emits OnResult, so for whole-machine backups
	// this is the ONLY thing that ever moves the run out of "running" — and
	// a status panel showing a backup still going hours after it finished is
	// the same class of wrong as not showing it at all.
	return func(err error) {
		if err != nil {
			reg.Complete(runID, false, err.Error())
			return
		}
		reg.Complete(runID, true, "Backup completed successfully")
	}
}
