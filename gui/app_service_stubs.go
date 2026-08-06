//go:build service
// +build service

// Stubs for service compilation
// These methods are required by api.BackupHandler interface
// Full implementations are in main.go (GUI mode)

package main

import (
	"os"
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

// StartBackup starts a backup job.
//
// The service process IS the pipeline: it executes here, in-process. All this
// does is hand the request over — the assembly, validation and execution that
// used to be ~150 lines of near-duplicate live in runBackupPipeline
// (backup_pipeline.go), shared with the GUI build so the two cannot drift
// again.
func (a *App) StartBackup(backupType string, backupDirs, driveLetters, excludeList []string, backupID string, useVSS bool, compression string) error {
	// Everything reaching this method was started AT THE MACHINE — the GUI's
	// button, or the local API. Control-plane commands run scheduled jobs
	// through executeScheduledJob and do not come this way, so gating here
	// cannot block the org's own work.
	if !UnmanagedBackupsPermitted() {
		return ErrUnmanagedBackupsDisabled
	}
	return a.runBackupPipeline(backupRequest{
		BackupType:   backupType,
		BackupDirs:   backupDirs,
		DriveLetters: driveLetters,
		ExcludeList:  excludeList,
		BackupID:     backupID,
		UseVSS:       useVSS,
		Compression:  compression,
	})
}
