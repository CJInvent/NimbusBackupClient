//go:build service
// +build service

// Stubs for service compilation
// These methods are required by api.BackupHandler interface
// Full implementations are in main.go (GUI mode)

package main

import (
	"errors"
	"fmt"
	"os"

	"controlplane"
	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
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

// StartBackup starts a backup somebody asked for AT THE MACHINE: the GUI's
// button, or the local API.
//
// ON AN ENROLLED MACHINE IT ASKS THE SERVER FIRST (V4-RUN-AUDIT 1.3a): the
// server answers a request id -- and in data path mode the grant that is
// this run's only way to PBS -- or refuses, and the refusal is this call's
// error, which the status panel shows. Then the run takes the same reporter,
// gate and engine as a run_backup the portal sent. Standalone: no handshake.
//
// Server unreachable: no request id, no run -- except through the existing
// outage override for unmanaged work (direct mode only; in data path mode
// there is nothing to back up to without the server).
func (a *App) StartBackup(backupType string, backupDirs, diskTargets, excludeList []string, backupID string, useVSS bool, compression string) error {
	if !UnmanagedBackupsPermitted() {
		return ErrUnmanagedBackupsDisabled
	}
	if c := currentControlPlaneClient(); c != nil {
		const jobName = "Manual backup"
		resp, err := c.RequestBackup(controlplane.BackupRequest{JobName: jobName, Managed: false,
			Sources: directorySources(backupType, backupDirs)})
		var refused *controlplane.RefusedError
		switch {
		case err == nil:
			stashRunGrant(resp.RequestID, resp.Grant)
			registerRunReporter(backupID, jobName, backupType, resp.RequestID, api.TriggerManual, 0)
		case errors.As(err, &refused) || datapathProxy() || !unmanagedOverrideInEffect():
			writeWarnLog(fmt.Sprintf("[Backup] not started: the control server did not allow it: %v", err))
			return fmt.Errorf("the control server did not allow this backup: %w", err)
		default:
			writeWarnLog(fmt.Sprintf("[Backup] the control server is unreachable (%v); running under the local outage override without a request id", err))
		}
	}
	return a.runJob(backupType, backupDirs, diskTargets, excludeList, backupID, useVSS, compression)
}

// runJob runs a job's backup in this process: the pipeline itself. Reached
// from executeJob (whose own gates already ran) and from StartBackup.
func (a *App) runJob(backupType string, backupDirs, diskTargets, excludeList []string, backupID string, useVSS bool, compression string) error {
	return a.runBackupPipeline(backupRequest{
		BackupType:  backupType,
		BackupDirs:  backupDirs,
		DiskTargets: diskTargets,
		ExcludeList: excludeList,
		BackupID:    backupID,
		UseVSS:      useVSS,
		Compression: compression,
	})
}

// unmanagedOverrideInEffect reports whether the local outage override is
// what would permit unmanaged work right now (the control server unreachable
// long enough, and the override requested on this machine).
func unmanagedOverrideInEffect() bool {
	if cpAgent == nil {
		return false
	}
	allowed, viaOverride := cpAgent.UnmanagedBackupsEligible(unmanagedBackupsOverrideRequested(), 0)
	return allowed && viaOverride
}
