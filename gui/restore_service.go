package main

// restore_service.go — the service end of the restore seam.
//
// This is where a request that arrived over the local API becomes a call into
// the engine. It is `service`-tagged, so the console cannot compile a copy of
// it, and it is the ONLY implementation of api.RestoreHandler.
//
// THE POLICY CHECK IS NOT HERE. It ran in gui/api/restore.go before this file
// was reached, on the far side of an authenticated socket, and the engine
// functions each still carry their own `ControlPolicy().FileRestore` guard for
// the paths that do not come through the API at all (the control server's
// delegated browse). Two checks, both real, neither in the process being
// restrained.
//
// PBS CREDENTIALS ARE RESOLVED HERE, from config.json, by the process that
// owns it. That is the substantive change: the console used to do this.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"controlplane"
	"security"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// snapshotLabel renders a snapshot for the audit trail in the shape PBS names
// it, so a row in the portal's restore history can be pasted straight into a
// PBS query. Best-effort by design: a label nobody can resolve is worth more
// than an empty column, and this must never be a reason a restore does not
// run.
func snapshotLabel(ref api.SnapshotRef) string {
	when := ref.SnapshotID
	if when == "" && ref.SnapshotUnix > 0 {
		when = time.Unix(ref.SnapshotUnix, 0).UTC().Format("2006-01-02T15:04:05Z")
	}
	switch {
	case ref.BackupID == "" && when == "":
		return ""
	case when == "":
		return "host/" + ref.BackupID
	case ref.BackupID == "":
		return when
	}
	return "host/" + ref.BackupID + "/" + when
}

// restoreTime resolves the instant a SnapshotRef names.
//
// Either spelling is accepted (see SnapshotRef). A ref that names neither is a
// caller bug and says so — defaulting to "now", or to the newest snapshot,
// would restore something nobody asked for.
func restoreTime(ref api.SnapshotRef) (time.Time, error) {
	if ref.SnapshotUnix > 0 {
		return time.Unix(ref.SnapshotUnix, 0), nil
	}
	if ref.SnapshotID != "" {
		t, err := time.Parse("2006-01-02T15:04:05Z", ref.SnapshotID)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid snapshot id %q: %w", ref.SnapshotID, err)
		}
		return t, nil
	}
	return time.Time{}, errors.New("the request names no snapshot")
}

// restoreOptionsFor builds engine options from a wire ref, resolving the PBS
// server out of this machine's configuration.
func (a *App) restoreOptionsFor(ref api.SnapshotRef) (RestoreOptions, error) {
	if ref.BackupID == "" {
		return RestoreOptions{}, errors.New(errBackupIDRequired)
	}
	cfg, err := a.resolveRestorePBS(ref.PBSID)
	if err != nil {
		return RestoreOptions{}, err
	}
	when, err := restoreTime(ref)
	if err != nil {
		return RestoreOptions{}, err
	}
	return RestoreOptions{
		BaseURL:         cfg.BaseURL,
		AuthID:          cfg.AuthID,
		Secret:          cfg.Secret,
		Datastore:       cfg.Datastore,
		Namespace:       cfg.Namespace,
		CertFingerprint: cfg.CertFingerprint,
		DataPath:        cfg.DataPath,
		BackupID:        ref.BackupID,
		SnapshotTime:    when,
	}, nil
}

// decodeParams unmarshals an op's parameters, naming the op when it cannot.
func decodeParams[T any](op string, raw json.RawMessage) (T, error) {
	var out T
	if len(raw) == 0 {
		return out, fmt.Errorf("%s: no parameters were sent", op)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("%s: the parameters could not be read: %w", op, err)
	}
	return out, nil
}

// checkRestoreDest refuses a destination a restore op would write to as
// SYSTEM when security.ValidatePath does. It is the check the console made
// alone until V4-CONSOLE C3 (finding F1: the API cannot yet tell callers
// apart, so a check in one client is no check at all). V4-LOCAL-API P3
// replaces it with destinations confined to the caller's own profile.
//
// An empty path passes: each op refuses that itself, as NB-3009.
func checkRestoreDest(path string) error {
	if path == "" {
		return nil
	}
	if err := security.ValidatePath(path); err != nil {
		return fmt.Errorf("%s :: %v", errDestPathInvalid, err)
	}
	return nil
}

// RestoreQuery answers the bounded operations. See gui/api/restore.go for why
// the dispatch is by name.
func (a *App) RestoreQuery(op string, raw json.RawMessage) (any, error) {
	switch op {
	case api.RestoreOpSnapshots:
		p, err := decodeParams[api.ListSnapshotsParams](op, raw)
		if err != nil {
			return nil, err
		}
		cfg, err := a.resolveRestorePBS(p.PBSID)
		if err != nil {
			return nil, err
		}
		return ListSnapshotsInline(cfg.BaseURL, cfg.AuthID, cfg.Secret,
			cfg.Datastore, cfg.Namespace, cfg.CertFingerprint, p.BackupID, cfg.DataPath)

	case api.RestoreOpContents:
		p, err := decodeParams[api.ListContentsParams](op, raw)
		if err != nil {
			return nil, err
		}
		opts, err := a.restoreOptionsFor(p.SnapshotRef)
		if err != nil {
			return nil, err
		}
		return ListSnapshotContentsInline(opts, "", p.ForceRefresh)

	case api.RestoreOpMeta:
		p, err := decodeParams[api.SnapshotMetaParams](op, raw)
		if err != nil {
			return nil, err
		}
		opts, err := a.restoreOptionsFor(p.SnapshotRef)
		if err != nil {
			return nil, err
		}
		return ReadSnapshotMetaInline(opts, false)

	case api.RestoreOpVolumePartitions:
		p, err := decodeParams[api.VolumeRef](op, raw)
		if err != nil {
			return nil, err
		}
		return a.ListVolumePartitions(p.PBSID, p.BackupID, p.SnapshotID, p.BackupType, p.DiskArchive)

	case api.RestoreOpVolumeFiles:
		p, err := decodeParams[api.VolumeFilesParams](op, raw)
		if err != nil {
			return nil, err
		}
		entries, err := a.ListVolumeFiles(p.PBSID, p.BackupID, p.SnapshotID, p.BackupType,
			p.DiskArchive, p.PartIndex, p.ForceRefresh)
		if err != nil {
			return nil, err
		}
		// The truncation flag rides WITH the answer — see VolumeFilesResult.
		//
		// It is false, and has been since per-directory listing replaced the
		// flat one: the whole tree stays here and the console fetches a
		// directory at a time, so nothing is truncated on the way out. The
		// field is carried rather than dropped because the walk itself still
		// has a cap (volumeWalkCap), and the day a scan hits it this is where
		// the answer belongs.
		return api.VolumeFilesResult{Entries: entries, Truncated: false}, nil

	case api.RestoreOpVolumeDirectory:
		p, err := decodeParams[api.VolumeDirectoryParams](op, raw)
		if err != nil {
			return nil, err
		}
		return a.ListVolumeDirectory(p.PBSID, p.BackupID, p.SnapshotID, p.BackupType,
			p.DiskArchive, p.PartIndex, p.Dir)

	case api.RestoreOpSpace:
		p, err := decodeParams[api.SpaceParams](op, raw)
		if err != nil {
			return nil, err
		}
		return checkDownloadSpace(p)
	}
	return nil, fmt.Errorf("restore query %q is declared but not implemented", op)
}

// RestoreJob runs the long operations.
//
// Progress from the archive engine is 0-1 (RestoreOptions.OnProgress's own
// convention) and is scaled once, here, at the boundary — the same single
// conversion the Wails binding used to do, moved rather than duplicated.
//
// THE CONTEXT IS DELIBERATELY UNUSED. Every cancellable operation here already
// has its own cancellation, reached through RestoreControl, and it stops at a
// boundary the engine chooses (a snapshot for search, a file for an image
// restore) so nothing is left half-written. Cancelling the goroutine's context
// instead would abort mid-write, and having two ways to stop the same work is
// how one of them ends up subtly wrong. The parameter stays in the signature
// because the API layer owns the job lifetime and will need it the day an
// engine grows real context support.
func (a *App) RestoreJob(_ context.Context, op string, raw json.RawMessage, progress api.RestoreProgress) (any, error) {
	switch op {
	case api.RestoreOpRestore:
		p, err := decodeParams[api.RestoreParams](op, raw)
		if err != nil {
			return nil, err
		}
		// The arguments are checked before the PBS server is resolved: a
		// refusal must not depend on whether a server is configured.
		mode := api.RestoreMode(p.Mode)
		if mode == "" {
			mode = api.RestoreModeAlternateAbs
		}
		if mode != api.RestoreModeOriginal {
			if p.DestPath == "" {
				return nil, errors.New(errDestPathRequired)
			}
			if err := checkRestoreDest(p.DestPath); err != nil {
				return nil, err
			}
		}
		opts, err := a.restoreOptionsFor(p.SnapshotRef)
		if err != nil {
			return nil, err
		}
		opts.DestPath = p.DestPath
		opts.Mode = mode
		opts.AllowCrossHost = p.AllowCrossHost
		opts.IncludePaths = p.IncludePaths
		opts.Overwrite = p.Overwrite
		opts.RestoreACLs = p.RestoreACLs
		opts.RestoreADS = p.RestoreADS
		opts.RestoreTimestamps = p.RestoreTimestamps
		opts.OnProgress = func(pct float64, msg string) {
			progress(pct*100, msg, 0, 0, 0, -1)
		}
		// REPORTED. This restore fetched a backup key and no backup run will
		// follow it, so the server's release audit sees it as unmatched by
		// construction -- see V4-SPEC 9.7 and restore_report_service.go. The
		// report never changes what the caller gets back.
		return nil, reportedRestore(controlplane.RestoreArchive, snapshotLabel(p.SnapshotRef),
			len(p.IncludePaths), func() error { return RestoreSnapshotInline(opts) })

	case api.RestoreOpDownload:
		p, err := decodeParams[api.DownloadParams](op, raw)
		if err != nil {
			return nil, err
		}
		if err := checkRestoreDest(p.DestPath); err != nil {
			return nil, err
		}
		return nil, a.downloadSelection(p, progress)

	case api.RestoreOpSearch:
		p, err := decodeParams[api.SearchParams](op, raw)
		if err != nil {
			return nil, err
		}
		cfg, err := a.resolveRestorePBS(p.PBSID)
		if err != nil {
			return nil, err
		}
		var from, to time.Time
		if p.FromUnix > 0 {
			from = time.Unix(p.FromUnix, 0)
		}
		if p.ToUnix > 0 {
			to = time.Unix(p.ToUnix, 0)
		}
		return SearchFilesInline(SearchOptions{
			BaseURL:         cfg.BaseURL,
			AuthID:          cfg.AuthID,
			Secret:          cfg.Secret,
			Datastore:       cfg.Datastore,
			Namespace:       cfg.Namespace,
			CertFingerprint: cfg.CertFingerprint,
			DataPath:        cfg.DataPath,
			HostPrefix:      p.HostPrefix,
			Query:           p.Query,
			Mode:            api.SearchMatchMode(p.Mode),
			From:            from,
			To:              to,
			AssembleMissing: p.AssembleMissing,
			// Search's own progress is already 0-100.
			OnProgress: func(pct float64, msg string) { progress(pct, msg, 0, 0, 0, -1) },
		})

	case api.RestoreOpVolumeDownload:
		p, err := decodeParams[api.VolumeDownloadParams](op, raw)
		if err != nil {
			return nil, err
		}
		if err := checkRestoreDest(p.DestPath); err != nil {
			return nil, err
		}
		return nil, withImageProgress(progress, func() error {
			return a.DownloadFilesFromVolume(p.PBSID, p.BackupID, p.SnapshotID, p.BackupType,
				p.DiskArchive, p.PartIndex, p.IncludePaths, p.DestPath, p.AsZip, p.NeededBytes)
		})

	case api.RestoreOpVolumeFileRestore:
		p, err := decodeParams[api.VolumeFileRestoreParams](op, raw)
		if err != nil {
			return nil, err
		}
		if err := checkRestoreDest(p.DestDir); err != nil {
			return nil, err
		}
		// The SOURCE is a stored disk image; the operation is still a file
		// restore, which is why the kind is `volume` and there is no kind
		// meaning "a whole partition" for it to be confused with.
		return nil, reportedRestore(controlplane.RestoreVolume,
			snapshotLabel(api.SnapshotRef{BackupID: p.BackupID, SnapshotID: p.SnapshotID}),
			len(p.IncludePaths), func() error {
				return withImageProgress(progress, func() error {
					return a.RestoreFilesFromVolume(p.PBSID, p.BackupID, p.SnapshotID, p.BackupType,
						p.DiskArchive, p.PartIndex, p.IncludePaths, p.DestDir, p.KeepStructure,
						p.Overwrite, p.RestoreMtimes, p.RestoreACLs, p.RestoreADS, p.NeededBytes)
				})
			})
	}
	return nil, fmt.Errorf("restore job %q is declared but not implemented", op)
}

// RestoreControl stops work already running.
//
// Both cancels reach the engine's OWN cancellation, not the job's context. The
// engine unwinds deliberately — a search stops at a snapshot boundary, an
// volume-file restore at a file boundary — and killing its context instead would
// leave a half-written file where the product's contract says "cancelled".
func (a *App) RestoreControl(op string, _ json.RawMessage) error {
	switch op {
	case api.RestoreOpCancelSearch:
		CancelFileSearch()
		return nil
	case api.RestoreOpCancelVolumeFileRestore:
		a.CancelVolumeFileRestore()
		return nil
	}
	return fmt.Errorf("restore control %q is declared but not implemented", op)
}
