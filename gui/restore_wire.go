package main

// restore_wire.go — what the console SAYS when it asks the service to restore.
//
// One definition of each request, compiled into both binaries: the GUI fills
// it in, the service reads it back. The alternative — a struct in the console
// and a matching one in the service — is two things to keep in step across a
// process boundary, and the first field that drifts fails at runtime on a
// customer's machine rather than at compile time here.
//
// NOTE WHAT IS ABSENT: no base URL, no auth id, no secret, no datastore. The
// console names a PBS server by its configured id and nothing more. Before
// this rewire it resolved credentials out of config.json to build a reader
// itself; it no longer sees them, and that is a property of these types rather
// than of anyone's discipline.

// Op names. Declared here as constants because both halves reference them and
// a typo in a string literal on one side is a 400 at runtime; the gate's own
// copy of the list lives in gui/api/restore.go, which is what refuses an op
// this file forgot to declare.
const (
	opSnapshots    = "snapshots"
	opContents     = "contents"
	opMeta         = "meta"
	opRestore      = "restore"
	opDownload     = "download"
	opSearch       = "search"
	opCancelSearch = "cancel-search"

	opImagePartitions = "image-partitions"
	opImageContents   = "image-contents"
	opImageDirectory  = "image-directory"
	opImageDownload   = "image-download"
	opImageRestore    = "image-restore"
	opCancelImage     = "cancel-image"
)

// SnapshotRef names one snapshot of one backup group on one PBS server.
//
// SnapshotUnix and SnapshotID are two spellings of the same instant because
// the console holds two: the tree view carries the `unix` field it got from a
// listing, the restore dialog carries the RFC-3339-shaped id. Rather than make
// every caller convert, the service accepts either and says so.
type SnapshotRef struct {
	PBSID        string `json:"pbs_id"`
	BackupID     string `json:"backup_id"`
	SnapshotUnix int64  `json:"snapshot_unix,omitempty"`
	SnapshotID   string `json:"snapshot_id,omitempty"`
}

// ListSnapshotsParams — op "snapshots".
type ListSnapshotsParams struct {
	PBSID    string `json:"pbs_id"`
	BackupID string `json:"backup_id"`
}

// ListContentsParams — op "contents".
type ListContentsParams struct {
	SnapshotRef
	ForceRefresh bool `json:"force_refresh"`
}

// SnapshotMetaParams — op "meta".
type SnapshotMetaParams struct {
	SnapshotRef
}

// RestoreParams — op "restore". Mirrors the Wails binding's arguments, which
// is why the reserved ACL/ADS flags are here: the console still sends them and
// the service still ignores all but timestamps (see RestoreOptions).
type RestoreParams struct {
	SnapshotRef
	DestPath          string   `json:"dest_path"`
	Mode              string   `json:"mode"`
	IncludePaths      []string `json:"include_paths"`
	AllowCrossHost    bool     `json:"allow_cross_host"`
	RestoreACLs       bool     `json:"restore_acls"`
	RestoreADS        bool     `json:"restore_ads"`
	RestoreTimestamps bool     `json:"restore_timestamps"`
	Overwrite         bool     `json:"overwrite"`
}

// DownloadParams — op "download".
type DownloadParams struct {
	SnapshotRef
	IncludePaths []string `json:"include_paths"`
	DestPath     string   `json:"dest_path"`
	AsZip        bool     `json:"as_zip"`
	NeededBytes  int64    `json:"needed_bytes"`
}

// SearchParams — op "search".
type SearchParams struct {
	PBSID           string `json:"pbs_id"`
	HostPrefix      string `json:"host_prefix"`
	Query           string `json:"query"`
	Mode            string `json:"mode"`
	FromUnix        int64  `json:"from_unix"`
	ToUnix          int64  `json:"to_unix"`
	AssembleMissing bool   `json:"assemble_missing"`
}

// ImageRef names a partition inside a volume backup's disk image.
type ImageRef struct {
	PBSID       string `json:"pbs_id"`
	BackupID    string `json:"backup_id"`
	SnapshotID  string `json:"snapshot_id"`
	BackupType  string `json:"backup_type"`
	DiskArchive string `json:"disk_archive"`
	PartIndex   int    `json:"part_index"`
}

// ImageContentsParams — op "image-contents".
type ImageContentsParams struct {
	ImageRef
	ForceRefresh bool `json:"force_refresh"`
}

// ImageDirectoryParams — op "image-directory".
type ImageDirectoryParams struct {
	ImageRef
	Dir string `json:"dir"`
}

// ImageDownloadParams — op "image-download".
type ImageDownloadParams struct {
	ImageRef
	IncludePaths []string `json:"include_paths"`
	DestPath     string   `json:"dest_path"`
	AsZip        bool     `json:"as_zip"`
	NeededBytes  int64    `json:"needed_bytes"`
}

// ImageRestoreParams — op "image-restore".
type ImageRestoreParams struct {
	ImageRef
	IncludePaths  []string `json:"include_paths"`
	DestDir       string   `json:"dest_dir"`
	KeepStructure bool     `json:"keep_structure"`
	Overwrite     bool     `json:"overwrite"`
	RestoreMtimes bool     `json:"restore_mtimes"`
	RestoreACLs   bool     `json:"restore_acls"`
	RestoreADS    bool     `json:"restore_ads"`
	NeededBytes   int64    `json:"needed_bytes"`
}

// ImageContentsResult carries a partition scan's root listing together with
// the truncation flag.
//
// The flag travels WITH the entries deliberately. In-process it was a separate
// accessor the console called afterwards (LastImageListTruncated), which works
// only while the answer and the question share a process — over a socket, with
// two consoles or a delegated browse in between, "the last one" names nothing.
type ImageContentsResult struct {
	Entries   []SnapshotEntry `json:"entries"`
	Truncated bool            `json:"truncated"`
}
