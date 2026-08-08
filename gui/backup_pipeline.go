//go:build service
// +build service

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"security"
)

// THE backup pipeline. One copy, and it is not in the GUI binary.
//
// The build tag is the lockdown. CJ's decision (2026-08-05) is that the GUI
// links no backup engine at all — absent rather than gated — so that a
// modified front end has nothing to call, and the escape hatch for a machine
// with no control plane is a SERVICE-side toggle instead. This file is the
// only caller of RunBackupInline and RunMachineBackup, so excluding it leaves
// both unreferenced in the GUI build and the linker drops them.
//
// Step 3 of docs/V4-PIPELINE.md. Until this file there were two: the GUI
// build's startBackupDirect (main.go) and the service build's StartBackup
// (app_service_stubs.go), each about 150 lines of option assembly, written
// from the same starting point and drifted since. The service build is the one
// that runs on every managed machine; these are the checks it was MISSING, all
// present in the GUI copy:
//
//   - security.ValidateBackupID on the id that becomes a PBS snapshot path
//   - security.ValidatePath on every directory handed to the engine
//   - pbsCfg.Validate(), so a half-configured PBS target fails here with a
//     reason rather than somewhere inside the upload
//   - the empty-target checks (errDirRequired / errDiskRequired)
//   - the isAdmin() check before raw \\.\PhysicalDriveN access
//
// And one the GUI copy was missing: split settings were read from a.config
// rather than from the resolved pbsCfg, so a multi-PBS-only configuration —
// exactly the one audit M-01/M-04 was raised about — silently ignored the
// per-server DisableSplit and SplitSizeBytes.
//
// Neither list was a decision. They are what two copies of one function look
// like after a year of fixes landing in whichever copy the bug was reported
// against. That is the argument for this file existing, and the reason "no
// dead-end code" was worth stating as a requirement.
//
// THERE ARE NO WAILS EVENTS HERE. An earlier version of this file emitted
// backup:progress / backup:stats / backup:complete / backup:result through a
// build-tagged seam that was real in the GUI build and a no-op in the service
// build. Once this file itself became service-only, that seam was a no-op on
// every path it could take, and the events were emitted nowhere at all — dead
// code with a convincing shape. Progress reaches the front end the way it
// reaches every other observer: it polls /runs/active (docs/V4-PIPELINE.md
// §3.5). One observation path, which is the property the whole rewire is for.
//
// OWNS: turning a backup request into a completed run.
// DOES NOT OWN: deciding a backup should happen (scheduler, API, portal),
// whether it is allowed (policy), or how it is reported (controlplane_glue.go,
// runs_glue.go).

// backupRequest is one request to back something up, however it arrived.
type backupRequest struct {
	BackupType   string // "directory" | "machine"
	BackupDirs   []string
	DriveLetters []string
	ExcludeList  []string
	BackupID     string
	UseVSS       bool
	Compression  string
}

// resolvedBackup is a request after normalization and validation: everything
// the option assembly needs, with nothing left to decide.
type resolvedBackup struct {
	backupID      string
	compression   string
	pbsBackupType string // "host" | "vm"
	targetDirs    []string
	pbs           *Config
}

// resolveBackupRequest normalizes and validates one request.
//
// Separate from execution so the request is fully resolved before any hook is
// attached or any engine is entered: a bad request fails with a reason and
// leaves no run record, no reporter and no PBS connection behind it.
func (a *App) resolveBackupRequest(req backupRequest) (*resolvedBackup, error) {
	if a.config == nil {
		return nil, errors.New("configuration not loaded")
	}

	out := &resolvedBackup{
		backupID:      req.BackupID,
		compression:   req.Compression,
		pbsBackupType: "host",
	}
	if out.backupID == "" {
		// os.Hostname rather than a.GetHostname: the latter is a Wails
		// binding and exists only in the GUI build, and this file has to
		// compile into both.
		out.backupID, _ = os.Hostname()
		writeDebugLog(fmt.Sprintf("[Backup ID] Empty backup-id, using hostname: %s", out.backupID))
	}
	if out.compression == "" {
		out.compression = "fastest"
		writeDebugLog("[Compression] Using default: fastest")
	}

	// Before this file, none of the validation below ran in the service
	// process. The backup id becomes part of a PBS snapshot path, so
	// validating it only in the build that does NOT run on managed machines
	// is the wrong way round.
	if err := security.ValidateBackupID(out.backupID); err != nil {
		return nil, fmt.Errorf("invalid backup ID: %w", err)
	}
	for _, dir := range req.BackupDirs {
		if err := security.ValidatePath(dir); err != nil {
			return nil, fmt.Errorf("invalid path '%s': %w", dir, err)
		}
	}

	// Resolve the EFFECTIVE PBS config: a multi-PBS-only config leaves the
	// legacy BaseURL/AuthID/Secret/Datastore fields empty, and building
	// options from those directly yielded "PBS connection parameters
	// required" in service mode (audit M-01/M-04, reported in production).
	out.pbs = a.config.EffectivePBS()
	if err := out.pbs.Validate(); err != nil {
		return nil, err
	}

	switch req.BackupType {
	case "directory":
		if len(req.BackupDirs) == 0 {
			return nil, errors.New(errDirRequired)
		}
		out.targetDirs = req.BackupDirs
	case "machine":
		if len(req.DriveLetters) == 0 {
			return nil, errors.New(errDiskRequired)
		}
		// Raw access to \\.\PhysicalDriveN, and the VSS snapshot of its
		// mounted partitions, always requires elevation. Failing here with a
		// clear message beats an opaque CreateFile "access denied" partway
		// through. In the service this is always true (LocalSystem), so the
		// check costs nothing there and is the only protection in the GUI.
		if !isAdmin() {
			return nil, errors.New(errAdminRequired)
		}
		out.targetDirs = req.DriveLetters
		// Directory backups are stored as host snapshots; full-volume
		// backups as vm snapshots holding drive-*.img.fidx, matching the
		// upstream machinebackup layout the nbd restore tool expects.
		out.pbsBackupType = "vm"
	default:
		return nil, fmt.Errorf("unknown backup type %q", req.BackupType)
	}

	return out, nil
}

// runBackupPipeline validates, assembles, executes and finalizes one backup.
// It blocks until the engine returns; callers that need it asynchronous wrap
// it themselves, as the API server already does.
func (a *App) runBackupPipeline(req backupRequest) error {
	r, err := a.resolveBackupRequest(req)
	if err != nil {
		return err
	}
	backupID, compression, targetDirs, pbsCfg := r.backupID, r.compression, r.targetDirs, r.pbs

	writeDebugLog(fmt.Sprintf("[Pipeline] StartBackup: type=%s, id=%s, vss=%v, compression=%s, dir_count=%d",
		req.BackupType, security.SanitizeForLog(backupID), req.UseVSS, compression, len(req.BackupDirs)))

	// --- Assemble --------------------------------------------------------

	var lastRelayedMsg string
	opts := BackupOptions{
		BaseURL:         pbsCfg.BaseURL,
		AuthID:          pbsCfg.AuthID,
		Secret:          pbsCfg.Secret,
		Datastore:       pbsCfg.Datastore,
		Namespace:       pbsCfg.Namespace,
		CertFingerprint: pbsCfg.CertFingerprint,
		BackupDirs:      targetDirs,
		BackupID:        backupID,
		BackupType:      r.pbsBackupType,
		UseVSS:          req.UseVSS,
		Compression:     compression,
		ExcludeList:     req.ExcludeList,
		// From the RESOLVED server, not from a.config: the GUI copy read
		// these from the legacy top-level fields, so a multi-PBS-only
		// configuration ignored the per-server split settings entirely.
		DisableSplit:    pbsCfg.DisableSplit,
		SplitSizeBytes:  pbsCfg.SplitSizeBytes(),
		UploadLimitMbps: a.config.UploadLimitMbps,

		OnProgress: func(percent float64, message string) {
			// Engines report a 0..1 fraction; callbacks and the GUI use
			// 0-100. The engine already logs its own progress, so this
			// relay logs only phase transitions — a line per chunk rewrote
			// megabytes of log on a 931 GB disk (~240k chunks).
			pct := percent * 100
			if message != "" && message != lastRelayedMsg {
				lastRelayedMsg = message
				writeDebugLog(fmt.Sprintf("Pipeline relay: %q delivered at %.1f%%", message, pct))
			}
			a.notifyProgressCallbacks(pct, message)
		},

		OnStats: func(stats *BackupProgressStats) {
			if stats == nil {
				return
			}
			a.notifyStatsCallbacks(stats.BytesDone, stats.BytesTotal, stats.NewChunks, stats.ReusedChunks)
		},

		OnComplete: func(success bool, message string) {
			writeDebugLog(fmt.Sprintf("[Backup Complete] success=%v - %s", success, message))
			if success {
				a.maybeRunExchangePostBackup()
			}

			a.notifyCompleteCallbacks(success, message)

			// Local history. The service build grew this separately because
			// a manual "Back up now" on a managed machine appeared in the
			// portal (takeRunReporter's ad-hoc fallback) but not in the
			// client's own list — the two counts disagreed for exactly that
			// reason.
			historyEntry := JobHistory{
				ID:         fmt.Sprintf("%d", time.Now().Unix()),
				Name:       fmt.Sprintf("Manual backup - %s", backupID),
				Timestamp:  time.Now().Format(time.RFC3339),
				Status:     "success",
				Message:    message,
				BackupDirs: targetDirs,
				BackupID:   backupID,
				UseVSS:     req.UseVSS,
			}
			if !success {
				historyEntry.Status = "failed"
			}
			if err := a.AddJobHistory(historyEntry); err != nil {
				writeWarnLog(fmt.Sprintf("Warning: failed to add backup to history: %v", err))
			}

			if success && req.BackupType == "directory" {
				a.config.LastBackupDirs = req.BackupDirs
				if err := a.config.Save(); err != nil {
					writeErrorLog(fmt.Sprintf("Failed to save last backup dirs: %v", err))
				}
			}
		},
	}

	// --- Report ----------------------------------------------------------
	//
	// Both finalizers MUST be called with the engine's error.
	// RunMachineBackup never emits OnResult, so without them an image backup
	// is never reported as finished at all — to the portal or to the local
	// status panel.
	cpFinish := attachControlPlaneHooks(&opts)
	runFinish := attachRunRegistry(&opts)

	// --- Execute ---------------------------------------------------------

	ctx, cancel := context.WithCancel(context.Background())
	opts.Ctx = ctx
	a.setBackupCancel(cancel)
	defer func() {
		a.setBackupCancel(nil)
		cancel()
	}()

	if req.BackupType == "machine" {
		writeDebugLog("[Pipeline] Executing full-volume backup via RunMachineBackup")
		err = RunMachineBackup(opts)
	} else {
		writeDebugLog("[Pipeline] Executing backup via RunBackupInline")
		err = RunBackupInline(opts)
	}

	cpFinish(err)
	runFinish(err)
	return err
}
