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
	Jobs  []InventoryJob         `json:"jobs"`
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
}

type CheckinResponse struct {
	Commands       []Command `json:"commands"`
	CheckinSeconds int       `json:"checkin_seconds"`
	Policy         Policy    `json:"policy"`
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
	RunUUID       string    `json:"run_uuid"`
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

type CommandResult struct {
	OK     bool                   `json:"ok"`
	Result map[string]interface{} `json:"result,omitempty"`
}
