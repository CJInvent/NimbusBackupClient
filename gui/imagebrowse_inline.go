//go:build !service
// +build !service

package main

// imagebrowse_inline.go — Browse-tab support for VOLUME (machine/image)
// backups: list partitions, walk the NTFS file tree, and download selections
// out of a raw disk image, without restoring the image.
//
// Data path: PBS reader session → pbscommon.FIDXReaderAt (lazy, chunk-LRU)
// → imagebrowse partition parse → go-ntfs over an io.SectionReader. Only the
// chunks the MFT walk / file extraction actually touch are downloaded — a
// directory listing on a multi-TB image moves megabytes, not terabytes.
// Pure userspace parsing: no mount, no driver, no admin ("sandboxed mount").
//
// Space safety for downloads is IDENTICAL to download.go and enforced here
// again server-side (the JS preflight is advisory): BLOCK when needed > free
// on staging temp or destination; the frontend warns at >= 90% usage after.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"imagebrowse"
	"pbscommon"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ibFail logs an image-browse failure to the backup log (so it is visible in
// C:\ProgramData\NimbusBackup logs for local and, later, control-plane
// diagnostics) and returns the error unchanged. EVERY error path in this file
// goes through it — the v0.2.140 lesson: an error only shown in a GUI alert
// is invisible to remote support.
func ibFail(err error) error {
	if err != nil {
		writeBackupLog("ImageBrowse ERROR: " + err.Error())
	}
	return err
}

// normalizeImageBackupType validates/defaults the snapshot's backup type for
// the reader session. Volume backups are uploaded as "vm" (drive-sataN.img);
// directory backups are "host". The frontend passes the snapshot's own
// backup_type — this guard only protects against an empty/garbage value.
func normalizeImageBackupType(bt string) string {
	switch bt {
	case "vm", "host", "ct":
		return bt
	default:
		return "vm" // image archives are produced by machine (vm-type) backups
	}
}

// imageWalkCap bounds a full-tree listing. 250k entries ≈ tens of MB of JSON;
// beyond that the UI is unusable anyway and the walk is cut with an honest
// "truncated" flag rather than an error.
const imageWalkCap = 250000

// ImagePartition is the JSON shape for the frontend partition picker.
type ImagePartition struct {
	Index      int    `json:"index"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	SizeBytes  int64  `json:"size_bytes"`
	Filesystem string `json:"filesystem"`
	Browsable  bool   `json:"browsable"` // ntfs today; fat/exfat later
}

// ImageContents is a partition's file tree plus truthful flags about it.
type ImageContents struct {
	Entries   []SnapshotEntry `json:"entries"`
	Truncated bool            `json:"truncated"` // hit imageWalkCap
}

// imageTreeCache avoids re-walking the MFT when the user reloads the same
// disk/partition in one session. Keyed on everything that identifies the
// tree; invalidated only by process restart (snapshots are immutable).
var (
	imageTreeMu    sync.Mutex
	imageTreeCache = map[string]*ImageContents{}
)

// withImageVolume opens the reader session, the fixed index for diskArchive,
// parses partitions, and hands the requested NTFS volume to fn. The PBS
// session stays open for the duration of fn (chunks are fetched lazily as fn
// reads). partIndex is 1-based per imagebrowse.Partition.Index; 0 = first
// browsable partition.
func (a *App) withImageVolume(pbsID, backupID, snapshotID, backupType, diskArchive string, partIndex int,
	fn func(vol *imagebrowse.Volume, parts []imagebrowse.Partition, chosen imagebrowse.Partition) error) error {

	cfg, err := a.resolveRestorePBS(pbsID)
	if err != nil {
		return err
	}
	ts, err := time.Parse("2006-01-02T15:04:05Z", snapshotID)
	if err != nil {
		return ibFail(fmt.Errorf("[NB-3410] invalid snapshot ID: %v", err))
	}
	if diskArchive == "" {
		return ibFail(errors.New("[NB-3411] disk archive name required"))
	}
	// Only fixed-index images are valid here; the frontend passes the
	// filename from the snapshot's file list verbatim.
	if !strings.HasSuffix(diskArchive, ".img.fidx") {
		return ibFail(fmt.Errorf("[NB-3412] not a disk image archive: %s", diskArchive))
	}

	client := &pbscommon.PBSClient{
		BaseURL:          cfg.BaseURL,
		CertFingerPrint:  cfg.CertFingerprint,
		AuthID:           cfg.AuthID,
		Secret:           cfg.Secret,
		Datastore:        cfg.Datastore,
		Namespace:        cfg.Namespace,
		Insecure:         cfg.CertFingerprint != "",
		CompressionLevel: pbscommon.CompressionFastest,
		Manifest: pbscommon.BackupManifest{
			BackupID:   backupID,
			BackupTime: ts.Unix(),
		},
	}
	client.Connect(true, normalizeImageBackupType(backupType))
	defer client.Close()

	// The download endpoint serves the index by its stored name (without the
	// .fidx suffix PBS lists it with... it lists the full name; pass as-is).
	ra, size, err := client.NewFIDXReaderAt(diskArchive, 64, func(fetched, total int) {
		if fetched == 1 || fetched%64 == 0 {
			writeBackupLog(fmt.Sprintf("ImageBrowse: fetched %d/%d chunks of %s", fetched, total, diskArchive))
		}
	})
	if err != nil {
		return ibFail(fmt.Errorf("[NB-3413] open disk image %s (type %s): %v", diskArchive, normalizeImageBackupType(backupType), err))
	}

	parts, err := imagebrowse.ListPartitions(ra, size)
	if err != nil {
		return ibFail(fmt.Errorf("[NB-3414] read partition table of %s: %v", diskArchive, err))
	}

	var chosen *imagebrowse.Partition
	for i := range parts {
		if partIndex > 0 && parts[i].Index == partIndex {
			chosen = &parts[i]
			break
		}
		if partIndex == 0 && parts[i].Filesystem == "ntfs" {
			chosen = &parts[i]
			break
		}
	}
	if chosen == nil {
		if partIndex > 0 {
			return ibFail(fmt.Errorf("[NB-3415] partition %d not found", partIndex))
		}
		return ibFail(errors.New("[NB-3416] no browsable (NTFS) partition found on this disk"))
	}
	switch chosen.Filesystem {
	case "ntfs":
		// proceed
	case "bitlocker":
		return ibFail(errors.New("[NB-3417] this partition is BitLocker-encrypted and cannot be browsed; restore the full image instead"))
	default:
		return ibFail(fmt.Errorf("[NB-3418] filesystem %q is not browsable yet (NTFS only); restore the full image instead", chosen.Filesystem))
	}

	vol, err := imagebrowse.OpenNTFS(ra, chosen.StartOffset, chosen.Length)
	if err != nil {
		return ibFail(fmt.Errorf("[NB-3419] open NTFS filesystem: %v", err))
	}
	return fn(vol, parts, *chosen)
}

// ListImagePartitions returns the partitions of one disk image in a volume
// snapshot, for the Browse tab's partition picker.
func (a *App) ListImagePartitions(pbsID, backupID, snapshotID, backupType, diskArchive string) ([]ImagePartition, error) {
	var out []ImagePartition
	cfg, err := a.resolveRestorePBS(pbsID)
	if err != nil {
		return nil, err
	}
	ts, terr := time.Parse("2006-01-02T15:04:05Z", snapshotID)
	if terr != nil {
		return nil, ibFail(fmt.Errorf("[NB-3410] invalid snapshot ID: %v", terr))
	}
	client := &pbscommon.PBSClient{
		BaseURL:          cfg.BaseURL,
		CertFingerPrint:  cfg.CertFingerprint,
		AuthID:           cfg.AuthID,
		Secret:           cfg.Secret,
		Datastore:        cfg.Datastore,
		Namespace:        cfg.Namespace,
		Insecure:         cfg.CertFingerprint != "",
		CompressionLevel: pbscommon.CompressionFastest,
		Manifest:         pbscommon.BackupManifest{BackupID: backupID, BackupTime: ts.Unix()},
	}
	client.Connect(true, normalizeImageBackupType(backupType))
	defer client.Close()

	ra, size, err := client.NewFIDXReaderAt(diskArchive, 16, nil)
	if err != nil {
		return nil, ibFail(fmt.Errorf("[NB-3413] open disk image %s (type %s): %v", diskArchive, normalizeImageBackupType(backupType), err))
	}
	parts, err := imagebrowse.ListPartitions(ra, size)
	if err != nil {
		return nil, ibFail(fmt.Errorf("[NB-3414] read partition table of %s: %v", diskArchive, err))
	}
	for _, p := range parts {
		out = append(out, ImagePartition{
			Index:      p.Index,
			Name:       p.Name,
			Type:       p.Type,
			SizeBytes:  p.Length,
			Filesystem: p.Filesystem,
			Browsable:  p.Filesystem == "ntfs",
		})
	}
	return out, nil
}

// ListImageContents walks the NTFS tree of one partition (0 = first NTFS)
// and returns entries shaped like directory-backup listings, so the existing
// Browse tree renders them unchanged. Returns the SAME []SnapshotEntry type
// as ListSnapshotContents (a known-good Wails binding shape) rather than a
// custom wrapper. Truncation (very large volumes) is exposed separately via
// LastImageListTruncated so the return signature stays identical.
func (a *App) ListImageContents(pbsID, backupID, snapshotID, backupType, diskArchive string, partIndex int, forceRefresh bool) ([]SnapshotEntry, error) {
	key := strings.Join([]string{pbsID, backupID, snapshotID, backupType, diskArchive, fmt.Sprint(partIndex)}, "|")
	imageTreeMu.Lock()
	if !forceRefresh {
		if c, ok := imageTreeCache[key]; ok {
			a.lastImageTruncated = c.Truncated
			imageTreeMu.Unlock()
			return c.Entries, nil
		}
	}
	imageTreeMu.Unlock()

	emit := func(pct float64, msg string) {
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "restore:progress", map[string]interface{}{
				"percent": pct, "message": msg,
			})
		}
	}
	emit(2, "Opening disk image…")

	var result *ImageContents
	err := a.withImageVolume(pbsID, backupID, snapshotID, backupType, diskArchive, partIndex,
		func(vol *imagebrowse.Volume, _ []imagebrowse.Partition, chosen imagebrowse.Partition) error {
			emit(10, fmt.Sprintf("Reading file table of partition %d (%s)…", chosen.Index, chosen.Type))
			entries, werr := vol.Walk("", imageWalkCap, nil)
			truncated := false
			if werr != nil {
				if errors.Is(werr, imagebrowse.ErrTooManyEntries) {
					truncated = true
				} else {
					return werr
				}
			}
			out := make([]SnapshotEntry, 0, len(entries))
			for _, e := range entries {
				out = append(out, SnapshotEntry{
					Path:    e.Path,
					IsDir:   e.IsDir,
					Size:    e.Size,
					ModTime: e.ModTime,
				})
			}
			result = &ImageContents{Entries: out, Truncated: truncated}
			emit(100, fmt.Sprintf("Listed %d entries", len(out)))
			return nil
		})
	if err != nil {
		return nil, err
	}
	imageTreeMu.Lock()
	imageTreeCache[key] = result
	imageTreeMu.Unlock()
	a.lastImageTruncated = result.Truncated
	writeBackupLog(fmt.Sprintf("ImageBrowse: listed %d entries (truncated=%v) from %s part %d",
		len(result.Entries), result.Truncated, diskArchive, partIndex))
	return result.Entries, nil
}

// LastImageListTruncated reports whether the most recent ListImageContents
// hit the entry cap (very large volume). Cheap accessor so ListImageContents
// can keep the exact []SnapshotEntry return shape of ListSnapshotContents.
func (a *App) LastImageListTruncated() bool {
	return a.lastImageTruncated
}

// DownloadImageSelection extracts the selection from a volume backup to
// destPath — single file directly, folders/multi-select as a zip — with the
// same authoritative space enforcement as directory downloads.
func (a *App) DownloadImageSelection(pbsID, backupID, snapshotID, backupType, diskArchive string, partIndex int,
	includePaths []string, destPath string, asZip bool, neededBytes int64) error {

	writeDebugLog(fmt.Sprintf("DownloadImageSelection(pbs=%s backup=%s snap=%s disk=%s part=%d includes=%d dest=%s zip=%v needed=%d)",
		pbsID, backupID, snapshotID, diskArchive, partIndex, len(includePaths), destPath, asZip, neededBytes))

	if destPath == "" {
		return ibFail(errors.New(errDestPathRequired))
	}
	if len(includePaths) == 0 {
		return ibFail(errors.New("nothing selected to download"))
	}
	if !asZip && len(includePaths) != 1 {
		return ibFail(errors.New("single-file download requires exactly one selected file"))
	}
	needed := uint64(0)
	if neededBytes > 0 {
		needed = uint64(neededBytes)
	}

	// ---- space enforcement (authoritative, mirrors download.go) -----------
	tmpParent := os.TempDir()
	if sc, err := evaluateSpace(tmpParent, needed); err == nil && !sc.Fits {
		return ibFail(fmt.Errorf("[NB-3401] not enough space on the temporary drive (%s): need %s, only %s free",
			tmpParent, formatBytesGo(needed), formatBytesGo(sc.FreeBytes)))
	}
	sc, err := evaluateSpace(destPath, needed)
	if err != nil {
		return ibFail(fmt.Errorf("[NB-3402] cannot check free space for %s: %v", destPath, err))
	}
	if !sc.Fits {
		return ibFail(fmt.Errorf("[NB-3403] not enough space on the destination drive: need %s, only %s free — download blocked",
			formatBytesGo(needed), formatBytesGo(sc.FreeBytes)))
	}

	staging, err := os.MkdirTemp("", "nimbus-imgdl-*")
	if err != nil {
		return ibFail(fmt.Errorf("temp dir: %v", err))
	}
	defer func() { _ = os.RemoveAll(staging) }()

	emit := func(percent float64, message string) {
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "restore:progress", map[string]interface{}{
				"percent": percent, "message": message,
			})
		}
	}

	// ---- stage: extract the selection out of the NTFS volume ---------------
	err = a.withImageVolume(pbsID, backupID, snapshotID, backupType, diskArchive, partIndex,
		func(vol *imagebrowse.Volume, _ []imagebrowse.Partition, _ imagebrowse.Partition) error {
			// Expand directory selections into their files using the cached
			// tree when available (cheap), else stat/walk on demand.
			files, xerr := expandImageSelection(vol, includePaths)
			if xerr != nil {
				return xerr
			}
			if len(files) == 0 {
				return ibFail(errors.New("[NB-3420] selection contains no files"))
			}
			for i, f := range files {
				emit(5+float64(i)/float64(len(files))*80,
					fmt.Sprintf("Extracting %d/%d: %s", i+1, len(files), filepath.Base(f)))
				rel := strings.TrimPrefix(f, "/")
				outPath := filepath.Join(staging, filepath.FromSlash(rel))
				if mkerr := os.MkdirAll(filepath.Dir(outPath), 0o755); mkerr != nil {
					return mkerr
				}
				w, cerr := os.Create(outPath)
				if cerr != nil {
					return cerr
				}
				if _, eerr := vol.ExtractFile(f, w); eerr != nil {
					_ = w.Close()
					return ibFail(fmt.Errorf("[NB-3421] extract %s: %v", f, eerr))
				}
				if cerr := w.Close(); cerr != nil {
					return cerr
				}
			}
			return nil
		})
	if err != nil {
		emit(100, "Download failed")
		return err
	}

	// ---- package: identical to directory downloads -------------------------
	emit(88, "Packaging…")
	if asZip {
		if err := zipDirectory(staging, destPath); err != nil {
			emit(100, "Download failed")
			return ibFail(fmt.Errorf("[NB-3422] create zip: %v", err))
		}
	} else {
		src, ferr := findSingleFile(staging)
		if ferr != nil {
			emit(100, "Download failed")
			return ferr
		}
		if err := copyFileTo(src, destPath); err != nil {
			emit(100, "Download failed")
			return ibFail(fmt.Errorf("[NB-3423] write file: %v", err))
		}
	}
	emit(100, "Download complete")
	writeBackupLog(fmt.Sprintf("ImageBrowse: downloaded %d selection(s) from %s to %s", len(includePaths), diskArchive, destPath))
	return nil
}

// expandImageSelection resolves selected paths to concrete files: files pass
// through; directories expand to every file beneath them (bounded by the
// walk cap — a truncated expansion errors rather than silently downloading
// a partial folder).
func expandImageSelection(vol *imagebrowse.Volume, selections []string) ([]string, error) {
	var files []string
	for _, sel := range selections {
		size, isDir, err := vol.StatSize(sel)
		_ = size
		if err != nil {
			return nil, ibFail(fmt.Errorf("[NB-3424] cannot stat %s: %v", sel, err))
		}
		if !isDir {
			files = append(files, sel)
			continue
		}
		entries, werr := vol.Walk(sel, imageWalkCap, nil)
		if werr != nil && !errors.Is(werr, imagebrowse.ErrTooManyEntries) {
			return nil, ibFail(fmt.Errorf("[NB-3425] cannot walk %s: %v", sel, werr))
		}
		if errors.Is(werr, imagebrowse.ErrTooManyEntries) {
			return nil, ibFail(fmt.Errorf("[NB-3426] folder %s has too many files for a single download; select subfolders instead", sel))
		}
		for _, e := range entries {
			if !e.IsDir {
				files = append(files, e.Path)
			}
		}
	}
	return files, nil
}
