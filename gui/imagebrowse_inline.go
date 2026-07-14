//go:build !service
// +build !service

package main

// imagebrowse_inline.go — Browse-tab support for VOLUME (machine/image)
// backups: enumerate partitions, walk a partition's file tree, and download or
// restore a selection out of a raw disk image without restoring the image.
//
// Data path: PBS reader session -> pbscommon.FIDXReaderAt (lazy, chunk-LRU)
// -> imagebrowse partition parse -> NTFS / FAT / exFAT reader over an
// io.SectionReader. Only the chunks a listing or extraction actually touches
// are downloaded, so browsing a multi-TB image moves megabytes. Pure userspace
// parsing: no mount, no driver, no admin.
//
// Every error path goes through ibFail so failures land in the backup log,
// not only in a GUI alert — remote diagnostics need them.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"imagebrowse"
	"pbscommon"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// imageWalkCap bounds a full-tree listing: memory AND the number of image
// blocks pulled from PBS. Beyond it the tree is truncated with an honest
// banner rather than silently partial.
const imageWalkCap = 250000

// ibFail logs an image-browse failure to the backup log and returns it
// unchanged. An error visible only in a GUI alert is invisible to support.
func ibFail(err error) error {
	if err != nil {
		writeBackupLog("ImageBrowse ERROR: " + err.Error())
	}
	return err
}

// normalizeImageBackupType guards the reader session's backup type. Volume
// backups are uploaded as "vm" (drive-sataN.img); directory backups are
// "host". Getting this wrong makes PBS 400 on the group's owner file.
func normalizeImageBackupType(bt string) string {
	switch bt {
	case "vm", "host", "ct":
		return bt
	default:
		return "vm"
	}
}

// ImagePartition is the JSON shape behind the Browse tab's partition picker.
// Allocated is the partition's size from the partition table; Used comes from
// the filesystem itself ($Bitmap / FAT / exFAT allocation bitmap) and is only
// meaningful when UsedKnown is true — we show "—" rather than guess.
type ImagePartition struct {
	Index          int    `json:"index"`
	Name           string `json:"name"`         // GPT partition name
	Type           string `json:"type"`         // "Windows data", "EFI system", ...
	Filesystem     string `json:"filesystem"`   // ntfs | fat32 | exfat | refs | bitlocker | none
	VolumeLabel    string `json:"volume_label"` // from the filesystem, when it has one
	AllocatedBytes int64  `json:"allocated_bytes"`
	UsedBytes      int64  `json:"used_bytes"`
	UsedKnown      bool   `json:"used_known"`
	Browsable      bool   `json:"browsable"`
	Reason         string `json:"reason"` // why it is not browsable, in plain words
}

// imageTreeCache avoids re-walking a partition the user already opened this
// session. Snapshots are immutable, so the only invalidation is process exit.
var (
	imageTreeMu    sync.Mutex
	imageTreeCache = map[string]imageTree{}
)

type imageTree struct {
	Entries   []SnapshotEntry
	Truncated bool
}

// openImageReader opens a PBS reader session and returns a lazy io.ReaderAt
// over one disk image, plus its size and a closer for the session.
func (a *App) openImageReader(pbsID, backupID, snapshotID, backupType, diskArchive string) (*pbscommon.FIDXReaderAt, int64, func(), error) {
	cfg, err := a.resolveRestorePBS(pbsID)
	if err != nil {
		return nil, 0, nil, err
	}
	ts, err := time.Parse("2006-01-02T15:04:05Z", snapshotID)
	if err != nil {
		return nil, 0, nil, ibFail(fmt.Errorf("[NB-3410] invalid snapshot ID: %v", err))
	}
	if diskArchive == "" || !strings.HasSuffix(diskArchive, ".img.fidx") {
		return nil, 0, nil, ibFail(fmt.Errorf("[NB-3412] not a disk image archive: %q", diskArchive))
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

	ra, size, err := client.NewFIDXReaderAt(diskArchive, 64, nil)
	if err != nil {
		client.Close()
		return nil, 0, nil, ibFail(fmt.Errorf("[NB-3413] open disk image %s (type %s): %v",
			diskArchive, normalizeImageBackupType(backupType), err))
	}
	// A single HTTP stream is latency-bound (~120 Mbps observed on a LAN):
	// each 4 MB chunk waits out a full round trip before the next request
	// starts. Sequential consumers — the $MFT scan, file extraction — telegraph
	// exactly which chunks come next, so read ahead with concurrent requests.
	// 6 workers x 16 chunks ahead ≈ 64 MB in flight window; verified race-free
	// and dedup-exact by the pbscommon prefetch tests.
	ra.SetPrefetch(6, 16)
	return ra, size, func() { client.Close() }, nil
}

// withPartition opens one partition's filesystem and hands it to fn.
// partIndex is the 1-based index from ListImagePartitions — there is NO
// auto-selection: picking a partition for the user landed them inside the
// WinRE recovery volume and looked like a bug, because it was one.
func (a *App) withPartition(pbsID, backupID, snapshotID, backupType, diskArchive string, partIndex int,
	fn func(fs imagebrowse.Filesystem, p imagebrowse.Partition) error) error {

	if partIndex < 1 {
		return ibFail(errors.New("[NB-3415] no partition selected — choose a partition to browse"))
	}
	ra, size, closer, err := a.openImageReader(pbsID, backupID, snapshotID, backupType, diskArchive)
	if err != nil {
		return err
	}
	defer closer()

	parts, err := imagebrowse.ListPartitions(ra, size)
	if err != nil {
		return ibFail(fmt.Errorf("[NB-3414] read partition table of %s: %v", diskArchive, err))
	}
	var chosen *imagebrowse.Partition
	for i := range parts {
		if parts[i].Index == partIndex {
			chosen = &parts[i]
			break
		}
	}
	if chosen == nil {
		return ibFail(fmt.Errorf("[NB-3415] partition %d not found on %s", partIndex, diskArchive))
	}

	fs, err := imagebrowse.OpenFilesystem(ra, *chosen)
	if err != nil {
		// OpenFilesystem's errors are already user-facing ("BitLocker-protected
		// volume — restore the full image instead"), so pass them through.
		return ibFail(fmt.Errorf("[NB-3419] partition %d (%s): %v", partIndex, chosen.Filesystem, err))
	}
	return fn(fs, *chosen)
}

// ListImagePartitions enumerates every partition on a disk image — regardless
// of whether we can browse it — with its filesystem, allocated size, and used
// size. The user chooses; we never choose for them.
func (a *App) ListImagePartitions(pbsID, backupID, snapshotID, backupType, diskArchive string) ([]ImagePartition, error) {
	ra, size, closer, err := a.openImageReader(pbsID, backupID, snapshotID, backupType, diskArchive)
	if err != nil {
		return nil, err
	}
	defer closer()

	parts, err := imagebrowse.ListPartitions(ra, size)
	if err != nil {
		return nil, ibFail(fmt.Errorf("[NB-3414] read partition table of %s: %v", diskArchive, err))
	}

	out := make([]ImagePartition, 0, len(parts))
	for _, p := range parts {
		ip := ImagePartition{
			Index:          p.Index,
			Name:           p.Name,
			Type:           p.Type,
			Filesystem:     p.Filesystem,
			VolumeLabel:    p.VolumeLabel,
			AllocatedBytes: p.Length,
			Browsable:      imagebrowse.Browsable(p.Filesystem),
		}
		if ip.Browsable {
			// Used space needs the filesystem opened. Best-effort: a partition
			// we cannot interrogate still lists, with used shown as unknown.
			if fs, ferr := imagebrowse.OpenFilesystem(ra, p); ferr == nil {
				if used, ok := fs.UsedBytes(); ok {
					ip.UsedBytes, ip.UsedKnown = used, true
				}
				if lbl := fs.Label(); lbl != "" && ip.VolumeLabel == "" {
					ip.VolumeLabel = lbl
				}
			} else {
				ip.Browsable = false
				ip.Reason = ferr.Error()
			}
		} else {
			switch p.Filesystem {
			case imagebrowse.FSBitLocker:
				ip.Reason = "BitLocker-encrypted — restore the full image instead"
			case imagebrowse.FSReFS:
				ip.Reason = "ReFS file browsing is not supported — restore the full image instead"
			case imagebrowse.FSNone:
				ip.Reason = "no filesystem (reserved partition)"
			default:
				ip.Reason = "unrecognized filesystem — restore the full image instead"
			}
		}
		out = append(out, ip)
	}
	writeBackupLog(fmt.Sprintf("ImageBrowse: %s has %d partition(s)", diskArchive, len(out)))
	return out, nil
}

// ListImageContents walks one partition's file tree. Returns the same flat
// []SnapshotEntry shape as ListSnapshotContents so the Browse tree renders
// both backup types with one code path.
func (a *App) ListImageContents(pbsID, backupID, snapshotID, backupType, diskArchive string,
	partIndex int, forceRefresh bool) ([]SnapshotEntry, error) {

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

	var result imageTree
	err := a.withPartition(pbsID, backupID, snapshotID, backupType, diskArchive, partIndex,
		func(fs imagebrowse.Filesystem, p imagebrowse.Partition) error {
			emit(10, fmt.Sprintf("Reading the file table of partition %d (%s)…", p.Index, fs.Kind()))
			// FullTree takes the fast path when the filesystem has one: on NTFS
			// that is ONE sequential read of the $MFT (the WizTree technique)
			// instead of random directory reads scattered across the volume —
			// over the PBS chunk reader, ~the MFT's size downloaded instead of
			// a large fraction of the partition.
			entries, werr := imagebrowse.FullTree(fs, imageWalkCap, nil, func(done, total int64) {
				if total > 0 {
					pct := 10 + 85*float64(done)/float64(total)
					emit(pct, fmt.Sprintf("Scanning file table: %d / %d records", done, total))
				}
			})
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
			result = imageTree{Entries: out, Truncated: truncated}
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
	writeBackupLog(fmt.Sprintf("ImageBrowse: listed %d entries (truncated=%v) from %s partition %d",
		len(result.Entries), result.Truncated, diskArchive, partIndex))
	return result.Entries, nil
}

// LastImageListTruncated reports whether the last ListImageContents hit the
// entry cap. Separate accessor so ListImageContents keeps the exact return
// shape of the (known-good) directory lister.
func (a *App) LastImageListTruncated() bool { return a.lastImageTruncated }

// extractSelection stages the chosen paths from an image partition into
// stageDir, preserving their relative structure. Shared by download and
// restore so the two can never drift.
func (a *App) extractSelection(pbsID, backupID, snapshotID, backupType, diskArchive string, partIndex int,
	includePaths []string, stageDir string, emit func(float64, string)) error {

	return a.withPartition(pbsID, backupID, snapshotID, backupType, diskArchive, partIndex,
		func(fs imagebrowse.Filesystem, _ imagebrowse.Partition) error {
			files, err := expandImageSelection(fs, includePaths)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				return ibFail(errors.New("[NB-3420] the selection contains no files"))
			}
			for i, f := range files {
				emit(5+float64(i)/float64(len(files))*80,
					fmt.Sprintf("Extracting %d/%d: %s", i+1, len(files), filepath.Base(f)))
				out := filepath.Join(stageDir, filepath.FromSlash(strings.TrimPrefix(f, "/")))
				if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
					return ibFail(err)
				}
				w, cerr := os.Create(out)
				if cerr != nil {
					return ibFail(cerr)
				}
				if _, eerr := fs.ExtractFile(f, w); eerr != nil {
					_ = w.Close()
					return ibFail(fmt.Errorf("[NB-3421] extract %s: %v", f, eerr))
				}
				if cerr := w.Close(); cerr != nil {
					return ibFail(cerr)
				}
			}
			return nil
		})
}

// DownloadImageSelection extracts a selection from a volume backup to
// destPath — a single file written directly, or a zip for folders and
// multi-selections — with authoritative free-space enforcement.
func (a *App) DownloadImageSelection(pbsID, backupID, snapshotID, backupType, diskArchive string, partIndex int,
	includePaths []string, destPath string, asZip bool, neededBytes int64) error {

	writeDebugLog(fmt.Sprintf("DownloadImageSelection(disk=%s part=%d includes=%d dest=%s zip=%v needed=%d)",
		diskArchive, partIndex, len(includePaths), destPath, asZip, neededBytes))

	if destPath == "" {
		return ibFail(errors.New(errDestPathRequired))
	}
	if len(includePaths) == 0 {
		return ibFail(errors.New("[NB-3420] nothing selected to download"))
	}
	if !asZip && len(includePaths) != 1 {
		return ibFail(errors.New("[NB-3420] a single-file download needs exactly one selected file"))
	}
	needed := uint64(0)
	if neededBytes > 0 {
		needed = uint64(neededBytes)
	}

	// Space enforcement — authoritative here, regardless of the frontend
	// pre-flight. Both the staging drive and the destination drive must fit it.
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

	emit := func(pct float64, msg string) {
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "restore:progress", map[string]interface{}{
				"percent": pct, "message": msg,
			})
		}
	}

	if err := a.extractSelection(pbsID, backupID, snapshotID, backupType, diskArchive, partIndex,
		includePaths, staging, emit); err != nil {
		emit(100, "Download failed")
		return err
	}

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
			return ibFail(ferr)
		}
		if err := copyFileTo(src, destPath); err != nil {
			emit(100, "Download failed")
			return ibFail(fmt.Errorf("[NB-3423] write file: %v", err))
		}
	}
	emit(100, "Download complete")
	writeBackupLog(fmt.Sprintf("ImageBrowse: downloaded %d selection(s) from %s to %s",
		len(includePaths), diskArchive, destPath))
	return nil
}

// RestoreImageSelection restores selected files from a volume backup INTO a
// destination folder (not a zip). This is what the Restore button does for an
// image backup: the old path tried to open backup.pxar.didx as a "host" backup
// and PBS 400'd, because a volume snapshot has neither.
func (a *App) RestoreImageSelection(pbsID, backupID, snapshotID, backupType, diskArchive string, partIndex int,
	includePaths []string, destDir string, keepStructure, overwrite bool, neededBytes int64) error {

	writeDebugLog(fmt.Sprintf("RestoreImageSelection(disk=%s part=%d includes=%d dest=%s keep=%v overwrite=%v)",
		diskArchive, partIndex, len(includePaths), destDir, keepStructure, overwrite))

	if destDir == "" {
		return ibFail(errors.New(errDestPathRequired))
	}
	if len(includePaths) == 0 {
		return ibFail(errors.New("[NB-3420] select the files or folders to restore"))
	}
	needed := uint64(0)
	if neededBytes > 0 {
		needed = uint64(neededBytes)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return ibFail(fmt.Errorf("[NB-3402] cannot create %s: %v", destDir, err))
	}
	sc, err := evaluateSpace(destDir, needed)
	if err != nil {
		return ibFail(fmt.Errorf("[NB-3402] cannot check free space for %s: %v", destDir, err))
	}
	if !sc.Fits {
		return ibFail(fmt.Errorf("[NB-3403] not enough space to restore: need %s, only %s free",
			formatBytesGo(needed), formatBytesGo(sc.FreeBytes)))
	}

	emit := func(pct float64, msg string) {
		if a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "restore:progress", map[string]interface{}{
				"percent": pct, "message": msg,
			})
		}
	}

	staging, err := os.MkdirTemp("", "nimbus-imgrs-*")
	if err != nil {
		return ibFail(fmt.Errorf("temp dir: %v", err))
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if err := a.extractSelection(pbsID, backupID, snapshotID, backupType, diskArchive, partIndex,
		includePaths, staging, emit); err != nil {
		emit(100, "Restore failed")
		return err
	}

	// Move the staged tree into place. keepStructure=false flattens to base
	// names (matching the directory-restore option of the same name).
	emit(90, "Writing files…")
	count := 0
	err = filepath.Walk(staging, func(p string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() {
			return werr
		}
		rel, rerr := filepath.Rel(staging, p)
		if rerr != nil {
			return rerr
		}
		target := filepath.Join(destDir, rel)
		if !keepStructure {
			target = filepath.Join(destDir, filepath.Base(rel))
		}
		if _, serr := os.Stat(target); serr == nil && !overwrite {
			return fmt.Errorf("[NB-3427] %s already exists (tick Overwrite to replace it)", target)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := copyFileTo(p, target); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		emit(100, "Restore failed")
		return ibFail(err)
	}

	emit(100, fmt.Sprintf("Restored %d file(s)", count))
	writeBackupLog(fmt.Sprintf("ImageBrowse: restored %d file(s) from %s partition %d to %s",
		count, diskArchive, partIndex, destDir))
	return nil
}

// expandImageSelection resolves a selection to concrete files: files pass
// through, directories expand to every file beneath them. An over-cap folder
// errors rather than silently downloading a partial tree.
func expandImageSelection(fs imagebrowse.Filesystem, selections []string) ([]string, error) {
	var files []string
	for _, sel := range selections {
		st, err := fs.Stat(sel)
		if err != nil {
			return nil, ibFail(fmt.Errorf("[NB-3424] cannot read %s: %v", sel, err))
		}
		if !st.IsDir {
			files = append(files, st.Path)
			continue
		}
		entries, werr := imagebrowse.Walk(fs, sel, imageWalkCap, nil)
		if errors.Is(werr, imagebrowse.ErrTooManyEntries) {
			return nil, ibFail(fmt.Errorf("[NB-3426] the folder %s holds too many files for one operation — select subfolders instead", sel))
		}
		if werr != nil {
			return nil, ibFail(fmt.Errorf("[NB-3425] cannot read the folder %s: %v", sel, werr))
		}
		for _, e := range entries {
			if !e.IsDir {
				files = append(files, e.Path)
			}
		}
	}
	return files, nil
}

// interface assertion: keeps the io import honest if extraction is refactored.
var _ io.Writer = (*os.File)(nil)
