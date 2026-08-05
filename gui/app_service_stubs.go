//go:build service
// +build service

// Stubs for service compilation
// These methods are required by api.BackupHandler interface
// Full implementations are in main.go (GUI mode)

package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

// GetConfigWithHostname returns the configuration with hostname
func (a *App) GetConfigWithHostname() map[string]interface{} {
	hostname, _ := os.Hostname()
	result := map[string]interface{}{
		"hostname": hostname,
	}

	if a.config != nil {
		result["baseurl"] = a.config.BaseURL
		result["datastore"] = a.config.Datastore
		result["certfingerprint"] = a.config.CertFingerprint
		result["backup-id"] = a.config.BackupID
	}

	return result
}

// emitAnalysisProgress is a no-op in the service process (no GUI event sink).
func (a *App) emitAnalysisProgress(done, total int, scannedBytes uint64) {}

// StartBackup starts a backup job
// Service implementation using RunBackupInline
func (a *App) StartBackup(backupType string, backupDirs, driveLetters, excludeList []string, backupID string, useVSS bool, compression string) error {
	writeDebugLog(fmt.Sprintf("[Service] StartBackup called: type=%s, dirs=%v, id=%s, vss=%v, compression=%s", backupType, backupDirs, backupID, useVSS, compression))

	if a.config == nil {
		return fmt.Errorf("configuration not loaded")
	}

	// Use hostname as fallback if backupID is empty
	if backupID == "" {
		backupID, _ = os.Hostname()
		writeDebugLog(fmt.Sprintf("[Backup ID] Empty backup-id, using hostname: %s", backupID))
	}

	// Default to "fastest" if compression is empty
	if compression == "" {
		compression = "fastest"
		writeDebugLog("[Compression] Using default: fastest")
	}

	// Merge directories: backupDirs for directory backup, driveLetters for machine backup
	var allDirs []string
	pbsBackupType := "host"
	if backupType == "directory" {
		allDirs = backupDirs
	} else if backupType == "machine" {
		allDirs = driveLetters
		pbsBackupType = "vm"
	}

	// Resolve the EFFECTIVE PBS config: a multi-PBS-only config keeps the legacy
	// BaseURL/AuthID/Secret/Datastore fields empty, so building options from those
	// directly yielded "PBS connection parameters required" in service mode (the GUI
	// standalone path already used EffectivePBS — audit M-01/M-04, reported in prod).
	pbsCfg := a.config.EffectivePBS()

	// Prepare backup options
	var lastRelayedMsg string
	opts := BackupOptions{
		BaseURL:         pbsCfg.BaseURL,
		AuthID:          pbsCfg.AuthID,
		Secret:          pbsCfg.Secret,
		Datastore:       pbsCfg.Datastore,
		Namespace:       pbsCfg.Namespace,
		CertFingerprint: pbsCfg.CertFingerprint,
		BackupDirs:      allDirs,
		BackupID:        backupID,
		BackupType:      pbsBackupType,
		UseVSS:          useVSS,
		Compression:     compression,
		ExcludeList:     excludeList,
		DisableSplit:    pbsCfg.DisableSplit,
		SplitSizeBytes:  pbsCfg.SplitSizeBytes(),
		OnProgress: func(percent float64, message string) {
			// Engines report a 0..1 fraction; the API progress map (and the
			// GUI) use 0-100. Chunk-level messages are throttled to >=0.1%
			// progress steps in the log (a 931GB disk is ~240k chunks; a line
			// per chunk was rewriting megabytes of log). The progress map
			// still gets every update.
			pct := percent * 100
			// The backup ENGINE already logs progress (see machine backup's
			// progress func). This relay logs only phase transitions, worded
			// as what it is: confirmation the GUI callback path delivered.
			if message != "" && message != lastRelayedMsg {
				lastRelayedMsg = message
				writeDebugLog(fmt.Sprintf("Service->GUI relay: %q delivered at %.1f%%", message, pct))
			}
			a.notifyProgressCallbacks(pct, message)
		},
		OnStats: func(stats *BackupProgressStats) {
			a.notifyStatsCallbacks(stats.BytesDone, stats.BytesTotal, stats.NewChunks, stats.ReusedChunks)
		},
		OnComplete: func(success bool, message string) {
			if success {
				writeDebugLog(fmt.Sprintf("[Backup Complete] SUCCESS - %s", message))
				a.maybeRunExchangePostBackup()
			} else {
				writeDebugLog(fmt.Sprintf("[Backup Complete] FAILED - %s", message))
			}
			a.notifyCompleteCallbacks(success, message)

			// Record manual runs to LOCAL history same as the GUI-standalone
			// path (main.go startBackupDirect) already does. Without this,
			// a manual "Back up now" run in the installed-service
			// configuration — every managed machine — never appeared in the
			// client's own history list, even though the control-plane
			// reporter still told the portal about it (takeRunReporter's
			// ad-hoc fallback in controlplane_glue.go). That split is why
			// the two counts disagreed: the portal had every run, the local
			// list only had scheduler-triggered ones.
			historyEntry := JobHistory{
				ID:         fmt.Sprintf("%d", time.Now().Unix()),
				Name:       fmt.Sprintf("Backup manuel - %s", backupID),
				Timestamp:  time.Now().Format(time.RFC3339),
				Status:     "success",
				Message:    message,
				BackupDirs: allDirs,
				BackupID:   backupID,
				UseVSS:     useVSS,
			}
			if !success {
				historyEntry.Status = "failed"
			}
			if err := a.AddJobHistory(historyEntry); err != nil {
				writeDebugLog(fmt.Sprintf("Warning: Failed to add manual backup to history: %v", err))
			}
		},
		UploadLimitMbps: a.config.UploadLimitMbps,
	}

	// Control plane run reporting (no-op when not configured). The returned
	// finalizer MUST be called with the engine's error: RunMachineBackup
	// never emits OnResult, so without it an image backup is never reported
	// as finished at all.
	cpFinish := attachControlPlaneHooks(&opts)
	runFinish := attachRunRegistry(&opts)

	// A cancellable context so a /backup/cancel request can stop this backup
	// cleanly. The engine runs synchronously here (the API server wraps the call
	// in its own goroutine); CancelActiveBackup cancels this ctx, the reader
	// aborts before the index commits, and the VSS snapshot is released.
	ctx, cancel := context.WithCancel(context.Background())
	opts.Ctx = ctx
	a.setBackupCancel(cancel)
	defer func() {
		a.setBackupCancel(nil)
		cancel()
	}()

	// Execute backup using the appropriate engine for the backup type.
	if backupType == "machine" {
		writeDebugLog("[Service] Executing full-volume backup via RunMachineBackup")
		err := RunMachineBackup(opts)
		cpFinish(err)
		runFinish(err)
		return err
	}
	writeDebugLog("[Service] Executing backup via RunBackupInline")
	err := RunBackupInline(opts)
	cpFinish(err)
	runFinish(err)
	return err
}
