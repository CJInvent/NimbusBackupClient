// Package controlplane implements the NimbusControl agent contract
// (NimbusControl docs/AGENT-API.md, v1). Standard library only.
package controlplane

// EnrollRequest — POST /api/agent/v1/enroll (one-time token).
type EnrollRequest struct {
	Token        string `json:"token"`
	Hostname     string `json:"hostname"`
	OSInfo       string `json:"os_info,omitempty"`
	AgentVersion string `json:"agent_version,omitempty"`
	MachineFP    string `json:"machine_fp,omitempty"`
}

// EnrollResponse — the ONLY time the secret crosses the wire. Callers must
// hand Secret to the TPM-backed secret store immediately.
type EnrollResponse struct {
	AgentID        int64  `json:"agent_id"`
	Secret         string `json:"secret"`
	CheckinSeconds int    `json:"checkin_seconds"`
}

// InventoryJob feeds server-side missed-backup detection: the server derives
// a standing expectation per (agent, job) from IntervalSeconds. REQUIRED for
// every enabled job or dead machines go unnoticed.
type InventoryJob struct {
	Name            string `json:"name"`
	IntervalSeconds int    `json:"interval_seconds"`
}

// Inventory is display + expectation telemetry. Server-side it is bounded
// (64 KB / depth 6 / 2000 elements) and sanitized; keep it lean regardless.
type Inventory struct {
	Jobs []InventoryJob `json:"jobs"`

	// BreakGlassFileRestore reports that this agent is honouring its LOCAL
	// emergency override (see breakglass.go) because the control plane has
	// been unreachable — i.e. file restore is active here even though the
	// resolved policy says otherwise.
	//
	// Emitted ALWAYS, not omitempty: an absent key would be ambiguous between
	// "no override" and "an older agent that cannot report one", and the whole
	// point of the field is an audit trail. Reserved in the server contract
	// (NimbusControl docs/AGENT-API.md) as an audit signal to SURFACE, never a
	// policy value to trust — the agent is reporting what it did while the
	// server was unreachable.
	BreakGlassFileRestore bool `json:"break_glass_file_restore"`

	// PBSReachable is this agent's own report of whether ITS configured PBS
	// server answered a lightweight connectivity check performed around
	// this same check-in cycle (see pbscommon.PBSClient.CheckConnectivity).
	// Pointer, not plain bool: omitted (nil) means "no reading this cycle"
	// -- an agent that has no PBS configured yet, or one whose check hasn't
	// run -- which the server must NOT confuse with a confirmed false. A
	// confirmed unreachable is Ptr(false), not omission.
	PBSReachable *bool `json:"pbs_reachable,omitempty"`

	Extra map[string]interface{} `json:"extra,omitempty"`
}

type CheckinRequest struct {
	AgentVersion string     `json:"agent_version,omitempty"`
	Inventory    *Inventory `json:"inventory,omitempty"`
}

// Command from the server queue. Handlers MUST be idempotent: a command
// stays 'sent' until its result is posted, so a crash mid-command means it
// is visible server-side but will not be re-delivered — and expires in 24 h.
type Command struct {
	ID      int64                  `json:"id"`
	Command string                 `json:"command"`
	Payload map[string]interface{} `json:"payload"`
}

// Policy is the resolved client-capability set (agent > org > global >
// default). MUST be re-applied on every check-in — values change server-side.
type Policy struct {
	// FileRestore=false: the GUI must hide/disable its restore browser and
	// the local API must refuse restore operations on this machine.
	FileRestore bool `json:"file_restore"`

	// RestrictUnmanagedBackups=true: the agent must refuse backups it
	// authored ITSELF — its local scheduler's jobs and anything started at
	// the console — and run only work this control plane sent it. It does
	// not gate restore, and it does not stop a run already under way.
	//
	// Note the polarity. Every other capability here is named for the
	// PERMISSION, so a Policy zero value denies everything and an agent that
	// has never checked in fails closed. This one is named for the
	// RESTRICTION, so the zero value PERMITS. That is deliberate: failing
	// closed here means a machine that cannot reach the server stops backing
	// up, which is the outcome the product exists to prevent. See
	// unmanaged.go, and the key's note in NimbusControl Core\Policy.
	RestrictUnmanagedBackups bool `json:"restrict_unmanaged_backups"`
}

type CheckinResponse struct {
	Commands       []Command `json:"commands"`
	CheckinSeconds int       `json:"checkin_seconds"`
	Policy         Policy    `json:"policy"`

	// CheckinOffsetSeconds is this agent's assigned slot within the
	// check-in interval's epoch-aligned grid (see NextAligned in
	// schedule.go). Server-computed (Nimbus\Agents\PollSchedule),
	// deterministic per agent ID -- reconnecting or restarting never
	// reassigns it. Zero is a valid offset, not "unset".
	CheckinOffsetSeconds int `json:"checkin_offset_seconds"`

	// PBSPollIntervalSeconds/PBSPollOffsetSeconds are the server-assigned
	// schedule for the INDEPENDENT PBS-connectivity poll (see
	// gui/controlplane_pbspoll.go) -- decoupled from the check-in cadence
	// above specifically so a live PBS network round-trip does not run on
	// every ~120s check-in across a whole fleet. Same NextAligned grid
	// mechanism, same PollSchedule server-side offset assignment, just a
	// separate (longer) interval.
	PBSPollIntervalSeconds int `json:"pbs_poll_interval_seconds"`
	PBSPollOffsetSeconds   int `json:"pbs_poll_offset_seconds"`
}

// RunStatus values — the server's state machine is forward-only
// (preparing -> running -> terminal); replays can never regress it.
type RunStatus string

const (
	// StatusPreparing: job accepted, VSS snapshot NOT yet confirmed.
	StatusPreparing RunStatus = "preparing"
	// StatusRunning: send ONLY after the VSS shadow copy exists. The
	// portal's "backing up" state is defined as VSS-confirmed.
	StatusRunning RunStatus = "running"
	// StatusVSSFailed: VSS creation failed — terminal, own alert runbook.
	StatusVSSFailed RunStatus = "vss_failed"
	StatusSuccess   RunStatus = "success"
	StatusWarning   RunStatus = "warning"
	StatusFailed    RunStatus = "failed"
)

// RunReport — POST /api/agent/v1/runs. Post the same RunUUID at every phase
// change. The PBS snapshot triple on success is what lets the server detect
// PBS-side GC/prune later — omit it and the run is never reconciled.
type RunReport struct {
	RunUUID string `json:"run_uuid"`
	// RequestID is set ONLY for a run the server itself requested (a
	// portal "Back up now" click resolves to a run_backup command whose
	// payload carries request_id — see cpHandleCommand's "run_backup"
	// case). Empty for scheduled and unattributed-manual runs, which is
	// correct, not a gap: the server never issued a request for those.
	// Sent on every report for the run, not just the first — the server
	// only needs it once to ack, but sending it every time means a lost
	// first report still lets a later one carry the link.
	RequestID     string    `json:"request_id,omitempty"`
	JobName       string    `json:"job_name"`
	BackupType    string    `json:"backup_type"` // "directory" | "machine"
	Status        RunStatus `json:"status"`
	StartedAt     string    `json:"started_at"` // ISO 8601 (RFC 3339)
	FinishedAt    string    `json:"finished_at,omitempty"`
	BytesTotal    int64     `json:"bytes_total,omitempty"`
	BytesUploaded int64     `json:"bytes_uploaded,omitempty"`
	PBSServer     string    `json:"pbs_server,omitempty"`
	PBSDatastore  string    `json:"pbs_datastore,omitempty"`
	PBSNamespace  string    `json:"pbs_namespace,omitempty"`
	PBSBackupType string    `json:"pbs_backup_type,omitempty"` // "host"
	PBSBackupID   string    `json:"pbs_backup_id,omitempty"`
	PBSBackupTime int64     `json:"pbs_backup_time,omitempty"`
	ErrorSummary  string    `json:"error_summary,omitempty"`
	LogTail       string    `json:"log_tail,omitempty"` // <=16 KB
}

// RunEvent — POST /api/agent/v1/runs/{uuid}/events. One granular milestone
// line for a run's checkpoint timeline: a specific partition finishing, a
// VSS sub-step. Checkpoint must be one of the four the server recognizes
// (backup_start, snapshot_vss, disks_partitions, finalization) or the
// server rejects it outright — this drives which section of the portal's
// timeline the line appears under, so getting it wrong is a real error,
// unlike Level, which the server silently normalizes to "info" if invalid
// rather than rejecting the whole milestone over a cosmetic field.
type RunEvent struct {
	Checkpoint string `json:"checkpoint"`
	Level      string `json:"level,omitempty"` // "info" | "warning" | "error"; server defaults to info
	Message    string `json:"message"`
}

type CommandResult struct {
	OK     bool                   `json:"ok"`
	Result map[string]interface{} `json:"result,omitempty"`
}
