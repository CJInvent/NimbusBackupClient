//go:build service
// +build service

package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"controlplane"
	"pbscommon"
)

// Run reporting: telling the control plane about a backup as it happens.
//
// Service-only, split out of controlplane_glue.go once the GUI stopped
// containing a backup engine (docs/V4-PIPELINE.md §3.1). A front end that
// cannot run a backup has nothing to report, and leaving this shared left the
// GUI build carrying a reporter, a pending-handoff slot and five helpers that
// nothing in that build could ever call.
//
// controlplane_glue.go keeps enrolment, the check-in loop, command handling
// and the policy gates, because the GUI still READS those.

// ---------------------------------------------------------------------------

// takeRunReporter is called from attachControlPlaneHooks at BackupOptions
// construction. A manual backup with no registered reporter gets an ad-hoc
// one so manual runs still show up in the portal.
func takeRunReporter(backupID, backupType string) *controlplane.RunReporter {
	if cpClient == nil {
		return nil
	}
	cpReportersMu.Lock()
	defer cpReportersMu.Unlock()
	if rep, ok := cpReporters[backupID]; ok && backupID != "" {
		delete(cpReporters, backupID)
		return rep
	}
	if cpPendingRep != nil {
		rep := cpPendingRep
		cpPendingRep = nil
		return rep
	}
	rep := cpClient.NewRun("manual:"+backupID, backupType)
	rep.Preparing()
	return rep
}

// attachControlPlaneHooks decorates a fully-built BackupOptions with phase
// and result reporting, and returns a FINALIZER the caller must invoke with
// the engine's error return:
//
//	finish := attachControlPlaneHooks(&opts)
//	err := RunMachineBackup(opts)
//	finish(err)
//
// It wraps (not replaces) OnResult and installs OnPhase, so every existing
// consumer keeps firing untouched.
//
// # WHY THE FINALIZER EXISTS
//
// The hooks below hang off opts.OnResult, which the DIRECTORY engine
// (backup_inline.go) emits at every exit. The MACHINE engine
// (machine_backup_windows.go, RunMachineBackup) never calls OnResult at all
// -- it reports through OnComplete and its error return instead. So a
// machine backup used to post "preparing" at registration and then NOTHING,
// ever: no success, no failure, no VSS abort. The portal showed every
// image backup stuck in "preparing" forever, "Last successful run: Never
// run", and a real VSS_E_UNEXPECTED failure was completely invisible
// server-side even though the client logged it plainly. On a fleet whose
// default_backup_mode is an image mode -- i.e. every job is type=machine --
// that meant the control plane never learned the outcome of ANY backup.
//
// Rather than thread OnResult through every exit path of a large
// Windows-only engine, the finalizer is a backstop: if the engine returned
// without OnResult having fired, report the outcome from the error return.
// That makes it impossible for ANY engine, present or future, to leave a
// run dangling in a non-terminal state -- the failure mode here was silence,
// and silence is exactly what a monitoring product must never produce.
func attachControlPlaneHooks(opts *BackupOptions) func(error) {
	kind := "directory"
	if opts.BackupType == "vm" {
		kind = "machine"
	}
	rep := takeRunReporter(opts.BackupID, kind)
	if rep == nil {
		return func(error) {} // control plane not configured
	}
	rep.SetPBSTarget(opts.BaseURL, opts.Datastore, opts.Namespace)

	var runningOnce sync.Once
	prevPhase := opts.OnPhase
	opts.OnPhase = func(phase string) {
		if phase == "running" {
			// First confirmation only: for VSS jobs this fires when the
			// shadow copy EXISTS (the product definition of "backing up");
			// multi-directory jobs confirm once per dir — report once.
			runningOnce.Do(rep.Running)
		}
		if prevPhase != nil {
			prevPhase(phase)
		}
	}

	prevMilestone := opts.OnMilestone
	opts.OnMilestone = func(checkpoint, level, message string) {
		rep.Event(checkpoint, level, message)
		if prevMilestone != nil {
			prevMilestone(checkpoint, level, message)
		}
	}

	// Guards the finalizer: set by the OnResult path below, read after the
	// engine returns. Both happen on the engine's goroutine (OnResult is
	// called synchronously by the engine, the finalizer immediately after
	// it returns), but the mutex keeps that safe if an engine ever reports
	// its result from a worker goroutine.
	var (
		reportedMu sync.Mutex
		reported   bool
	)
	markReported := func() {
		reportedMu.Lock()
		reported = true
		reportedMu.Unlock()
	}

	prevResult := opts.OnResult
	opts.OnResult = func(s *BackupStatus) {
		if s != nil {
			markReported()
			tail := s.Message
			switch {
			case s.Outcome == OutcomeFailed && errorLooksVSS(s.Message):
				rep.VSSFailed(firstLine(s.Message))
			case s.Outcome == OutcomeFailed:
				rep.Failed(firstLine(s.Message), tail)
			case len(s.SkippedReadError) > 0 || len(s.Directories) > 0 && anyDirFailed(s.Directories):
				rep.Warning(opts.BackupType, s.BackupID, s.BackupTime,
					int64(s.TotalBytes), 0, firstLine(s.Message), tail)
				cpStampSnapshotNotes(opts, rep, s.BackupID, s.BackupTime)
			default:
				rep.Success(opts.BackupType, s.BackupID, s.BackupTime,
					int64(s.TotalBytes), 0, tail)
				cpStampSnapshotNotes(opts, rep, s.BackupID, s.BackupTime)
			}
		}
		if prevResult != nil {
			prevResult(s)
		}
	}

	return func(err error) {
		reportedMu.Lock()
		already := reported
		reportedMu.Unlock()
		if already {
			return // the engine reported its own outcome; nothing to add
		}
		if err != nil {
			msg := err.Error()
			if errorLooksVSS(msg) {
				rep.VSSFailed(firstLine(msg))
			} else {
				rep.Failed(firstLine(msg), msg)
			}
			return
		}
		// Succeeded, but this engine gave us no BackupStatus, so the PBS
		// snapshot coordinates and byte counts are not available here --
		// they are reported as zero/empty rather than guessed. Recording
		// the SUCCESS is still strictly better than leaving the run in
		// "preparing" forever, and closing that metadata gap is exactly
		// what end-to-end backup-job correlation is for.
		rep.Success(opts.BackupType, opts.BackupID, time.Now().Unix(), 0, 0, "")
	}
}

func anyDirFailed(dirs []DirResult) bool {
	for _, d := range dirs {
		if !d.OK {
			return true
		}
	}
	return false
}

// cpStampSnapshotNotes attaches this run's Backup Job ID to its PBS
// snapshot (pbscommon.PBSClient.SetSnapshotNotes), so the snapshot itself
// -- not just the server's own records -- can be traced back to the run
// that made it. opts already carries everything needed (BaseURL/AuthID/
// Secret/Datastore/Namespace/CertFingerprint): no new field threaded
// through BackupStatus, no second PBS connection reused from the backup
// itself -- this is a fresh, standalone HTTP call.
//
// ONLY called from the OnResult path (the directory engine, which always
// reports a real BackupStatus with the ACTUAL s.BackupID/s.BackupTime PBS
// recorded), never from the finalizer's blind-success fallback. That
// fallback (attachControlPlaneHooks's returned func, used today only when
// the machine engine reports no BackupStatus at all) has no trustworthy
// backup-time to target -- guessing one risks silently tagging nothing,
// or the wrong snapshot, rather than the one that was just created. This
// is a known, documented gap (see attachControlPlaneHooks's own comment
// on RunMachineBackup never emitting OnResult): machine-backup snapshots
// do not get a Job ID stamped until that engine is updated to report a
// real BackupStatus like the directory engine already does.
//
// Best-effort and async: called after the backup already succeeded, so a
// slow or unreachable PBS must never delay -- or appear to affect -- the
// run's own already-decided outcome.
func cpStampSnapshotNotes(opts *BackupOptions, rep *controlplane.RunReporter, backupID string, backupTime int64) {
	if opts.BaseURL == "" {
		return // defensive only: a successful backup implies PBS was configured
	}
	pbs := &pbscommon.PBSClient{
		BaseURL: opts.BaseURL, AuthID: opts.AuthID, Secret: opts.Secret,
		Datastore: opts.Datastore, Namespace: opts.Namespace,
		CertFingerPrint: opts.CertFingerprint, // note the casing difference from BackupOptions' own field
	}
	runUUID := rep.RunUUID()
	go func() {
		note := "nimbus-job:" + runUUID
		if err := pbs.SetSnapshotNotes(opts.BackupType, backupID, backupTime, note); err != nil {
			writeDebugLog(fmt.Sprintf("[controlplane] failed to stamp PBS snapshot notes for run %s: %v", runUUID, err))
		}
	}()
}

// errorLooksVSS classifies a failure as VSS-side: the sentinel from
// backupDirectory wraps every error that occurred before the shadow copy
// was confirmed.
func errorLooksVSS(msg string) bool {
	return strings.Contains(msg, vssCreateFailedMarker)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
