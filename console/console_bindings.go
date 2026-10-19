package main

// The console's bindings, other than restore (restore_bindings.go) and the
// folder picker (pathpicker.go). Each is one call to the local API, as
// docs/V4-CONSOLE.md §3's "After" column lists them; none has a fallback for
// a service that does not answer.

import (
	"fmt"
	stdruntime "runtime"
	"time"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// ---- this console and this machine -----------------------------------------

// GetVersion is the console's own build stamp.
func (c *Console) GetVersion() string { return appVersion }

// GetSystemInfo is what the screens know about the machine before anything
// else loads: the hostname (GET /status), whether the service answered, and
// the console's operating system, which the restore screen compares with a
// snapshot's. "is_admin" is gone: no screen read it, and the service decides
// what a caller may do (docs/V4-LOCAL-API.md).
func (c *Console) GetSystemInfo() map[string]interface{} {
	out := map[string]interface{}{
		"mode":              "Service Unavailable",
		"hostname":          "",
		"service_available": false,
		"os":                stdruntime.GOOS,
	}
	st, err := c.api.GetStatus()
	if err != nil {
		return out
	}
	c.up.Store(true)
	out["mode"] = "Service Mode"
	out["service_available"] = true
	if h, ok := st.Configuration["hostname"].(string); ok {
		out["hostname"] = h
	}
	return out
}

// GetConfigWithHostname is the service's configuration as the console may see
// it (/status "configuration"; no secret, only whether one is stored).
func (c *Console) GetConfigWithHostname() (map[string]interface{}, error) {
	st, err := c.api.GetStatus()
	if err != nil {
		return nil, err
	}
	return st.Configuration, nil
}

// GetLastBackupDirs is the folders of the last successful directory backup,
// which pre-fill the backup form.
func (c *Console) GetLastBackupDirs() ([]string, error) {
	st, err := c.api.GetStatus()
	if err != nil {
		return nil, err
	}
	raw, _ := st.Configuration["last_backup_dirs"].([]interface{})
	dirs := make([]string, 0, len(raw))
	for _, d := range raw {
		if s, ok := d.(string); ok {
			dirs = append(dirs, s)
		}
	}
	return dirs, nil
}

// GetSecurityWarnings is the machine's security posture, as the service
// computes it.
func (c *Console) GetSecurityWarnings() ([]string, error) {
	st, err := c.api.GetStatus()
	if err != nil {
		return nil, err
	}
	if st.SecurityWarnings == nil {
		return []string{}, nil
	}
	return st.SecurityWarnings, nil
}

// GetExchangeStatus is Exchange detection (GET /exchange).
func (c *Console) GetExchangeStatus() (api.ExchangeStatus, error) {
	out, err := c.api.GetExchange(false)
	if err != nil {
		return api.ExchangeStatus{}, err
	}
	return out.Status, nil
}

// QueryExchangeLogMode is the transaction-log posture (GET
// /exchange?log_mode=1), slow because the service runs the Exchange
// Management Shell; the screen asks for it only where Exchange is installed.
func (c *Console) QueryExchangeLogMode() (api.ExchangeLogMode, error) {
	out, err := c.api.GetExchange(true)
	if err != nil {
		return api.ExchangeLogMode{}, err
	}
	if out.LogMode == nil {
		return api.ExchangeLogMode{}, nil
	}
	return *out.LogMode, nil
}

// ListPhysicalDisks is the disks a machine backup can target (GET /disks),
// enumerated by the service. The console no longer opens \\.\PhysicalDriveN.
func (c *Console) ListPhysicalDisks() ([]api.PhysicalDisk, error) {
	return c.api.GetDisks()
}

// ---- the control server and the lock ---------------------------------------

// IsReadOnly reports whether this console is a status panel
// (/controlplane/status "gui_read_only").
//
// PRESENTATION ONLY. The service refuses every mutating call while locked
// (gui/api/readonly.go); this decides whether the controls are drawn at all.
// A service that does not answer is not a locked one.
func (c *Console) IsReadOnly() bool {
	st, err := c.api.GetControlPlaneStatus()
	if err != nil {
		return false
	}
	locked, _ := st["gui_read_only"].(bool)
	return locked
}

// GetControlServerStatus is the control-server card (GET /controlplane/status),
// with the storage identity, which also sets the tray's icon.
func (c *Console) GetControlServerStatus() (map[string]interface{}, error) {
	st, err := c.api.GetControlPlaneStatus()
	if err != nil {
		return nil, err
	}
	if storage, ok := st["storage_identity"].(map[string]interface{}); ok {
		c.updateStorageTray(storage)
	}
	return st, nil
}

// SaveControlServerConfig sends control-server settings to the service. An
// empty enroll token keeps the stored one; a changed URL forgets the old
// identity (the service's SaveControlPlaneFromMap).
func (c *Console) SaveControlServerConfig(serverURL, enrollToken, certFP string) error {
	svc, err := c.service()
	if err != nil {
		return err
	}
	return svc.SaveControlPlane(map[string]interface{}{
		"control_server_url":   serverURL,
		"control_enroll_token": enrollToken,
		"control_cert_fp":      certFP,
	})
}

// GetStorageIdentityStatus is the storage identity (GET /storage/identity).
func (c *Console) GetStorageIdentityStatus() (map[string]interface{}, error) {
	st, err := c.api.GetStorageIdentity()
	if err != nil {
		return nil, err
	}
	c.updateStorageTray(st)
	return st, nil
}

// ApproveStorageIdentity approves the observed disks on a standalone machine
// (POST /storage/identity); a joined machine approves through its dashboard.
func (c *Console) ApproveStorageIdentity(input map[string]interface{}) error {
	svc, err := c.service()
	if err != nil {
		return err
	}
	return svc.ApproveStorageIdentity(input)
}

// ---- runs ------------------------------------------------------------------

// GetConnections backs the panel's connection tiles. An unreachable service is
// an empty picture rather than an error: the caller polls, and the panel has
// its own place to say the service is not answering.
func (c *Console) GetConnections() (*api.ConnectionsResponse, error) {
	resp, err := c.api.GetConnections()
	if err != nil {
		return &api.ConnectionsResponse{PBS: []api.PBSConnection{}, ServerTime: time.Now()}, nil
	}
	return resp, nil
}

// GetActiveRuns is every backup in flight on this machine, whatever started
// it. The whole response is returned: it carries the SERVICE's clock, which a
// live run's elapsed time is measured against. Empty when the service does not
// answer, for the same reason as GetConnections.
func (c *Console) GetActiveRuns() (*api.RunsResponse, error) {
	resp, err := c.api.GetActiveRuns()
	if err != nil {
		return &api.RunsResponse{Runs: []api.Run{}, ServerTime: time.Now()}, nil
	}
	return resp, nil
}

// GetRecentRuns is the runs of the last `days` days, live ones included: the
// status panel's seven days, and the Backup tab's history (which replaced
// job_history.json).
func (c *Console) GetRecentRuns(days int) (*api.RunsResponse, error) {
	resp, err := c.api.GetRecentRuns(days)
	if err != nil {
		return &api.RunsResponse{Runs: []api.Run{}, ServerTime: time.Now()}, nil
	}
	return resp, nil
}

// StartBackup asks the service to run a backup. The console has no engine
// and no fallback (docs/V4-PIPELINE.md §3.1).
func (c *Console) StartBackup(backupType string, backupDirs []string, diskTargets []string, excludeList []string, backupID string, useVSS bool, compression string) error {
	writeInfoLog(fmt.Sprintf("StartBackup() called - VSS: %v, compression: %s", useVSS, compression))
	svc, err := c.service()
	if err != nil {
		return err
	}
	resp, err := svc.StartBackup(&api.BackupRequest{
		BackupType:  backupType,
		BackupID:    backupID,
		BackupDirs:  backupDirs,
		DiskTargets: diskTargets,
		ExcludeList: excludeList,
		UseVSS:      useVSS,
		Compression: compression,
	})
	if err != nil {
		writeErrorLog(fmt.Sprintf("[Console] backup request failed: %v", err))
		// The service's refusal is its own sentence. Only a request that
		// never got an answer is a communication failure (the client
		// already says NB-2007 for those).
		return err
	}
	// No poller here: the front end polls /runs/active for every run,
	// whatever started it.
	writeInfoLog(fmt.Sprintf("[Console] backup started: %s (run %s)", resp.Message, resp.JobID))
	return nil
}

// StopBackup asks the service to cancel the running backup.
func (c *Console) StopBackup() error {
	svc, err := c.service()
	if err != nil {
		return err
	}
	return svc.CancelBackup()
}

// ---- local scheduled jobs --------------------------------------------------

// GetScheduledJobs is this machine's local jobs (GET /jobs).
func (c *Console) GetScheduledJobs() ([]api.ScheduledJob, error) {
	return c.api.GetScheduledJobs()
}

// SaveScheduledJob creates a local job. The service enables it and computes its
// next run. Refused while the console is locked: until this binding asked the
// service, it wrote scheduled_jobs.json itself and the lock never saw it
// (docs/V4-GUI-AUDIT.md finding 3).
func (c *Console) SaveScheduledJob(job api.ScheduledJob) error {
	svc, err := c.service()
	if err != nil {
		return err
	}
	return svc.CreateJob(job)
}

// UpdateScheduledJob changes a local job (the service keeps its enabled state).
func (c *Console) UpdateScheduledJob(job api.ScheduledJob) error {
	svc, err := c.service()
	if err != nil {
		return err
	}
	return svc.UpdateJob(job)
}

// DeleteScheduledJob deletes a local job.
func (c *Console) DeleteScheduledJob(jobID string) error {
	svc, err := c.service()
	if err != nil {
		return err
	}
	return svc.DeleteJob(jobID)
}

// ---- PBS servers (standalone) ----------------------------------------------

// ListPBSServers is the configured servers, without their tokens (GET
// /pbs/servers). Empty in data-path proxy mode, where the machine has none.
func (c *Console) ListPBSServers() ([]api.PBSServerInfo, error) {
	out, err := c.api.ListPBSServers()
	if err != nil {
		return nil, err
	}
	return out.Servers, nil
}

// GetDefaultPBSID is the default server's id (GET /pbs/servers).
func (c *Console) GetDefaultPBSID() (string, error) {
	out, err := c.api.ListPBSServers()
	if err != nil {
		return "", err
	}
	return out.DefaultID, nil
}

// AddPBSServer and UpdatePBSServer save a server in the service (POST
// /pbs/save). An empty secret keeps the stored one; the service validates.
func (c *Console) AddPBSServer(server api.PBSServerSave) error {
	return c.savePBSServer(server)
}

// UpdatePBSServer saves a changed server; see AddPBSServer.
func (c *Console) UpdatePBSServer(server api.PBSServerSave) error {
	return c.savePBSServer(server)
}

func (c *Console) savePBSServer(server api.PBSServerSave) error {
	svc, err := c.service()
	if err != nil {
		return err
	}
	return svc.SavePBSServer(server)
}

// DeletePBSServer removes a server.
func (c *Console) DeletePBSServer(id string) error {
	svc, err := c.service()
	if err != nil {
		return err
	}
	return svc.DeletePBSServer(id)
}

// SetDefaultPBSServer sets the default server.
func (c *Console) SetDefaultPBSServer(id string) error {
	svc, err := c.service()
	if err != nil {
		return err
	}
	return svc.SetDefaultPBS(id)
}

// TestPBSConnection runs a server's connection test in the service, with the
// stored token, which the console never holds (POST /pbs/test).
//
// A certificate the console may offer to pin comes back as NB-1011, which is
// what the setup screen looks for; it used to recognise the case by matching
// words in the TLS error.
func (c *Console) TestPBSConnection(id string) error {
	svc, err := c.service()
	if err != nil {
		return err
	}
	resp, err := svc.TestPBSServer(id)
	if err != nil && resp != nil && resp.CertUntrusted {
		return fmt.Errorf("%s :: %s", errCertUntrusted, resp.Error)
	}
	return err
}

// GetServerFingerprint is the certificate fingerprint of the server at
// baseURL, read by the service in a TLS handshake that sends no token (POST
// /pbs/discover).
func (c *Console) GetServerFingerprint(baseURL string) (string, error) {
	svc, err := c.service()
	if err != nil {
		return "", err
	}
	return svc.DiscoverPBSFingerprint(baseURL)
}

// PinPBSServerFingerprint pins a discovered fingerprint on a server; the
// service validates it.
func (c *Console) PinPBSServerFingerprint(id, fingerprint string) error {
	svc, err := c.service()
	if err != nil {
		return err
	}
	return svc.PinFingerprint(id, fingerprint)
}
