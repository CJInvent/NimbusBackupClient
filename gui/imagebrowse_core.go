package main

// imagebrowse_core.go — VOLUME (machine/image) backup browsing, shared by
// BOTH processes: the GUI's Browse tab AND the service's control-plane
// delegation (the NimbusControl portal browses image backups by sending
// commands that land here — see controlplane_glue.go). Progress reporting is
// abstracted behind a.ibEmit so this file carries no Wails dependency: enumerate partitions, walk a partition's file tree, and download or
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
	"context"
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
	"security"
)

// imageWalkCap bounds a full-tree listing: memory AND the number of image
// blocks pulled from PBS. Beyond it the tree is truncated with an honest
// banner rather than silently partial.
const imageWalkCap = 250000

// imageScanWorkers is how many chunk requests the $MFT plan keeps in flight.
// They multiplex over one HTTP/2 connection to PBS, so this can be well above
// the old value of 6 without opening connections; it is bounded inside
// PlanPrefetch to a safe ceiling.
const imageScanWorkers = 32

// resolveRestorePBS picks the PBS server to restore from. When pbsID is empty
// the default PBS server is used. Falls back to legacy single-server fields
// when no multi-PBS entry is configured.
func (a *App) resolveRestorePBS(pbsID string) (*Config, error) {
	if pbsID != "" {
		pbs, err := a.config.GetPBSServer(pbsID)
		if err != nil {
			return nil, err
		}
		return pbs.ToConfig(), nil
	}
	cfg := a.config.EffectivePBS()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

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
	FileTableBytes int64  `json:"file_table_bytes"` // $MFT size on NTFS — the download cost of Browse
	FileTableFrags int    `json:"file_table_frags"` // how many on-disk fragments it is in
	Browsable      bool   `json:"browsable"`
	Reason         string `json:"reason"` // why it is not browsable, in plain words
}

// imageTreeCache avoids re-walking a partition the user already opened this
// session. Snapshots are immutable, so the only invalidation is process exit.
var (
	imageTreeMu    sync.Mutex
	imageTreeCache = map[string]*imageTree{}
)

// imageTree is the Go-side cache of one partition scan: every entry, plus a
// per-directory child index and rolled-up directory sizes, built once.
type imageTree struct {
	entries  []SnapshotEntry
	byDir    map[string][]int  // parent dir -> indices of children
	dirSize  map[string]uint64 // dir path -> sum of all file bytes beneath
	dirCount map[string]int    // dir path -> number of files beneath
}

func imgParentDir(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

func newImageTree(entries []SnapshotEntry) *imageTree {
	t := &imageTree{
		entries:  entries,
		byDir:    make(map[string][]int, len(entries)/8+1),
		dirSize:  make(map[string]uint64),
		dirCount: make(map[string]int),
	}
	for i, e := range entries {
		d := imgParentDir(e.Path)
		t.byDir[d] = append(t.byDir[d], i)
		if !e.IsDir {
			// Roll the file's size up every ancestor.
			for anc := d; ; anc = imgParentDir(anc) {
				t.dirSize[anc] += e.Size
				t.dirCount[anc]++
				if anc == "/" {
					break
				}
			}
		}
	}
	return t
}

// children returns dir's immediate children; directories carry their
// rolled-up size so the size column is meaningful at every level.
func (t *imageTree) children(dir string) []SnapshotEntry {
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "/"
	}
	idx := t.byDir[dir]
	out := make([]SnapshotEntry, 0, len(idx))
	for _, i := range idx {
		e := t.entries[i]
		if e.IsDir {
			e.Size = t.dirSize[e.Path]
		}
		out = append(out, e)
	}
	return out
}

// openImageReader opens a PBS reader session and returns a lazy io.ReaderAt
// over one disk image, plus its size and a closer for the session.
// lookahead=true enables image-linear read-ahead (right for extraction of
// mostly-contiguous files); the $MFT scan passes false and uses an exact
// PlanPrefetch from the run list instead — on a fragmented volume, linear
// read-ahead between fragments drags in gigabytes of unrelated disk.
func (a *App) openImageReader(pbsID, backupID, snapshotID, backupType, diskArchive string, lookahead bool) (*pbscommon.FIDXReaderAt, int64, func(), error) {
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
	if lookahead {
		// Image-linear read-ahead: 6 workers, 16 chunks (~64 MB) ahead.
		ra.SetPrefetch(6, 16)
	}
	// Browsing and file downloads honour the same configurable network limit
	// as the rest of the client (one shared bucket across all fetch workers).
	if a.config != nil && a.config.DownloadLimitMbps > 0 {
		ra.SetRateLimitMbps(a.config.DownloadLimitMbps)
	}
	return ra, size, func() { client.Close() }, nil
}

// withPartition opens one partition's filesystem and hands it to fn.
// partIndex is the 1-based index from ListImagePartitions — there is NO
// auto-selection: picking a partition for the user landed them inside the
// WinRE recovery volume and looked like a bug, because it was one.
func (a *App) withPartition(pbsID, backupID, snapshotID, backupType, diskArchive string, partIndex int, lookahead bool,
	fn func(fs imagebrowse.Filesystem, p imagebrowse.Partition, ra *pbscommon.FIDXReaderAt) error) error {

	if partIndex < 1 {
		return ibFail(errors.New("[NB-3415] no partition selected — choose a partition to browse"))
	}
	ra, size, closer, err := a.openImageReader(pbsID, backupID, snapshotID, backupType, diskArchive, lookahead)
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
	return fn(fs, *chosen, ra)
}

// ListImagePartitions enumerates every partition on a disk image — regardless
// of whether we can browse it — with its filesystem, allocated size, and used
// size. The user chooses; we never choose for them.
func (a *App) ListImagePartitions(pbsID, backupID, snapshotID, backupType, diskArchive string) ([]ImagePartition, error) {
	if !ControlPolicy().FileRestore {
		return nil, ErrRestoreDisabled
	}
	ra, size, closer, err := a.openImageReader(pbsID, backupID, snapshotID, backupType, diskArchive, false)
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
				// File-table size (the $MFT on NTFS): what a Browse will
				// actually download, shown next to the button so the user
				// knows the cost before clicking.
				if pl, ok := fs.(imagebrowse.Planner); ok {
					if mftSize, extents, perr := pl.StoragePlan(); perr == nil {
						ip.FileTableBytes = mftSize
						ip.FileTableFrags = len(extents)
					}
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

// ListImageContents scans one partition's file table and returns the ROOT
// directory listing. The full tree stays in the Go-side session cache —
// shipping 1.2M entries of JSON into the webview is what forced the old
// 250k-entry truncation; per-directory listing (ListImageDirectory) has no
// such limit. Directory entries carry rolled-up sizes.
func (a *App) ListImageContents(pbsID, backupID, snapshotID, backupType, diskArchive string,
	partIndex int, forceRefresh bool) ([]SnapshotEntry, error) {
	if !ControlPolicy().FileRestore {
		return nil, ErrRestoreDisabled
	}

	key := strings.Join([]string{pbsID, backupID, snapshotID, backupType, diskArchive, fmt.Sprint(partIndex)}, "|")
	imageTreeMu.Lock()
	if !forceRefresh {
		if c, ok := imageTreeCache[key]; ok {
			imageTreeMu.Unlock()
			a.lastImageKey = key
			a.lastImageTruncated = false
			return c.children("/"), nil
		}
	}
	imageTreeMu.Unlock()

	emit := a.ibEmit
	emit(2, "Opening disk image…")

	var result *imageTree
	err := a.withPartition(pbsID, backupID, snapshotID, backupType, diskArchive, partIndex, false,
		func(fs imagebrowse.Filesystem, p imagebrowse.Partition, ra *pbscommon.FIDXReaderAt) error {
			// Fetch EXACTLY the file table's on-disk extents, concurrently —
			// never linear read-ahead, which on a fragmented volume drags in
			// unrelated disk between fragments.
			if pl, ok := fs.(imagebrowse.Planner); ok {
				if mftSize, extents, perr := pl.StoragePlan(); perr == nil {
					abs := make([][2]int64, 0, len(extents))
					for _, e := range extents {
						abs = append(abs, [2]int64{p.StartOffset + e.Offset, e.Length})
					}
					// HTTP/2 multiplexes all of these over the single PBS
					// connection, so deep concurrency costs almost nothing and
					// is what actually saturates the link — 6 workers left the
					// pipe mostly idle between round trips. 32 keeps enough
					// streams in flight to hide latency without tripping the
					// server's default MaxConcurrentStreams (~100).
					stopPlan := ra.PlanPrefetch(abs, imageScanWorkers)
					defer stopPlan()
					emit(6, fmt.Sprintf("Downloading file table: %s in %d fragment(s)…",
						formatBytesGo(uint64(mftSize)), len(extents)))
					writeBackupLog(fmt.Sprintf("ImageBrowse: $MFT plan %s across %d fragment(s) on %s partition %d",
						formatBytesGo(uint64(mftSize)), len(extents), diskArchive, p.Index))
				}
			}
			entries, werr := imagebrowse.FullTree(fs, 0, nil, func(done, total int64) {
				if total > 0 {
					pct := 8 + 87*float64(done)/float64(total)
					emit(pct, fmt.Sprintf("Scanning file table: %d / %d records", done, total))
				}
			})
			if werr != nil && !errors.Is(werr, imagebrowse.ErrTooManyEntries) {
				return werr
			}
			converted := make([]SnapshotEntry, 0, len(entries))
			for _, e := range entries {
				converted = append(converted, SnapshotEntry{
					Path: e.Path, IsDir: e.IsDir, Size: e.Size, ModTime: e.ModTime,
				})
			}
			result = newImageTree(converted)
			emit(100, fmt.Sprintf("Listed %d entries", len(converted)))
			return nil
		})
	if err != nil {
		return nil, err
	}

	imageTreeMu.Lock()
	imageTreeCache[key] = result
	imageTreeMu.Unlock()
	a.lastImageKey = key
	a.lastImageTruncated = false
	writeBackupLog(fmt.Sprintf("ImageBrowse: cached %d entries from %s partition %d",
		len(result.entries), diskArchive, partIndex))
	return result.children("/"), nil
}

// ListImageDirectory returns the immediate children of dir from the cached
// scan — the whole point of keeping the tree in Go: the webview only ever
// holds one directory's worth of rows, so nothing needs truncating.
func (a *App) ListImageDirectory(pbsID, backupID, snapshotID, backupType, diskArchive string,
	partIndex int, dir string) ([]SnapshotEntry, error) {
	if !ControlPolicy().FileRestore {
		return nil, ErrRestoreDisabled
	}
	key := strings.Join([]string{pbsID, backupID, snapshotID, backupType, diskArchive, fmt.Sprint(partIndex)}, "|")
	imageTreeMu.Lock()
	c, ok := imageTreeCache[key]
	imageTreeMu.Unlock()
	if !ok {
		return nil, ibFail(errors.New("[NB-3428] no scan cached for this partition — open it with Browse files first"))
	}
	return c.children(dir), nil
}

// LastImageListTruncated reports whether the last ListImageContents hit the
// entry cap. Separate accessor so ListImageContents keeps the exact return
// shape of the (known-good) directory lister.
func (a *App) LastImageListTruncated() bool { return a.lastImageTruncated }

// DownloadImageSelection packages the selection as a ZIP, STREAMED in one
// pass to destPath: PBS chunks -> NTFS parser -> zip entry -> disk, nothing
// staged anywhere. The zip is packaging, not compression (Store method), so
// throughput is I/O-bound and the progress bar's byte math is exact. The
// old single-file "direct" mode is gone: the caller only invokes this when
// the user explicitly ticked Package as ZIP, and one file in a zip is what
// they asked for.
func (a *App) DownloadImageSelection(pbsID, backupID, snapshotID, backupType, diskArchive string, partIndex int,
	includePaths []string, destPath string, asZip bool, neededBytes int64) error {
	if !ControlPolicy().FileRestore {
		return ErrRestoreDisabled
	}

	writeDebugLog(fmt.Sprintf("DownloadImageSelection(disk=%s part=%d includes=%d dest=%s needed=%d)",
		diskArchive, partIndex, len(includePaths), destPath, neededBytes))
	_ = asZip // retained in the signature for frontend compatibility; always zip now

	if destPath == "" {
		return ibFail(errors.New(errDestPathRequired))
	}
	if len(includePaths) == 0 {
		return ibFail(errors.New("[NB-3420] nothing selected to download"))
	}
	if !strings.HasSuffix(strings.ToLower(destPath), ".zip") {
		destPath += ".zip"
	}
	needed := uint64(0)
	if neededBytes > 0 {
		needed = uint64(neededBytes)
	}

	// Space enforcement — authoritative here, regardless of the frontend
	// preflight. Store-method zip output ≈ input bytes (+ tiny headers).
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return ibFail(fmt.Errorf("[NB-3402] cannot create %s: %v", filepath.Dir(destPath), err))
	}
	if needed > 0 {
		sc, err := evaluateSpace(filepath.Dir(destPath), needed)
		if err == nil && !sc.Fits {
			return ibFail(fmt.Errorf("[NB-3403] not enough space: need %s, only %s free",
				formatBytesGo(needed), formatBytesGo(sc.FreeBytes)))
		}
	}

	// Cancellable, same registration the restore path uses — the one Cancel
	// button covers both.
	ctx, cancel := context.WithCancel(context.Background())
	a.ibRestoreMu.Lock()
	a.ibRestoreCancel = cancel
	a.ibRestoreMu.Unlock()
	defer func() {
		a.ibRestoreMu.Lock()
		a.ibRestoreCancel = nil
		a.ibRestoreMu.Unlock()
		cancel()
	}()
	cancelled := func() bool {
		select {
		case <-ctx.Done():
			return true
		default:
			return false
		}
	}

	a.ibEmit(0, "Opening disk image…")
	var nFiles int
	var nBytes int64
	err := a.withPartition(pbsID, backupID, snapshotID, backupType, diskArchive, partIndex, true,
		func(fs imagebrowse.Filesystem, _ imagebrowse.Partition, _ *pbscommon.FIDXReaderAt) error {
			files, total, perr := planSelection(fs, includePaths)
			if perr != nil {
				return perr
			}
			out, cerr := os.Create(destPath)
			if cerr != nil {
				return ibFail(fmt.Errorf("[NB-3421] create %s: %v", destPath, cerr))
			}
			nFiles, nBytes, perr = streamImageZip(fs, files, total, out, a.ibEmitTask, cancelled)
			if perr != nil {
				_ = out.Close()
				_ = os.Remove(destPath) // never leave a corrupt half-zip
				return perr
			}
			return out.Close()
		})
	if errors.Is(err, errImageRestoreCancelled) {
		a.ibEmit(100, "Download cancelled")
		return err
	}
	if err != nil {
		a.ibEmit(100, "Download failed")
		return ibFail(err)
	}
	a.ibEmit(100, fmt.Sprintf("Packaged %d file(s), %s", nFiles, formatBytesGo(uint64(nBytes))))
	writeBackupLog(fmt.Sprintf("ImageBrowse: streamed %d file(s) / %s into %s",
		nFiles, formatBytesGo(uint64(nBytes)), destPath))
	return nil
}

// RestoreImageSelection restores selected files from a volume backup INTO a
// destination folder (not a zip). This is what the Restore button does for an
// image backup: the old path tried to open backup.pxar.didx as a "host" backup
// and PBS 400'd, because a volume snapshot has neither.
// The restoreMtimes / restoreACLs / restoreADS options only have meaning when
// the SOURCE stores them (NTFS) — the frontend greys them out otherwise, and
// the backend treats them as best-effort per file: metadata failures warn in
// the backup log, the file's data is already safely in place.
func (a *App) RestoreImageSelection(pbsID, backupID, snapshotID, backupType, diskArchive string, partIndex int,
	includePaths []string, destDir string, keepStructure, overwrite bool,
	restoreMtimes, restoreACLs, restoreADS bool, neededBytes int64) error {
	if !ControlPolicy().FileRestore {
		return ErrRestoreDisabled
	}

	writeDebugLog(fmt.Sprintf("RestoreImageSelection(disk=%s part=%d includes=%d dest=%s keep=%v overwrite=%v mtime=%v acl=%v ads=%v)",
		diskArchive, partIndex, len(includePaths), destDir, keepStructure, overwrite, restoreMtimes, restoreACLs, restoreADS))

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

	emit := a.ibEmit

	// Register this restore as cancellable. The frontend Cancel button calls
	// CancelImageRestore, which fires this context.
	ctx, cancel := context.WithCancel(context.Background())
	a.ibRestoreMu.Lock()
	a.ibRestoreCancel = cancel
	a.ibRestoreMu.Unlock()
	defer func() {
		a.ibRestoreMu.Lock()
		a.ibRestoreCancel = nil
		a.ibRestoreMu.Unlock()
		cancel()
	}()
	cancelled := func() bool {
		select {
		case <-ctx.Done():
			return true
		default:
			return false
		}
	}

	// Extract DIRECTLY to the destination — no temp staging. Staging doubled
	// the space requirement (temp copy + final copy) and, on a nearly-full
	// system drive holding %TEMP%, could fail the staging write for a large
	// file even when the DESTINATION drive had ample room. Direct placement
	// needs space only where the files actually land, which is what we checked.
	count, warned, refused := 0, 0, 0
	err = a.withPartition(pbsID, backupID, snapshotID, backupType, diskArchive, partIndex, true,
		func(fs imagebrowse.Filesystem, _ imagebrowse.Partition, _ *pbscommon.FIDXReaderAt) error {
			files, totalBytes, ferr := planSelection(fs, includePaths)
			if ferr != nil {
				return ferr
			}
			streamer, canStream := fs.(imagebrowse.StreamLister)
			secReader, canSD := fs.(imagebrowse.SecurityReader)

			// Byte-accurate progress: the bar tracks bytes landed, not files
			// finished — a single 5.7 GB file moves the bar continuously
			// instead of parking at its first emit.
			prog := newTaskProgress(totalBytes, "", a.ibEmitTask)

			for i, f := range files {
				if cancelled() {
					return errImageRestoreCancelled
				}
				prog.label = fmt.Sprintf("Restoring %d/%d: %s", i+1, len(files), filepath.Base(f))

				// Where this file lands. keepStructure=false flattens to the base
				// name (matches the directory-restore option of the same name).
				//
				// These names come from the IMAGE's own directory entries, not
				// from the user. Nothing in NTFS/FAT on disk prevents a name
				// from holding a separator or a ".." component — that is a
				// Windows API restriction, not a format one — and
				// filepath.Join RESOLVES ".." instead of rejecting it. Joining
				// them unchecked let a crafted or corrupted image write
				// anywhere the process could reach, and the service restores as
				// LocalSystem. Refuse and keep going, as the PXAR path does: one
				// hostile entry must not abort a 10,000-file restore, but it
				// must never be silent either.
				var target string
				if keepStructure {
					t, perr := security.SafeJoin(destDir, strings.TrimPrefix(f, "/"))
					if perr != nil {
						writeBackupLog(fmt.Sprintf("ImageBrowse: REFUSED unsafe path from image: %v", perr))
						refused++
						continue
					}
					target = t
				} else {
					base, berr := security.SafeBaseName(f)
					if berr != nil {
						writeBackupLog(fmt.Sprintf("ImageBrowse: REFUSED unsafe file name from image: %v", berr))
						refused++
						continue
					}
					t, perr := security.SafeJoin(destDir, base)
					if perr != nil {
						writeBackupLog(fmt.Sprintf("ImageBrowse: REFUSED unsafe path from image: %v", perr))
						refused++
						continue
					}
					target = t
				}
				if _, serr := os.Stat(target); serr == nil && !overwrite {
					return fmt.Errorf("[NB-3427] %s already exists (tick Overwrite to replace it)", target)
				}
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return ibFail(err)
				}

				// Stream the file's bytes straight to the destination.
				w, cerr := os.Create(target)
				if cerr != nil {
					return ibFail(fmt.Errorf("[NB-3421] create %s: %v", target, cerr))
				}
				if _, eerr := fs.ExtractFile(f, &countingWriter{w: w, t: prog}); eerr != nil {
					_ = w.Close()
					_ = os.Remove(target) // don't leave a half-written file
					return ibFail(fmt.Errorf("[NB-3421] extract %s: %v", f, eerr))
				}
				if cerr := w.Close(); cerr != nil {
					return ibFail(fmt.Errorf("[NB-3421] finalize %s: %v", target, cerr))
				}
				count++

				// Metadata, best-effort, applied AFTER the bytes are in place:
				// ADS, then mtime, then the security descriptor LAST (a
				// restrictive DACL could lock us out of a file we still need to
				// finish writing streams onto).
				if restoreADS && canStream {
					if streams, lerr := streamer.ListStreams(f); lerr == nil {
						for _, si := range streams {
							var buf strings.Builder
							if _, xerr := streamer.ExtractStream(f, si.Name, &buf); xerr == nil {
								if aerr := writeADS(target, si.Name, []byte(buf.String())); aerr != nil {
									warned++
									writeBackupLog(fmt.Sprintf("ImageBrowse WARN: ADS %s:%s not restored: %v", target, si.Name, aerr))
								}
							}
						}
					}
				}
				if restoreMtimes {
					if st, serr := fs.Stat(f); serr == nil && st.ModTime > 0 {
						ts := time.Unix(st.ModTime, 0)
						if terr := os.Chtimes(target, ts, ts); terr != nil {
							warned++
							writeBackupLog(fmt.Sprintf("ImageBrowse WARN: mtime not restored on %s: %v", target, terr))
						}
					}
				}
				if restoreACLs && canSD {
					if sd, sderr := secReader.SecurityDescriptor(f); sderr == nil && len(sd) > 0 {
						if warning, aerr := applyNTFSSecurity(target, sd); aerr != nil {
							warned++
							writeBackupLog(fmt.Sprintf("ImageBrowse WARN: permissions not restored on %s: %v", target, aerr))
						} else if warning != "" {
							warned++
							writeBackupLog(fmt.Sprintf("ImageBrowse WARN: %s: %s", target, warning))
						}
					}
				}
			}
			return nil
		})

	if errors.Is(err, errImageRestoreCancelled) {
		emit(100, "Restore cancelled")
		writeBackupLog(fmt.Sprintf("ImageBrowse: restore CANCELLED after %d file(s) to %s", count, destDir))
		return errImageRestoreCancelled
	}
	if err != nil {
		emit(100, "Restore failed")
		return ibFail(err)
	}
	if warned > 0 {
		writeBackupLog(fmt.Sprintf("ImageBrowse: restore finished with %d metadata warning(s) — files themselves are intact", warned))
	}
	if refused > 0 {
		writeBackupLog(fmt.Sprintf("ImageBrowse: %d file(s) REFUSED — their names in the image could have written outside %s. This indicates a crafted or corrupted image; the remaining files restored normally.", refused, destDir))
	}

	doneMsg := fmt.Sprintf("Restored %d file(s)", count)
	if warned > 0 {
		doneMsg += fmt.Sprintf(" — %d metadata warning(s), see the log", warned)
	}
	if refused > 0 {
		doneMsg += fmt.Sprintf(" — %d unsafe path(s) refused, see the log", refused)
	}
	emit(100, doneMsg)
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

// errImageRestoreCancelled is returned when the user cancels an in-flight
// image restore. Surfaced to the frontend so it can show "cancelled" rather
// than a scary error.
var errImageRestoreCancelled = errors.New("[NB-3429] restore cancelled by user")

// CancelImageRestore aborts an in-progress image restore, if one is running.
// The restore loop checks between files, so cancellation takes effect at the
// next file boundary (a large file in flight finishes its current write).
func (a *App) CancelImageRestore() {
	a.ibRestoreMu.Lock()
	cancel := a.ibRestoreCancel
	a.ibRestoreMu.Unlock()
	if cancel != nil {
		cancel()
		writeBackupLog("ImageBrowse: cancel requested for in-progress restore")
	}
}
