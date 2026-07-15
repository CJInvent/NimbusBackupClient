//go:build !service
// +build !service

package main

// download.go — "Download" support for the Browse tab: extract a selection
// from a snapshot to a user-chosen location, as a single file or a zip
// (folder / multi-select). Reuses the existing restore machinery
// (RestoreSnapshotInline) to do the PBS extraction into a temp staging dir,
// then packages from there.
//
// Space safety (the rules, exactly):
//   - BLOCK when the download would not fit: needed > free (on either the
//     staging temp drive or the destination drive).
//   - WARN when it fits but would push the destination drive to >= 90% used:
//     used_after = total - free + needed;  warn if used_after >= 0.90 * total.
// The frontend pre-flights CheckDownloadSpace for UX (warning dialog), and
// DownloadSelection re-checks server-side before any bytes move — the Go
// check is authoritative, the JS one is advisory.

import (
	"errors"
	"fmt"
	"os"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// CheckDownloadSpace is Wails-bound: frontend pre-flight for the warning /
// block UX before starting a download of neededBytes to destPath's drive.
func (a *App) CheckDownloadSpace(destPath string, neededBytes int64) (SpaceCheck, error) {
	if destPath == "" {
		return SpaceCheck{}, errors.New("destination path required")
	}
	if neededBytes < 0 {
		neededBytes = 0
	}
	return evaluateSpace(destPath, uint64(neededBytes))
}

// OpenSaveFileDialog is Wails-bound: native "save as" picker. Same headless-
// service guard as OpenRestoreDestDialog (native pickers fault in session 0).
// OpenSaveFileDialog is RETIRED. The Wails native Save dialog takes a native
// COM fault in this app — the process dies outright (tray icon and all) with
// no Go panic to log and nothing recover() can catch. A backup tool cannot
// ship a button that kills the process, so the picker is now rendered in the
// webview over ListDrives/ListFolders/CreateFolder (see pathpicker.go), which
// is pure Go and cannot fault. This stub stays so any stale caller gets a
// clear error instead of resurrecting the crash.
func (a *App) OpenSaveFileDialog(_ string) (string, error) {
	return "", errors.New("[NB-3009] the native save dialog is disabled — use the in-app picker")
}

// DownloadSelection extracts includePaths from a snapshot and writes them to
// destPath. asZip=true packages the selection into a zip at destPath (used
// for folders and multi-select); asZip=false expects the selection to be a
// single file, written directly to destPath.
//
// neededBytes is the frontend's computed selection size (uncompressed upper
// bound). Space is enforced here regardless of what the frontend showed.
func (a *App) DownloadSelection(pbsID, backupID, snapshotID string,
	includePaths []string, destPath string, asZip bool, neededBytes int64) error {

	writeDebugLog(fmt.Sprintf("DownloadSelection(pbs=%s, backup=%s, snap=%s, includes=%d, dest=%s, zip=%v, needed=%d)",
		pbsID, backupID, snapshotID, len(includePaths), destPath, asZip, neededBytes))

	if destPath == "" {
		return errors.New(errDestPathRequired)
	}
	if len(includePaths) == 0 {
		return errors.New("nothing selected to download")
	}
	if !asZip && len(includePaths) != 1 {
		return errors.New("single-file download requires exactly one selected file")
	}
	cfg, err := a.resolveRestorePBS(pbsID)
	if err != nil {
		return err
	}
	timestamp, err := time.Parse("2006-01-02T15:04:05Z", snapshotID)
	if err != nil {
		return fmt.Errorf("ID de snapshot invalide: %v", err)
	}
	needed := uint64(0)
	if neededBytes > 0 {
		needed = uint64(neededBytes)
	}

	// ---- space enforcement (authoritative) --------------------------------
	// Staging temp drive needs the extracted bytes; destination drive needs
	// up to the same again (zip output <= uncompressed input, single file ==
	// its own size). Checking both with the uncompressed size is the safe
	// upper bound.
	tmpParent := os.TempDir()
	if sc, err := evaluateSpace(tmpParent, needed); err == nil {
		if !sc.Fits {
			return fmt.Errorf("[NB-3401] not enough space on the temporary drive (%s): need %s, only %s free",
				tmpParent, formatBytesGo(needed), formatBytesGo(sc.FreeBytes))
		}
	} else {
		writeDebugLog(fmt.Sprintf("DownloadSelection: temp space check failed (continuing): %v", err))
	}
	sc, err := evaluateSpace(destPath, needed)
	if err != nil {
		return fmt.Errorf("[NB-3402] cannot check free space for %s: %v", destPath, err)
	}
	if !sc.Fits {
		return fmt.Errorf("[NB-3403] not enough space on the destination drive: need %s, only %s free — download blocked",
			formatBytesGo(needed), formatBytesGo(sc.FreeBytes))
	}

	// ---- stage: restore the selection into a temp dir ----------------------
	staging, err := os.MkdirTemp("", "nimbus-dl-*")
	if err != nil {
		return fmt.Errorf("temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	emit := func(percent float64, message string) {
		if a.ctx == nil {
			return
		}
		wailsruntime.EventsEmit(a.ctx, "restore:progress", map[string]interface{}{
			"percent": percent * 0.85, // stage = 85% of the bar; packaging = rest
			"message": message,
		})
	}

	opts := RestoreOptions{
		BaseURL:           cfg.BaseURL,
		AuthID:            cfg.AuthID,
		Secret:            cfg.Secret,
		Datastore:         cfg.Datastore,
		Namespace:         cfg.Namespace,
		CertFingerprint:   cfg.CertFingerprint,
		BackupID:          backupID,
		SnapshotTime:      timestamp,
		DestPath:          staging,
		Mode:              RestoreModeAlternateAbs,
		IncludePaths:      includePaths,
		Overwrite:         true,
		RestoreTimestamps: true,
		OnProgress:        emit,
	}
	if err := RestoreSnapshotInline(opts); err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	// ---- package -----------------------------------------------------------
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "restore:progress", map[string]interface{}{
			"percent": 90.0, "message": "Packaging…",
		})
	}
	if asZip {
		if err := zipDirectory(staging, destPath); err != nil {
			return fmt.Errorf("zip failed: %w", err)
		}
	} else {
		src, err := findSingleFile(staging)
		if err != nil {
			return err
		}
		if err := copyFileTo(src, destPath); err != nil {
			return fmt.Errorf("write failed: %w", err)
		}
	}
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "restore:progress", map[string]interface{}{
			"percent": 100.0, "message": "Download complete",
		})
	}
	writeDebugLog(fmt.Sprintf("DownloadSelection: wrote %s", destPath))
	return nil
}
