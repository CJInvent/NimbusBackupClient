//go:build service
// +build service

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

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// restoreTime resolves the instant a SnapshotRef names.
//
// Either spelling is accepted (see SnapshotRef). A ref that names neither is a
// caller bug and says so — defaulting to "now", or to the newest snapshot,
// would restore something nobody asked for.
func restoreTime(ref SnapshotRef) (time.Time, error) {
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
func (a *App) restoreOptionsFor(ref SnapshotRef) (RestoreOptions, error) {
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

// RestoreQuery answers the bounded operations. See gui/api/restore.go for why
// the dispatch is by name.
func (a *App) RestoreQuery(op string, raw json.RawMessage) (any, error) {
	switch op {
	case opSnapshots:
		p, err := decodeParams[ListSnapshotsParams](op, raw)
		if err != nil {
			return nil, err
		}
		cfg, err := a.resolveRestorePBS(p.PBSID)
		if err != nil {
			return nil, err
		}
		return ListSnapshotsInline(cfg.BaseURL, cfg.AuthID, cfg.Secret,
			cfg.Datastore, cfg.Namespace, cfg.CertFingerprint, p.BackupID)

	case opContents:
		p, err := decodeParams[ListContentsParams](op, raw)
		if err != nil {
			return nil, err
		}
		opts, err := a.restoreOptionsFor(p.SnapshotRef)
		if err != nil {
			return nil, err
		}
		return ListSnapshotContentsInline(opts, "", p.ForceRefresh)

	case opMeta:
		p, err := decodeParams[SnapshotMetaParams](op, raw)
		if err != nil {
			return nil, err
		}
		opts, err := a.restoreOptionsFor(p.SnapshotRef)
		if err != nil {
			return nil, err
		}
		return ReadSnapshotMetaInline(opts, false)

	case opVolumePartitions:
		p, err := decodeParams[VolumeRef](op, raw)
		if err != nil {
			return nil, err
		}
		return a.ListVolumePartitions(p.PBSID, p.BackupID, p.SnapshotID, p.BackupType, p.DiskArchive)

	case opVolumeFiles:
		p, err := decodeParams[VolumeFilesParams](op, raw)
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
		return VolumeFilesResult{Entries: entries, Truncated: false}, nil

	case opVolumeDirectory:
		p, err := decodeParams[VolumeDirectoryParams](op, raw)
		if err != nil {
			return nil, err
		}
		return a.ListVolumeDirectory(p.PBSID, p.BackupID, p.SnapshotID, p.BackupType,
			p.DiskArchive, p.PartIndex, p.Dir)
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
	case opRestore:
		p, err := decodeParams[RestoreParams](op, raw)
		if err != nil {
			return nil, err
		}
		opts, err := a.restoreOptionsFor(p.SnapshotRef)
		if err != nil {
			return nil, err
		}
		mode := RestoreMode(p.Mode)
		if mode == "" {
			mode = RestoreModeAlternateAbs
		}
		if mode != RestoreModeOriginal && p.DestPath == "" {
			return nil, errors.New(errDestPathRequired)
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
		return nil, RestoreSnapshotInline(opts)

	case opDownload:
		p, err := decodeParams[DownloadParams](op, raw)
		if err != nil {
			return nil, err
		}
		return nil, a.downloadSelection(p, progress)

	case opSearch:
		p, err := decodeParams[SearchParams](op, raw)
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
			HostPrefix:      p.HostPrefix,
			Query:           p.Query,
			Mode:            SearchMatchMode(p.Mode),
			From:            from,
			To:              to,
			AssembleMissing: p.AssembleMissing,
			// Search's own progress is already 0-100.
			OnProgress: func(pct float64, msg string) { progress(pct, msg, 0, 0, 0, -1) },
		})

	case opVolumeDownload:
		p, err := decodeParams[VolumeDownloadParams](op, raw)
		if err != nil {
			return nil, err
		}
		return nil, withImageProgress(progress, func() error {
			return a.DownloadFilesFromVolume(p.PBSID, p.BackupID, p.SnapshotID, p.BackupType,
				p.DiskArchive, p.PartIndex, p.IncludePaths, p.DestPath, p.AsZip, p.NeededBytes)
		})

	case opVolumeFileRestore:
		p, err := decodeParams[VolumeFileRestoreParams](op, raw)
		if err != nil {
			return nil, err
		}
		return nil, withImageProgress(progress, func() error {
			return a.RestoreFilesFromVolume(p.PBSID, p.BackupID, p.SnapshotID, p.BackupType,
				p.DiskArchive, p.PartIndex, p.IncludePaths, p.DestDir, p.KeepStructure,
				p.Overwrite, p.RestoreMtimes, p.RestoreACLs, p.RestoreADS, p.NeededBytes)
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
	case opCancelSearch:
		CancelFileSearch()
		return nil
	case opCancelVolumeFileRestore:
		a.CancelVolumeFileRestore()
		return nil
	}
	return fmt.Errorf("restore control %q is declared but not implemented", op)
}
