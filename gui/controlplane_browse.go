package main

// controlplane_browse.go — the agent side of PORTAL-DELEGATED image browsing.
//
// The NimbusControl portal cannot browse volume (disk-image) backups itself:
// PBS has no file API over a fixed-index image, PBS credentials and
// encryption keys live on the agent, and the NTFS/FAT/exFAT parsing lives in
// this codebase (imagebrowse/). So the portal queues commands on the existing
// agent_commands channel and THIS file answers them, reusing the exact same
// core (imagebrowse_core.go) the local GUI Browse tab uses — one
// implementation, gated by the same file_restore policy.
//
// Command contract (payload -> result, all JSON):
//
//	image_partitions {pbs_server, backup_id, backup_time, backup_type}
//	    -> {disks: [{disk, partitions: [ImagePartition]}]}
//	image_scan       {..., disk, part}
//	    -> {total, entries: [...root children...]}   (also warms the tree cache)
//	image_dir        {..., disk, part, dir}
//	    -> {entries: [...children of dir...], truncated}
//	image_extract    {..., disk, part, paths[]}
//	    -> {artifact: true, bytes}  after streaming a ZIP to
//	       POST /api/agent/v1/commands/{id}/artifact
//
// Entries use compact keys to respect the server's result-body cap:
//	p=path  d=is_dir  s=size  m=mtime(unix)
//
// SECURITY MODEL: downloads through the portal are DATA ONLY — the zip holds
// plain file bytes. NTFS permissions and ADS deliberately do not travel this
// path (a browser download could not apply them anyway); full-fidelity
// restore remains a local-GUI operation on the machine itself.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"controlplane"
	"imagebrowse"
	"pbscommon"
)

// cpDirEntryCap bounds one directory listing in a command result. The server
// caps result bodies; a directory bigger than this returns its first entries
// plus truncated=true (the portal shows a notice).
const cpDirEntryCap = 20000

// cpHandleBrowseCommand answers the image_* commands. Returns ok=false for
// commands it does not own so cpHandleCommand can fall through.
func (a *App) cpHandleBrowseCommand(cmd controlplane.Command) (controlplane.CommandResult, bool) {
	switch cmd.Command {
	case "image_partitions", "image_scan", "image_dir", "image_extract":
	default:
		return controlplane.CommandResult{}, false
	}

	// The SAME policy gate the local GUI honours: if the MSP disabled file
	// restore on this machine, the portal gets the same refusal.
	if !ControlPolicy().FileRestore {
		return cpErr("file restore is disabled on this machine by policy"), true
	}

	p := cmd.Payload
	backupID, _ := p["backup_id"].(string)
	backupType, _ := p["backup_type"].(string)
	btF, _ := p["backup_time"].(float64)
	if backupID == "" || btF <= 0 {
		return cpErr("payload needs backup_id and backup_time"), true
	}
	snapshotID := time.Unix(int64(btF), 0).UTC().Format("2006-01-02T15:04:05Z")
	pbsHost, _ := p["pbs_server"].(string)
	pbsID := a.cpPickPBSID(pbsHost)

	switch cmd.Command {
	case "image_partitions":
		return a.cpImagePartitions(pbsID, backupID, snapshotID, backupType), true

	case "image_scan":
		disk, part, err := cpDiskPart(p)
		if err != nil {
			return cpErr(err.Error()), true
		}
		entries, lerr := a.ListImageContents(pbsID, backupID, snapshotID, backupType, disk, part, false)
		if lerr != nil {
			return cpErr(lerr.Error()), true
		}
		out, truncated := cpEncodeEntries(entries)
		return controlplane.CommandResult{OK: true, Result: map[string]interface{}{
			"entries": out, "truncated": truncated,
		}}, true

	case "image_dir":
		disk, part, err := cpDiskPart(p)
		if err != nil {
			return cpErr(err.Error()), true
		}
		dir, _ := p["dir"].(string)
		if dir == "" {
			dir = "/"
		}
		entries, lerr := a.ListImageDirectory(pbsID, backupID, snapshotID, backupType, disk, part, dir)
		if lerr != nil {
			// A cold cache (service restarted between scan and browse) is
			// recoverable: rescan and retry once, transparently.
			if strings.Contains(lerr.Error(), "NB-3428") {
				if _, serr := a.ListImageContents(pbsID, backupID, snapshotID, backupType, disk, part, false); serr == nil {
					entries, lerr = a.ListImageDirectory(pbsID, backupID, snapshotID, backupType, disk, part, dir)
				}
			}
			if lerr != nil {
				return cpErr(lerr.Error()), true
			}
		}
		out, truncated := cpEncodeEntries(entries)
		return controlplane.CommandResult{OK: true, Result: map[string]interface{}{
			"entries": out, "truncated": truncated,
		}}, true

	case "image_extract":
		disk, part, err := cpDiskPart(p)
		if err != nil {
			return cpErr(err.Error()), true
		}
		raw, _ := p["paths"].([]interface{})
		paths := make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				paths = append(paths, s)
			}
		}
		if len(paths) == 0 {
			return cpErr("no paths selected"), true
		}
		return a.cpImageExtract(cmd.ID, pbsID, backupID, snapshotID, backupType, disk, part, paths), true
	}
	return controlplane.CommandResult{}, false
}

// cpImagePartitions enumerates the snapshot's disks and each disk's
// partitions — one round trip gives the portal everything its picker needs.
func (a *App) cpImagePartitions(pbsID, backupID, snapshotID, backupType string) controlplane.CommandResult {
	cfg, err := a.resolveRestorePBS(pbsID)
	if err != nil {
		return cpErr(err.Error())
	}
	snaps, err := ListSnapshotsInline(cfg.BaseURL, cfg.AuthID, cfg.Secret,
		cfg.Datastore, cfg.Namespace, cfg.CertFingerprint, backupID)
	if err != nil {
		return cpErr("list snapshots: " + err.Error())
	}
	var disks []string
	for _, s := range snaps {
		if s.BackupID != backupID || s.BackupTime.UTC().Format("2006-01-02T15:04:05Z") != snapshotID {
			continue
		}
		for _, f := range s.Files {
			if strings.HasSuffix(f, ".img.fidx") {
				disks = append(disks, f)
			}
		}
		break
	}
	if len(disks) == 0 {
		return cpErr("snapshot has no disk images (is this a directory backup?)")
	}
	out := make([]map[string]interface{}, 0, len(disks))
	for _, d := range disks {
		parts, perr := a.ListImagePartitions(pbsID, backupID, snapshotID, backupType, d)
		entry := map[string]interface{}{"disk": d}
		if perr != nil {
			entry["error"] = perr.Error()
		} else {
			entry["partitions"] = parts
		}
		out = append(out, entry)
	}
	return controlplane.CommandResult{OK: true, Result: map[string]interface{}{"disks": out}}
}

// cpImageExtract streams the selection into a Store-method ZIP in ONE pass
// (PBS -> parser -> zip -> temp file), uploads it as the command's artifact,
// and deletes the temp. Interim storage is exactly the zip's size — the old
// extract-tree-then-zip needed it twice. The zip carries file DATA only: no
// NTFS permissions, no ADS — by design for browser downloads; full-fidelity
// restore is a local-GUI operation. Mirrors the GUI's DownloadImageSelection
// via the same streamImageZip core, so the two paths cannot drift.
func (a *App) cpImageExtract(cmdID int64, pbsID, backupID, snapshotID, backupType, disk string, part int, paths []string) controlplane.CommandResult {
	if cpClient == nil {
		return cpErr("control server client not initialised")
	}
	tmp, err := os.CreateTemp("", "nimbus-portal-*.zip")
	if err != nil {
		return cpErr("temp zip: " + err.Error())
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	var nFiles int
	var nBytes int64
	err = a.withPartition(pbsID, backupID, snapshotID, backupType, disk, part, true,
		func(fs imagebrowse.Filesystem, _ imagebrowse.Partition, _ *pbscommon.FIDXReaderAt) error {
			files, total, perr := planSelection(fs, paths)
			if perr != nil {
				return perr
			}
			nFiles, nBytes, perr = streamImageZip(fs, files, total, tmp, a.ibEmitTask, nil)
			if perr != nil {
				_ = tmp.Close()
				return perr
			}
			return tmp.Close()
		})
	if err != nil {
		return cpErr(err.Error())
	}
	if err := cpClient.PostCommandArtifact(cmdID, tmpPath); err != nil {
		return cpErr("artifact upload: " + err.Error())
	}
	writeBackupLog(fmt.Sprintf("Portal browse: streamed %d file(s) / %s into artifact for command %d (from %s)",
		nFiles, formatBytesGo(uint64(nBytes)), cmdID, disk))
	return controlplane.CommandResult{OK: true, Result: map[string]interface{}{
		"artifact": true, "bytes": nBytes, "files": nFiles,
	}}
}

// cpPickPBSID matches the run's PBS host to one of the agent's configured
// servers; empty means the default server (single-PBS installs).
func (a *App) cpPickPBSID(host string) string {
	if host == "" || a.config == nil {
		return ""
	}
	for i := range a.config.PBSServers {
		if strings.Contains(a.config.PBSServers[i].BaseURL, host) ||
			strings.Contains(host, strings.TrimPrefix(strings.TrimPrefix(a.config.PBSServers[i].BaseURL, "https://"), "http://")) {
			return a.config.PBSServers[i].ID
		}
	}
	return ""
}

func cpDiskPart(p map[string]interface{}) (string, int, error) {
	disk, _ := p["disk"].(string)
	partF, _ := p["part"].(float64)
	if disk == "" || partF < 1 {
		return "", 0, errors.New("payload needs disk and part (>=1)")
	}
	return disk, int(partF), nil
}

// cpEncodeEntries converts SnapshotEntry rows to the compact wire form,
// bounded by cpDirEntryCap.
func cpEncodeEntries(entries []SnapshotEntry) ([]map[string]interface{}, bool) {
	truncated := false
	if len(entries) > cpDirEntryCap {
		entries = entries[:cpDirEntryCap]
		truncated = true
	}
	out := make([]map[string]interface{}, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]interface{}{
			"p": e.Path, "d": e.IsDir, "s": e.Size, "m": e.ModTime,
		})
	}
	return out, truncated
}

func cpErr(msg string) controlplane.CommandResult {
	return controlplane.CommandResult{OK: false, Result: map[string]interface{}{"error": msg}}
}
