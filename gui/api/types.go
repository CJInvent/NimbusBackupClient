package api

// BackupRequest represents a backup job request
type BackupRequest struct {
	BackupType  string   `json:"backup_type"` // "directory" or "machine"
	BackupID    string   `json:"backup_id"`
	BackupDirs  []string `json:"backup_dirs"`
	DiskTargets []string `json:"disk_targets,omitempty"`
	ExcludeList []string `json:"exclude_list,omitempty"`
	UseVSS      bool     `json:"use_vss"`
	Compression string   `json:"compression,omitempty"` // "fastest", "default", "better", "best"
}

// BackupResponse represents the result of a backup operation
type BackupResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	JobID   string `json:"job_id,omitempty"`
	Error   string `json:"error,omitempty"`
}

// StatusResponse represents the service status
type StatusResponse struct {
	Running       bool           `json:"running"`
	Version       string         `json:"version"`
	ActiveJobs    int            `json:"active_jobs"`
	LastBackup    string         `json:"last_backup,omitempty"`
	Configuration map[string]any `json:"configuration"`
	// SecurityWarnings is the machine's security posture, one sentence per
	// problem; empty when nothing is wrong. Never null.
	SecurityWarnings []string `json:"security_warnings"`
}

// ScheduledJob is one of this machine's local scheduled jobs, as
// scheduled_jobs.json stores it and as GET /jobs returns it: whole, because the
// console's job list and edit form read every field. (GET /jobs used to return
// a five-field summary nothing read.) The service's own type aliases this one.
type ScheduledJob struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	ScheduleTime string   `json:"scheduleTime"` // HH:MM format
	RunAtStartup bool     `json:"runAtStartup"`
	BackupDirs   []string `json:"backupDirs"`
	DiskTargets  []string `json:"diskTargets"` // persistent targets for machine (full-volume) backups
	BackupID     string   `json:"backupId"`
	UseVSS       bool     `json:"useVSS"`
	BackupType   string   `json:"backupType"`
	ExcludeList  []string `json:"excludeList"`
	Compression  string   `json:"compression"`       // "fastest", "default", "better", "best"
	LastRun      string   `json:"lastRun,omitempty"` // ISO timestamp
	NextRun      string   `json:"nextRun,omitempty"` // ISO timestamp
	Enabled      bool     `json:"enabled"`
}

// JobsResponse is GET /jobs.
type JobsResponse struct {
	Jobs []ScheduledJob `json:"jobs"`
}

// ErrorResponse represents an API error
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    int    `json:"code"`
	Details string `json:"details,omitempty"`
}
