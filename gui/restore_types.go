package main

// restore_types.go — the SHAPES a restore speaks in, and nothing that acts.
//
// These types are untagged (both builds) while every function that USES them
// is `service`-only. That split is the whole point of the restore rewire: the
// console still names a snapshot entry, a partition, a search hit — it renders
// them — but it can no longer produce one, because the code that reads a
// datastore does not exist in its binary. The engine returns these over the
// local API; the GUI decodes them.
//
// So: if you are adding a field, it belongs here. If you are adding a function
// that reaches PBS, it does not.

import "time"

// RestoreMode picks where extracted files land on disk.
//
// Original restores back to the original filesystem location captured in the
// backup metadata sidecar (requires hostname + OS to match). The two Alternate
// modes write under opts.DestPath: Abs preserves the archive's full directory
// layout, Flat strips the longest common prefix of the user's selection so a
// single file lands at dest/<basename>.
type RestoreMode string

const (
	RestoreModeOriginal      RestoreMode = "original"
	RestoreModeAlternateAbs  RestoreMode = "alternate_abs"
	RestoreModeAlternateFlat RestoreMode = "alternate_flat"
)

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

// SearchMatchMode selects how Query is interpreted.
type SearchMatchMode string

const (
	// SearchModeName matches a case-insensitive substring against the file's
	// base name (the last path segment). The intuitive default.
	SearchModeName SearchMatchMode = "name"
	// SearchModeRegex matches a Go regular expression against the base name.
	// Case-insensitive by default (to match Windows expectations); add (?-i) in
	// the pattern to force case-sensitive matching.
	SearchModeRegex SearchMatchMode = "regex"
	// SearchModePath matches a case-insensitive substring against the whole
	// archive-relative path (directories included).
	SearchModePath SearchMatchMode = "path"
)

// SearchHit is a single matching entry found in a snapshot.
type SearchHit struct {
	BackupID     string `json:"backup_id"`
	SnapshotTime int64  `json:"snapshot_time"` // unix seconds
	Path         string `json:"path"`          // archive-relative, forward slashes
	OriginPath   string `json:"origin_path"`   // reconstructed absolute origin, "" if no meta
	IsDir        bool   `json:"is_dir"`
	Size         uint64 `json:"size"`
	ModTime      int64  `json:"mtime"`
	FromCache    bool   `json:"from_cache"`
}

// SearchResult bundles the matches with a summary of what was (and wasn't)
// scanned, so the UI can warn when results may be incomplete.
type SearchResult struct {
	Hits               []SearchHit `json:"hits"`
	SnapshotsInRange   int         `json:"snapshots_in_range"` // snapshots within the From/To period; 0 => widen the dates
	SnapshotsSearched  int         `json:"snapshots_searched"`
	SnapshotsSkipped   int         `json:"snapshots_skipped"` // uncached and AssembleMissing=false, or failed to assemble
	SnapshotsAssembled int         `json:"snapshots_assembled"`
	Truncated          bool        `json:"truncated"` // hit maxSearchHits
	Cancelled          bool        `json:"cancelled"`
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
