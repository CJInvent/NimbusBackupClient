//go:build service
// +build service

package main

// download_service.go — "Download" from the Browse tab, executed in the
// service (docs/V4-RESTORE.md, "the restore rewire").
//
// Unchanged in substance from the version that ran in the console: stage the
// selection into a temp dir with the restore engine, then package from there.
// What changed is WHERE, and one thing follows from it that is worth being
// explicit about.
//
// SPACE IS CHECKED HERE AND THAT IS NOW THE ONLY CHECK THAT COUNTS. The
// console keeps CheckDownloadSpace for its warning dialog, but it is running
// as the user and the service is running as SYSTEM: on a machine with disk
// quotas the two can disagree, and the one that must be right is the one about
// to write the bytes.
//
// Space safety (the rules, exactly, as before):
//   - BLOCK when the download would not fit: needed > free (on either the
//     staging temp drive or the destination drive).
//   - WARN when it fits but would push the destination drive to >= 90% used.
//     The warning is the console's job; blocking is this file's.

import (
	"fmt"
	"os"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// downloadSelection extracts a selection and packages it at p.DestPath.
//
// Progress is reported 0-100 across the whole operation: staging is 85% of the
// bar and packaging the rest, the same split the console drew when it ran this
// itself. The numbers were chosen from how long each phase actually takes;
// they are kept rather than re-derived so the bar behaves as users expect.
func (a *App) downloadSelection(p DownloadParams, progress api.RestoreProgress) error {
	writeDebugLog(fmt.Sprintf("downloadSelection(pbs=%s, backup=%s, includes=%d, dest=%s, zip=%v, needed=%d)",
		p.PBSID, p.BackupID, len(p.IncludePaths), p.DestPath, p.AsZip, p.NeededBytes))

	if p.DestPath == "" {
		return fmt.Errorf("%s", errDestPathRequired)
	}
	if len(p.IncludePaths) == 0 {
		return fmt.Errorf("nothing selected to download")
	}
	if !p.AsZip && len(p.IncludePaths) != 1 {
		return fmt.Errorf("single-file download requires exactly one selected file")
	}

	opts, err := a.restoreOptionsFor(p.SnapshotRef)
	if err != nil {
		return err
	}

	needed := uint64(0)
	if p.NeededBytes > 0 {
		needed = uint64(p.NeededBytes)
	}

	// ---- space enforcement (authoritative) --------------------------------
	// Staging temp drive needs the extracted bytes; destination drive needs
	// up to the same again (zip output <= uncompressed input, single file ==
	// its own size). Checking both with the uncompressed size is the safe
	// upper bound.
	tmpParent := os.TempDir()
	if sc, serr := evaluateSpace(tmpParent, needed); serr == nil {
		if !sc.Fits {
			return fmt.Errorf("[NB-3401] not enough space on the temporary drive (%s): need %s, only %s free",
				tmpParent, formatBytesGo(needed), formatBytesGo(sc.FreeBytes))
		}
	} else {
		writeErrorLog(fmt.Sprintf("downloadSelection: temp space check failed (continuing): %v", serr))
	}
	sc, err := evaluateSpace(p.DestPath, needed)
	if err != nil {
		return fmt.Errorf("[NB-3402] cannot check free space for %s: %v", p.DestPath, err)
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

	opts.DestPath = staging
	opts.Mode = RestoreModeAlternateAbs
	opts.IncludePaths = p.IncludePaths
	opts.Overwrite = true
	opts.RestoreTimestamps = true
	opts.OnProgress = func(pct float64, msg string) {
		// Engine progress is 0-1; staging owns 85% of the bar.
		progress(pct*85, msg, 0, 0, 0, -1)
	}

	if err := RestoreSnapshotInline(opts); err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	// ---- package -----------------------------------------------------------
	progress(90, "Packaging…", 0, 0, 0, -1)
	if p.AsZip {
		if err := zipDirectory(staging, p.DestPath); err != nil {
			return fmt.Errorf("zip failed: %w", err)
		}
	} else {
		src, ferr := findSingleFile(staging)
		if ferr != nil {
			return ferr
		}
		if err := copyFileTo(src, p.DestPath); err != nil {
			return fmt.Errorf("write failed: %w", err)
		}
	}
	progress(100, "Download complete", 0, 0, 0, -1)
	writeDebugLog(fmt.Sprintf("downloadSelection: wrote %s", p.DestPath))
	return nil
}
