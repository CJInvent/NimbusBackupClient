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
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// SpaceCheck is the result of a free-space evaluation for a pending write of
// neededBytes onto the drive holding path.
type SpaceCheck struct {
	Path          string  `json:"path"`
	FreeBytes     uint64  `json:"free_bytes"`
	TotalBytes    uint64  `json:"total_bytes"`
	NeededBytes   uint64  `json:"needed_bytes"`
	Fits          bool    `json:"fits"`    // needed <= free
	Warn90        bool    `json:"warn_90"` // fits, but usage after >= 90%
	UsageAfterPct float64 `json:"usage_after_pct"`
}

func evaluateSpace(path string, needed uint64) (SpaceCheck, error) {
	free, total, err := driveSpace(path)
	if err != nil {
		return SpaceCheck{}, err
	}
	sc := SpaceCheck{Path: path, FreeBytes: free, TotalBytes: total, NeededBytes: needed}
	sc.Fits = needed <= free
	if total > 0 {
		// used_after = total - free + needed. Guard the arithmetic: all
		// uint64, and needed > free is already the !Fits case (no subtraction
		// underflow possible in the used computation: total >= free always).
		usedAfter := (total - free) + needed
		sc.UsageAfterPct = float64(usedAfter) / float64(total) * 100.0
		sc.Warn90 = sc.Fits && float64(usedAfter) >= 0.90*float64(total)
	}
	return sc, nil
}

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
func (a *App) OpenSaveFileDialog(defaultName string) (string, error) {
	if a.ctx == nil {
		return "", fmt.Errorf("runtime non disponible")
	}
	if a.isServiceProcess {
		return "", errors.New(errFolderPickerSvc)
	}
	return wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		DefaultFilename:      defaultName,
		Title:                "Download",
		CanCreateDirectories: true,
	})
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

// zipDirectory writes every file under root into a zip at destZip, with
// paths relative to root (forward slashes, per the zip spec).
func zipDirectory(root, destZip string) error {
	out, err := os.Create(destZip)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			// Explicit dir entries keep empty folders.
			_, err := zw.Create(rel + "/")
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = rel
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = io.Copy(w, f)
		return err
	})
	if cerr := zw.Close(); walkErr == nil {
		walkErr = cerr
	}
	if cerr := out.Close(); walkErr == nil {
		walkErr = cerr
	}
	if walkErr != nil {
		_ = os.Remove(destZip) // no half-written zips
	}
	return walkErr
}

// findSingleFile returns the path of the only regular file under root.
func findSingleFile(root string) (string, error) {
	var found string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if found != "" {
			return fmt.Errorf("expected a single file, found several")
		}
		found = path
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("extraction produced no file")
	}
	return found, nil
}

func copyFileTo(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// formatBytesGo mirrors the frontend's formatBytes for error strings.
func formatBytesGo(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	if exp >= len(units) {
		exp = len(units) - 1
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(b)/float64(div)), ".0") + " " + units[exp]
}
