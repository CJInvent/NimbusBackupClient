//go:build !service
// +build !service

package main

// restore_bindings.go — the console's restore surface.
//
// Every function here was, until this rewire, a call into a restore engine
// running IN THIS PROCESS. They now say the same things to the service over
// the local API and render what comes back. The frontend's contract is
// unchanged: same method names, same arguments, same events, same shapes — a
// deliberate constraint, because a rewire that also redesigns the UI cannot be
// reviewed for whether it changed behaviour.
//
// WHAT IS DIFFERENT, AND WHY IT MATTERS:
//
//   - This build links no restore engine. Not "gated" — absent. CI asserts it
//     the same way it asserts the absence of the backup engine, so a modified
//     console has nothing to call.
//   - The permission check is no longer here. It is in the service, which is
//     the only place a check on the console can mean anything.
//   - PBS credentials are not read here. The console names a server by id.
//   - The encryption key never enters this process, which is what phase G's
//     open question was actually about (docs/V4-RESTORE.md).
//
// LONG WORK IS POLLED, NOT STREAMED. A restore, a download or a search returns
// a job id and this file polls it, translating each state into the SAME Wails
// event the frontend already listens for. That mirrors how backup runs are
// observed (/runs/active, every three seconds) rather than inventing a second
// mechanism, and it means a console that is restarted mid-restore can pick the
// job back up instead of losing it with the socket.

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"security"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
	runtime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// restoreJobPollInterval is how often a running job is polled.
//
// Half a second rather than the three the run panel uses: this drives a
// progress bar somebody is watching while a file copies, and a bar that
// updates three times a minute reads as a hang. It is a loopback request to a
// process on the same machine.
const restoreJobPollInterval = 500 * time.Millisecond

// requireService is the precondition for every call in this file.
//
// It re-probes, exactly as StartBackup does and for the same reason: the
// service may have started after the console did, and "press it again in a
// minute" beats "restart the application".
func (a *App) requireService() error {
	if a.apiClient == nil {
		return errors.New(errServiceUnavailable)
	}
	if a.mode != api.ModeService && a.apiClient.IsServiceAvailable() {
		writeDebugLog("[Mode] Service now reachable")
		a.mode = api.ModeService
	}
	if a.mode != api.ModeService {
		return errors.New(errServiceUnavailable)
	}
	return nil
}

// restoreQuery runs a bounded operation and decodes it into out.
func (a *App) restoreQuery(op string, params, out any) error {
	if err := a.requireService(); err != nil {
		return err
	}
	raw, err := a.apiClient.RestoreQuery(op, params)
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("the service's answer to %s could not be read: %w", op, err)
	}
	return nil
}

// awaitRestoreJob starts a job and blocks until it finishes, reporting each
// state to onState. The job's raw result is returned.
//
// A POLL THAT FAILS DOES NOT END THE WAIT unless it fails repeatedly: a single
// dropped loopback request during a two-hour restore must not report a restore
// that is still running as failed. Three consecutive failures is the service
// actually being gone.
func (a *App) awaitRestoreJob(op string, params any, onState func(*api.RestoreJobState)) (json.RawMessage, error) {
	if err := a.requireService(); err != nil {
		return nil, err
	}
	id, err := a.apiClient.RestoreJobStart(op, params)
	if err != nil {
		return nil, err
	}

	const maxPollFailures = 3
	failures := 0
	for {
		time.Sleep(restoreJobPollInterval)
		st, err := a.apiClient.RestoreJobState(id)
		if err != nil {
			failures++
			if failures >= maxPollFailures {
				return nil, fmt.Errorf("lost contact with the backup service while %s was running: %w", op, err)
			}
			continue
		}
		failures = 0
		if onState != nil {
			onState(st)
		}
		if !st.Complete {
			continue
		}
		if !st.Success {
			return nil, errors.New(st.Error)
		}
		return st.Result, nil
	}
}

// emitRestoreProgress is the event the Browse tab and the restore dialog both
// listen for. Field names are the union of what the two emitters used before
// (gui/ibemit_gui.go and the old RestoreSnapshot binding), so neither view
// needed changing.
func (a *App) emitRestoreProgress(st *api.RestoreJobState) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "restore:progress", map[string]interface{}{
		"percent":     st.Percent,
		"message":     st.Message,
		"done_bytes":  st.DoneBytes,
		"total_bytes": st.TotalBytes,
		"bps":         st.BPS,
		"eta_seconds": st.ETASeconds,
	})
}

func (a *App) emitRestoreComplete(err error) {
	if a.ctx == nil {
		return
	}
	msg := "Restore completed"
	if err != nil {
		msg = err.Error()
		writeErrorLog(fmt.Sprintf("Restore failed: %v", err))
	}
	runtime.EventsEmit(a.ctx, "restore:complete", map[string]interface{}{
		"success": err == nil,
		"message": msg,
	})
}

// ==================== RESTORE (archive) ====================

// ListSnapshots lists available snapshots on a PBS server, optionally filtered
// by backup ID (partial match supports split backups).
//
// pbsID selects the PBS server. Empty means "use the default server" — kept
// for backward compatibility with the legacy single-PBS UI.
func (a *App) ListSnapshots(pbsID, backupID string) ([]map[string]interface{}, error) {
	writeDebugLog(fmt.Sprintf("ListSnapshots(pbs=%s, backupID=%s)", pbsID, backupID))

	var snaps []SnapshotInfo
	if err := a.restoreQuery(opSnapshots, ListSnapshotsParams{PBSID: pbsID, BackupID: backupID}, &snaps); err != nil {
		writeErrorLog(fmt.Sprintf("ListSnapshots failed: %v", err))
		return nil, fmt.Errorf("%s :: %v", errSnapshotList, err)
	}

	// The frontend's shape, built here rather than in the service: it is
	// presentation, and the service has no business knowing what the tree view
	// wants its keys called.
	result := make([]map[string]interface{}, 0, len(snaps))
	for _, s := range snaps {
		result = append(result, map[string]interface{}{
			"id":          s.BackupTime.UTC().Format("2006-01-02T15:04:05Z"),
			"backup_id":   s.BackupID,
			"backup_type": s.BackupType,
			"time":        s.BackupTime.Format("2006-01-02 15:04:05"),
			"unix":        s.BackupTime.Unix(),
			"files":       s.Files,
		})
	}
	writeDebugLog(fmt.Sprintf("Returning %d snapshots", len(result)))
	return result, nil
}

// ListSnapshotContents returns a snapshot's flat tree of entries. The frontend
// turns this into a navigable view so the user can pick individual files or
// directories before restoring.
//
// snapshotUnix is the snapshot's backup-time as Unix seconds (the `unix` field
// returned by ListSnapshots). Set forceRefresh to bypass the service's listing
// cache — useful for a manual "Reload" action.
func (a *App) ListSnapshotContents(pbsID, backupID string, snapshotUnix int64, forceRefresh bool) ([]SnapshotEntry, error) {
	writeDebugLog(fmt.Sprintf("ListSnapshotContents(pbs=%s, backupID=%s, unix=%d, force=%v)",
		pbsID, backupID, snapshotUnix, forceRefresh))

	var entries []SnapshotEntry
	err := a.restoreQuery(opContents, ListContentsParams{
		SnapshotRef:  SnapshotRef{PBSID: pbsID, BackupID: backupID, SnapshotUnix: snapshotUnix},
		ForceRefresh: forceRefresh,
	}, &entries)
	return entries, err
}

// GetSnapshotMeta returns the `.nimbus_backup_meta.json` sidecar from a
// snapshot. Returns nil (not an error) when the snapshot predates the sidecar
// — the frontend should fall back to a generic banner in that case.
func (a *App) GetSnapshotMeta(pbsID, backupID string, snapshotUnix int64) (*BackupMeta, error) {
	writeDebugLog(fmt.Sprintf("GetSnapshotMeta(pbs=%s, backupID=%s, unix=%d)",
		pbsID, backupID, snapshotUnix))

	var meta *BackupMeta
	err := a.restoreQuery(opMeta, SnapshotMetaParams{
		SnapshotRef: SnapshotRef{PBSID: pbsID, BackupID: backupID, SnapshotUnix: snapshotUnix},
	}, &meta)
	return meta, err
}

// RestoreSnapshot extracts a snapshot (or selected files) according to mode.
//
//   - mode "original": restore in-place to the path captured in the snapshot's
//     .nimbus_backup_meta.json sidecar. destPath is ignored. Cross-host
//     attempts are refused unless allowCrossHost is true.
//   - mode "alternate_abs" (or empty): write to destPath, preserving the full
//     archive directory layout below it.
//   - mode "alternate_flat": write to destPath stripping the longest common
//     prefix of the selection — useful for restoring a single file as
//     destPath/<basename>.
//
// includePaths uses archive-style paths (forward slash). When empty the entire
// snapshot is restored. The ACL/ADS/timestamps flags are accepted today but
// only timestamps is effective — the per-file NTFS sidecar required for the
// other two is still on the roadmap.
//
// Returns as soon as the service has accepted the job. Progress arrives on
// "restore:progress" and the outcome on "restore:complete", exactly as before.
func (a *App) RestoreSnapshot(pbsID, backupID, snapshotID, destPath, mode string,
	includePaths []string, allowCrossHost, restoreACLs, restoreADS, restoreTimestamps, overwrite bool) error {
	writeDebugLog(fmt.Sprintf("RestoreSnapshot(pbs=%s, backupID=%s, snap=%s, mode=%s, dest=%s, includes=%d, crossHost=%v, acl=%v, ads=%v, ts=%v, overwrite=%v)",
		pbsID, backupID, snapshotID, mode, destPath, len(includePaths), allowCrossHost, restoreACLs, restoreADS, restoreTimestamps, overwrite))

	if backupID == "" {
		return errors.New(errBackupIDRequired)
	}
	if snapshotID == "" {
		return errors.New(errSnapshotIDRequired)
	}
	// Destination is validated for alternate modes only; in-place derives its
	// target from the metadata sidecar. Validated HERE as well as in the
	// service because this is where the user's own path came from and a bad
	// one should be refused before a job exists.
	if RestoreMode(mode) != RestoreModeOriginal {
		if destPath == "" {
			return errors.New(errDestPathRequired)
		}
		if err := security.ValidatePath(destPath); err != nil {
			return fmt.Errorf("chemin de destination invalide: %w", err)
		}
	}
	if err := a.requireService(); err != nil {
		return err
	}

	params := RestoreParams{
		SnapshotRef:       SnapshotRef{PBSID: pbsID, BackupID: backupID, SnapshotID: snapshotID},
		DestPath:          destPath,
		Mode:              mode,
		IncludePaths:      includePaths,
		AllowCrossHost:    allowCrossHost,
		RestoreACLs:       restoreACLs,
		RestoreADS:        restoreADS,
		RestoreTimestamps: restoreTimestamps,
		Overwrite:         overwrite,
	}
	go func() {
		_, err := a.awaitRestoreJob(opRestore, params, a.emitRestoreProgress)
		a.emitRestoreComplete(err)
	}()
	return nil
}

// SearchFiles scans every backup-id matching hostPrefix over the given period
// for entries matching query, and returns the matches. mode is one of "name"
// (substring on file name), "regex", or "path" (substring on full path).
//
// fromUnix/toUnix bound the snapshot period in Unix seconds; pass 0 for an
// open end. When assembleMissing is true, snapshots not already in the
// service's listing cache are downloaded + assembled (slow, needs temp space);
// otherwise only cached snapshots are searched. Progress is streamed via
// "search:progress".
//
// BLOCKS UNTIL THE SEARCH FINISHES, as it always has: the frontend awaits the
// result. The wait is a poll of a service-side job rather than an in-process
// call, so a console restarted mid-search loses its view of the search and not
// the search itself.
func (a *App) SearchFiles(pbsID, hostPrefix, query, mode string, fromUnix, toUnix int64, assembleMissing bool) (*SearchResult, error) {
	writeDebugLog(fmt.Sprintf("SearchFiles(pbs=%s, prefix=%s, query=%q, mode=%s, from=%d, to=%d, assemble=%v)",
		pbsID, hostPrefix, query, mode, fromUnix, toUnix, assembleMissing))

	raw, err := a.awaitRestoreJob(opSearch, SearchParams{
		PBSID:           pbsID,
		HostPrefix:      hostPrefix,
		Query:           query,
		Mode:            mode,
		FromUnix:        fromUnix,
		ToUnix:          toUnix,
		AssembleMissing: assembleMissing,
	}, func(st *api.RestoreJobState) {
		if a.ctx == nil {
			return
		}
		runtime.EventsEmit(a.ctx, "search:progress", map[string]interface{}{
			"percent": st.Percent,
			"message": st.Message,
		})
	})
	if err != nil {
		return nil, err
	}
	var result *SearchResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("the search finished but its results could not be read: %w", err)
	}
	return result, nil
}

// CancelSearch asks an in-flight SearchFiles to stop at the next snapshot
// boundary. The call returning does not mean the search has stopped yet — the
// search returns its partial result with Cancelled=true.
func (a *App) CancelSearch() {
	writeDebugLog("CancelSearch requested")
	if err := a.requireService(); err != nil {
		return
	}
	if err := a.apiClient.RestoreControl(opCancelSearch, nil); err != nil {
		writeErrorLog(fmt.Sprintf("CancelSearch failed: %v", err))
	}
}

// DownloadSelection extracts includePaths from a snapshot and writes them to
// destPath. asZip=true packages the selection into a zip at destPath (used for
// folders and multi-select); asZip=false expects the selection to be a single
// file, written directly to destPath.
//
// neededBytes is the frontend's computed selection size (uncompressed upper
// bound). Space is enforced in the SERVICE regardless of what this console
// showed — see download_service.go.
func (a *App) DownloadSelection(pbsID, backupID, snapshotID string,
	includePaths []string, destPath string, asZip bool, neededBytes int64) error {

	writeDebugLog(fmt.Sprintf("DownloadSelection(pbs=%s, backup=%s, snap=%s, includes=%d, dest=%s, zip=%v, needed=%d)",
		pbsID, backupID, snapshotID, len(includePaths), destPath, asZip, neededBytes))

	_, err := a.awaitRestoreJob(opDownload, DownloadParams{
		SnapshotRef:  SnapshotRef{PBSID: pbsID, BackupID: backupID, SnapshotID: snapshotID},
		IncludePaths: includePaths,
		DestPath:     destPath,
		AsZip:        asZip,
		NeededBytes:  neededBytes,
	}, a.emitRestoreProgress)
	return err
}

// ==================== BROWSE (volume backups) ====================

// ListVolumePartitions enumerates every partition on a disk image — regardless
// of whether it can be browsed — with its filesystem, allocated size, and used
// size. The user chooses; we never choose for them.
func (a *App) ListVolumePartitions(pbsID, backupID, snapshotID, backupType, diskArchive string) ([]VolumePartition, error) {
	var parts []VolumePartition
	err := a.restoreQuery(opVolumePartitions, VolumeRef{
		PBSID: pbsID, BackupID: backupID, SnapshotID: snapshotID,
		BackupType: backupType, DiskArchive: diskArchive,
	}, &parts)
	return parts, err
}

// ListVolumeFiles scans one partition's file table and returns the ROOT
// directory listing. The full tree stays in the SERVICE's session cache —
// shipping a million entries of JSON into the webview is what forced the old
// truncation; per-directory listing (ListVolumeDirectory) has no such limit.
func (a *App) ListVolumeFiles(pbsID, backupID, snapshotID, backupType, diskArchive string,
	partIndex int, forceRefresh bool) ([]SnapshotEntry, error) {

	var out VolumeFilesResult
	err := a.restoreQuery(opVolumeFiles, VolumeFilesParams{
		VolumeRef: VolumeRef{
			PBSID: pbsID, BackupID: backupID, SnapshotID: snapshotID,
			BackupType: backupType, DiskArchive: diskArchive, PartIndex: partIndex,
		},
		ForceRefresh: forceRefresh,
	}, &out)
	if err != nil {
		return nil, err
	}
	a.lastVolumeTruncated = out.Truncated
	return out.Entries, nil
}

// ListVolumeDirectory returns the immediate children of dir from the scan the
// service is holding — the whole point of keeping the tree there: the webview
// only ever holds one directory's worth of rows, so nothing needs truncating.
func (a *App) ListVolumeDirectory(pbsID, backupID, snapshotID, backupType, diskArchive string,
	partIndex int, dir string) ([]SnapshotEntry, error) {

	var entries []SnapshotEntry
	err := a.restoreQuery(opVolumeDirectory, VolumeDirectoryParams{
		VolumeRef: VolumeRef{
			PBSID: pbsID, BackupID: backupID, SnapshotID: snapshotID,
			BackupType: backupType, DiskArchive: diskArchive, PartIndex: partIndex,
		},
		Dir: dir,
	}, &entries)
	return entries, err
}

// LastVolumeListTruncated reports whether the last ListVolumeFiles hit the
// service's entry cap.
func (a *App) LastVolumeListTruncated() bool { return a.lastVolumeTruncated }

// DownloadFilesFromVolume packages the selection as a ZIP at destPath, streamed
// in one pass by the service: PBS chunks -> filesystem parser -> zip entry ->
// disk, nothing staged anywhere.
func (a *App) DownloadFilesFromVolume(pbsID, backupID, snapshotID, backupType, diskArchive string, partIndex int,
	includePaths []string, destPath string, asZip bool, neededBytes int64) error {

	writeDebugLog(fmt.Sprintf("DownloadFilesFromVolume(disk=%s part=%d includes=%d dest=%s needed=%d)",
		diskArchive, partIndex, len(includePaths), destPath, neededBytes))

	_, err := a.awaitRestoreJob(opVolumeDownload, VolumeDownloadParams{
		VolumeRef: VolumeRef{
			PBSID: pbsID, BackupID: backupID, SnapshotID: snapshotID,
			BackupType: backupType, DiskArchive: diskArchive, PartIndex: partIndex,
		},
		IncludePaths: includePaths,
		DestPath:     destPath,
		AsZip:        asZip,
		NeededBytes:  neededBytes,
	}, a.emitRestoreProgress)
	return err
}

// RestoreFilesFromVolume restores selected files from a volume backup INTO a
// destination folder (not a zip). The restoreMtimes / restoreACLs / restoreADS
// options only have meaning when the SOURCE stores them (NTFS); the service
// treats them as best-effort per file.
func (a *App) RestoreFilesFromVolume(pbsID, backupID, snapshotID, backupType, diskArchive string, partIndex int,
	includePaths []string, destDir string, keepStructure, overwrite bool,
	restoreMtimes, restoreACLs, restoreADS bool, neededBytes int64) error {

	writeDebugLog(fmt.Sprintf("RestoreFilesFromVolume(disk=%s part=%d includes=%d dest=%s keep=%v overwrite=%v mtime=%v acl=%v ads=%v)",
		diskArchive, partIndex, len(includePaths), destDir, keepStructure, overwrite, restoreMtimes, restoreACLs, restoreADS))

	_, err := a.awaitRestoreJob(opVolumeFileRestore, VolumeFileRestoreParams{
		VolumeRef: VolumeRef{
			PBSID: pbsID, BackupID: backupID, SnapshotID: snapshotID,
			BackupType: backupType, DiskArchive: diskArchive, PartIndex: partIndex,
		},
		IncludePaths:  includePaths,
		DestDir:       destDir,
		KeepStructure: keepStructure,
		Overwrite:     overwrite,
		RestoreMtimes: restoreMtimes,
		RestoreACLs:   restoreACLs,
		RestoreADS:    restoreADS,
		NeededBytes:   neededBytes,
	}, a.emitRestoreProgress)
	return err
}

// CancelVolumeFileRestore aborts an in-progress volume-file download or
// restore. The
// service's loop checks between files, so cancellation takes effect at the
// next file boundary (a large file in flight finishes its current write).
func (a *App) CancelVolumeFileRestore() {
	if err := a.requireService(); err != nil {
		return
	}
	if err := a.apiClient.RestoreControl(opCancelVolumeFileRestore, nil); err != nil {
		writeErrorLog(fmt.Sprintf("CancelVolumeFileRestore failed: %v", err))
	}
}
