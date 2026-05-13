package main

import (
	"fmt"
	"strings"
	"time"

	"pbscommon"
)

// RestoreOptions contains all parameters for a restore operation.
//
// IncludePaths is the list of archive-relative paths to extract. Empty means
// "extract everything in the snapshot". Selecting a directory implies all
// descendants. Paths use forward slashes (archive style); backslashes are
// accepted and normalized.
//
// RestoreACLs / RestoreADS / RestoreTimestamps are reserved for the upcoming
// NTFS sidecar work — accepted today so the API surface is stable, but only
// RestoreTimestamps has any effect (always-on: mtime is restored). The other
// two are no-ops until the per-file .nimbus_meta sidecar lands.
type RestoreOptions struct {
	BaseURL         string
	AuthID          string
	Secret          string
	Datastore       string
	Namespace       string
	CertFingerprint string
	BackupID        string
	SnapshotTime    time.Time
	DestPath        string

	IncludePaths      []string
	Overwrite         bool
	RestoreACLs       bool // reserved — requires NTFS sidecar
	RestoreADS        bool // reserved — requires NTFS sidecar
	RestoreTimestamps bool // mtime is always restored; flag kept for symmetry

	OnProgress func(percent float64, message string)
}

// SnapshotInfo contains information about a backup snapshot.
type SnapshotInfo struct {
	BackupType string
	BackupID   string
	BackupTime time.Time
	Size       int64
	Files      []string
}

// SnapshotEntry is a single file or directory inside a snapshot, suitable for
// driving a tree view in the GUI.
type SnapshotEntry struct {
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    uint64 `json:"size"`
	ModTime int64  `json:"mtime"`
}

// ListSnapshotsInline lists available snapshots from PBS.
// SECURITY: Only lists snapshots from the specified PBS server/datastore/namespace
// to prevent cross-server snapshot access.
func ListSnapshotsInline(baseURL, authID, secret, datastore, namespace, certFingerprint, backupID string) ([]SnapshotInfo, error) {
	writeBackupLog(fmt.Sprintf("Listing snapshots for backup ID: %s on %s/%s/%s", backupID, baseURL, datastore, namespace))

	client := &pbscommon.PBSClient{
		BaseURL:          baseURL,
		CertFingerPrint:  certFingerprint,
		AuthID:           authID,
		Secret:           secret,
		Datastore:        datastore,
		Namespace:        namespace,
		Insecure:         certFingerprint != "",
		CompressionLevel: pbscommon.CompressionFastest,
		Manifest: pbscommon.BackupManifest{
			BackupID: backupID,
		},
	}

	manifests, err := client.ListSnapshots()
	if err != nil {
		writeBackupLog(fmt.Sprintf("Failed to list snapshots: %v", err))
		return nil, fmt.Errorf("failed to list snapshots: %v", err)
	}

	result := make([]SnapshotInfo, 0)
	for _, m := range manifests {
		// Partial match supports split backups: searching "JDS-SRV-1" matches
		// "JDS-SRV-1_D_DATA" or "JDS-SRV-1_PART-A".
		if backupID != "" && !strings.Contains(m.BackupID, backupID) {
			continue
		}

		info := SnapshotInfo{
			BackupType: m.BackupType,
			BackupID:   m.BackupID,
			BackupTime: time.Unix(m.BackupTime, 0),
			Size:       0,
			Files:      make([]string, 0, len(m.Files)),
		}
		for _, f := range m.Files {
			info.Files = append(info.Files, f.Filename)
		}
		result = append(result, info)
	}

	writeBackupLog(fmt.Sprintf("Found %d snapshots", len(result)))
	return result, nil
}

// ListSnapshotContentsInline downloads a snapshot's PXAR archive and returns
// its tree of entries (files + directories) without extracting anything to disk.
// Used by the GUI to power the restore navigation tree.
//
// archiveName defaults to "backup.pxar.didx" when empty.
func ListSnapshotContentsInline(opts RestoreOptions, archiveName string) ([]SnapshotEntry, error) {
	if archiveName == "" {
		archiveName = "backup.pxar.didx"
	}
	writeBackupLog(fmt.Sprintf("Listing contents: backupID=%s snapshot=%s archive=%s",
		opts.BackupID, opts.SnapshotTime.Format(time.RFC3339), archiveName))

	if opts.BaseURL == "" || opts.AuthID == "" || opts.Secret == "" {
		return nil, fmt.Errorf("PBS connection parameters required")
	}
	if opts.BackupID == "" {
		return nil, fmt.Errorf("backup ID required")
	}
	if opts.Datastore == "" {
		return nil, fmt.Errorf("datastore required")
	}

	client := &pbscommon.PBSClient{
		BaseURL:          opts.BaseURL,
		CertFingerPrint:  opts.CertFingerprint,
		AuthID:           opts.AuthID,
		Secret:           opts.Secret,
		Datastore:        opts.Datastore,
		Namespace:        opts.Namespace,
		Insecure:         opts.CertFingerprint != "",
		CompressionLevel: pbscommon.CompressionFastest,
		Manifest: pbscommon.BackupManifest{
			BackupID:   opts.BackupID,
			BackupTime: opts.SnapshotTime.Unix(),
		},
	}

	client.Connect(true, "host")
	defer client.Close()

	pxarData, err := client.AssembleDIDX(archiveName, 8, func(done, total int) {
		if done == total || done%32 == 0 {
			writeBackupLog(fmt.Sprintf("Listing: assembled %d/%d chunks of %s", done, total, archiveName))
		}
	})
	if err != nil {
		writeBackupLog(fmt.Sprintf("Failed to assemble PXAR for listing: %v", err))
		return nil, fmt.Errorf("failed to assemble archive: %v", err)
	}

	reader := pbscommon.NewPXARReader(pxarData)
	entries, err := reader.ListEntries()
	if err != nil {
		return nil, fmt.Errorf("failed to parse archive: %v", err)
	}

	result := make([]SnapshotEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, SnapshotEntry{
			Path:    e.Path,
			IsDir:   e.IsDir,
			Size:    e.Size,
			ModTime: e.ModTime,
		})
	}
	writeBackupLog(fmt.Sprintf("Listed %d entries in snapshot", len(result)))
	return result, nil
}

// RestoreSnapshotInline restores a snapshot from PBS.
// SECURITY: Only restores from the configured PBS server/datastore/namespace.
// Snapshots from other servers will fail with HTTP 404.
//
// When opts.IncludePaths is non-empty, only the matching files and directories
// are extracted. Otherwise the whole snapshot is restored.
func RestoreSnapshotInline(opts RestoreOptions) error {
	writeBackupLog(fmt.Sprintf("Starting restore: snapshot=%s, dest=%s, includes=%d, overwrite=%v from %s/%s/%s",
		opts.SnapshotTime.Format("2006-01-02T15:04:05Z"), opts.DestPath, len(opts.IncludePaths), opts.Overwrite,
		opts.BaseURL, opts.Datastore, opts.Namespace))

	progress := func(pct float64, msg string) {
		writeBackupLog(fmt.Sprintf("Restore progress: %.1f%% - %s", pct*100, msg))
		if opts.OnProgress != nil {
			opts.OnProgress(pct, msg)
		}
	}

	if opts.BaseURL == "" || opts.AuthID == "" || opts.Secret == "" {
		return fmt.Errorf("PBS connection parameters required")
	}
	if opts.BackupID == "" {
		return fmt.Errorf("backup ID required")
	}
	if opts.DestPath == "" {
		return fmt.Errorf("destination path required")
	}
	if opts.Datastore == "" {
		return fmt.Errorf("datastore required for security")
	}

	progress(0.05, "Connecting to PBS...")

	client := &pbscommon.PBSClient{
		BaseURL:          opts.BaseURL,
		CertFingerPrint:  opts.CertFingerprint,
		AuthID:           opts.AuthID,
		Secret:           opts.Secret,
		Datastore:        opts.Datastore,
		Namespace:        opts.Namespace,
		Insecure:         opts.CertFingerprint != "",
		CompressionLevel: pbscommon.CompressionFastest,
		Manifest: pbscommon.BackupManifest{
			BackupID:   opts.BackupID,
			BackupTime: opts.SnapshotTime.Unix(),
		},
	}

	client.Connect(true, "host")
	// Always release the H2 connection so PBS frees the snapshot lock without
	// waiting for TCP keepalive.
	defer client.Close()
	progress(0.10, "Connected to PBS")

	progress(0.20, "Downloading backup archive...")
	// AssembleDIDX downloads the .didx index and reassembles the actual PXAR
	// stream by fetching each referenced chunk. DownloadToBytes alone returns
	// only the index, which would crash the PXAR parser.
	pxarData, err := client.AssembleDIDX("backup.pxar.didx", 8, func(done, total int) {
		// Map chunk progress to the 0.20–0.80 portion of the overall bar.
		if total == 0 {
			return
		}
		pct := 0.20 + 0.60*(float64(done)/float64(total))
		progress(pct, fmt.Sprintf("Downloading archive (%d/%d chunks)", done, total))
	})
	if err != nil {
		writeBackupLog(fmt.Sprintf("Failed to assemble PXAR: %v", err))
		return fmt.Errorf("failed to assemble backup archive: %v", err)
	}
	writeBackupLog(fmt.Sprintf("Assembled %d bytes", len(pxarData)))
	progress(0.80, "Archive assembled")

	progress(0.85, "Extracting files...")

	reader := pbscommon.NewPXARReader(pxarData)
	extracted, err := reader.ExtractFiltered(opts.DestPath, opts.IncludePaths, opts.Overwrite)
	if err != nil {
		writeBackupLog(fmt.Sprintf("PXAR extraction failed: %v", err))
		return fmt.Errorf("failed to extract archive: %v", err)
	}

	successCount := 0
	skipCount := 0
	dirCount := 0
	for _, f := range extracted {
		if f.Skipped {
			skipCount++
			writeBackupLog(fmt.Sprintf("SKIPPED: %s - %s", f.Path, f.SkipReason))
		} else if f.IsDir {
			dirCount++
		} else {
			successCount++
		}
	}

	writeBackupLog(fmt.Sprintf("Extraction complete: %d files, %d dirs, %d skipped",
		successCount, dirCount, skipCount))
	progress(0.95, fmt.Sprintf("Extracted %d files", successCount))

	if opts.RestoreACLs || opts.RestoreADS {
		// Reserved options — sidecar metadata isn't written by the backup yet.
		// Log the request so it shows up in support transcripts.
		writeBackupLog("NOTE: ACL/ADS restore requested but not yet implemented (NTFS sidecar pending)")
	}

	progress(1.0, "Restore completed")

	if skipCount > 0 {
		return fmt.Errorf("restore completed with %d skipped files (see logs)", skipCount)
	}
	return nil
}
