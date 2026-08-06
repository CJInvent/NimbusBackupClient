//go:build service
// +build service

package main

import "github.com/tizbac/proxmoxbackupclient_go/gui/api"

// Wiring the engine's hooks into the run registry. Service-only: only the
// service links a backup engine (docs/V4-PIPELINE.md §3.1), so only the
// service has hooks to attach.
//
// Registering a run is NOT service-only, and stays in runs_glue.go. The
// scheduler compiles into both builds because a control-plane run_backup
// command can be delivered to either, and in the GUI build it asks the
// service to do the work.

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
