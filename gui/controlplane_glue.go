package main

// controlplane_glue.go — wires the NimbusControl agent (controlplane pkg)
// into the client. One file owns ALL glue so the integration surface is
// auditable in one read:
//
//   * enrollment + secret persistence (via the existing DEK/TPM store)
//   * the check-in loop (inventory from scheduled jobs, command dispatch)
//   * hierarchical policy application (fail CLOSED before first check-in)
//   * per-run phase/result reporting hooks for BackupOptions
//
// Everything is a no-op when Config.ControlServerURL is empty — standalone
// installs keep working exactly as before.

import (
	"controlplane"
	"errors"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	cpMu     sync.Mutex // guards cpAgent/cpClient/cpStop lifecycle
	cpStop   chan struct{}
	cpAgent  *controlplane.Agent
	cpClient *controlplane.Client

	// Reporter hand-off between the code that KNOWS the job name
	// (executeScheduledJob) and the code that builds BackupOptions.
	// Keyed by BackupID; jobs without a fixed BackupID fall back to the
	// single pending slot — see the honest caveat in takeRunReporter.
	cpReporters   = map[string]*controlplane.RunReporter{}
	cpPendingRep  *controlplane.RunReporter
	cpReportersMu sync.Mutex
)

// StartControlPlane enrolls (first run) and starts the check-in loop.
// Call once from service startup after config load. Returns silently when
// no control server is configured.
func (a *App) StartControlPlane() {
	cpMu.Lock()
	defer cpMu.Unlock()
	cfg := a.config
	if cfg.ControlServerURL == "" {
		writeDebugLog("[controlplane] not configured; running standalone")
		return
	}

	cpClient = &controlplane.Client{
		BaseURL:         cfg.ControlServerURL,
		CertFingerprint: cfg.ControlCertFP,
		AgentID:         cfg.ControlAgentID,
		Secret:          decryptSecret(cfg.ControlSecret),
		UserAgent:       "NimbusBackupClient/" + appVersion,
	}

	// ---- one-time enrollment ------------------------------------------
	if cpClient.AgentID == 0 {
		if cfg.ControlEnrollToken == "" {
			writeDebugLog("[controlplane] no identity and no enrollment token; staying standalone")
			return
		}
		hostname, _ := os.Hostname()
		resp, err := cpClient.Enroll(controlplane.EnrollRequest{
			Token:        cfg.ControlEnrollToken,
			Hostname:     hostname,
			OSInfo:       runtime.GOOS + "/" + runtime.GOARCH,
			AgentVersion: appVersion,
		})
		if err != nil {
			writeDebugLog(fmt.Sprintf("[controlplane] enrollment failed (will retry next start): %v", err))
			return
		}
		cpClient.AgentID, cpClient.Secret = resp.AgentID, resp.Secret
		// Persist: secret sealed by the DEK/TPM store; one-time token wiped.
		a.config.ControlAgentID = resp.AgentID
		a.config.ControlSecret = encryptSecret(resp.Secret)
		a.config.ControlEnrollToken = ""
		if err := a.config.Save(); err != nil {
			writeDebugLog(fmt.Sprintf("[controlplane] WARNING: enrolled but config save failed: %v", err))
		}
		writeDebugLog(fmt.Sprintf("[controlplane] enrolled as agent %d", resp.AgentID))
	}

	cpAgent = &controlplane.Agent{
		Client:         cpClient,
		AgentVersion:   appVersion,
		BuildInventory: a.cpBuildInventory,
		HandleCommand:  a.cpHandleCommand,
		OnPolicy: func(p controlplane.Policy) {
			writeDebugLog(fmt.Sprintf("[controlplane] policy applied: file_restore=%v", p.FileRestore))
		},
	}
	cpStop = make(chan struct{})
	go cpAgent.Run(cpStop)
}

// StopControlPlane halts the check-in loop (config change / shutdown).
func (a *App) StopControlPlane() {
	cpMu.Lock()
	defer cpMu.Unlock()
	if cpStop != nil {
		close(cpStop)
		cpStop = nil
	}
	cpAgent, cpClient = nil, nil
}

// RestartControlPlane applies a changed control-server config live.
func (a *App) RestartControlPlane() {
	a.StopControlPlane()
	a.StartControlPlane()
	// Prompt an immediate first contact so the GUI status card reflects the
	// new server within seconds instead of one full interval.
	cpMu.Lock()
	ag := cpAgent
	cpMu.Unlock()
	if ag != nil {
		go ag.CheckinNow()
	}
}

// ControlPlaneStatusMap is the display snapshot for the GUI/local API.
// Never includes the secret or enrollment token.
func (a *App) ControlPlaneStatusMap() map[string]interface{} {
	cfg := a.config
	out := map[string]interface{}{
		"configured": cfg != nil && cfg.ControlServerURL != "",
		"server_host": func() string {
			if cfg == nil || cfg.ControlServerURL == "" {
				return ""
			}
			if u, err := url.Parse(cfg.ControlServerURL); err == nil && u.Host != "" {
				return u.Host
			}
			return cfg.ControlServerURL
		}(),
		"enrolled":             cfg != nil && cfg.ControlAgentID > 0,
		"agent_id":             int64(0),
		"connected":            false,
		"pending_enroll_token": cfg != nil && cfg.ControlEnrollToken != "",
	}
	if cfg != nil {
		out["agent_id"] = cfg.ControlAgentID
	}
	cpMu.Lock()
	ag := cpAgent
	cpMu.Unlock()
	if ag != nil {
		st := ag.Status()
		out["connected"] = st.Connected
		out["last_error"] = st.LastError
		out["checkin_seconds"] = st.CheckinPeriod
		out["policy_file_restore"] = st.Policy.FileRestore
		if !st.LastSuccess.IsZero() {
			out["last_checkin"] = st.LastSuccess.UTC().Format(time.RFC3339)
		}
		if !st.LastAttempt.IsZero() {
			out["last_attempt"] = st.LastAttempt.UTC().Format(time.RFC3339)
		}
	}
	return out
}

// SaveControlPlaneFromMap applies control-server settings (service-side
// write path, mirroring SaveConfigFromMap conventions: empty strings keep
// stored values; url cleared = disable + forget identity).
func (a *App) SaveControlPlaneFromMap(m map[string]interface{}) error {
	str := func(k string) (string, bool) {
		v, ok := m[k]
		if !ok {
			return "", false
		}
		sv, _ := v.(string)
		return strings.TrimSpace(sv), true
	}
	if u, ok := str("control_server_url"); ok {
		if u != "" {
			parsed, err := url.Parse(u)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
				return errors.New("control server url must be https://host[:port]")
			}
		}
		if u == "" || u != a.config.ControlServerURL {
			// New/removed server: previous identity is meaningless there.
			a.config.ControlAgentID = 0
			a.config.ControlSecret = ""
		}
		a.config.ControlServerURL = u
	}
	if tok, ok := str("control_enroll_token"); ok && tok != "" {
		a.config.ControlEnrollToken = tok
	}
	if fp, ok := str("control_cert_fp"); ok {
		a.config.ControlCertFP = fp
	}
	if err := a.config.Save(); err != nil {
		return err
	}
	a.RestartControlPlane()
	return nil
}

// cpBuildInventory reports every scheduled job so the server can maintain
// missed-backup expectations. Scheduling is daily-at-HH:MM, so the
// expectation interval is 24 h per enabled job.
func (a *App) cpBuildInventory() controlplane.Inventory {
	inv := controlplane.Inventory{Jobs: []controlplane.InventoryJob{}}
	jobs, err := a.GetScheduledJobs()
	if err != nil {
		return inv
	}
	for _, j := range jobs {
		if !j.Enabled {
			continue
		}
		inv.Jobs = append(inv.Jobs, controlplane.InventoryJob{
			Name:            j.Name,
			IntervalSeconds: 86400,
		})
	}
	return inv
}

// cpHandleCommand executes server commands. Idempotence: run_backup rides
// on the runningJobs de-dup in executeScheduledJob, so a re-delivered
// command while the job runs is a clean no-op.
func (a *App) cpHandleCommand(cmd controlplane.Command) controlplane.CommandResult {
	// Portal-delegated image browsing (image_partitions / image_scan /
	// image_dir / image_extract) — see controlplane_browse.go.
	if res, handled := a.cpHandleBrowseCommand(cmd); handled {
		return res
	}
	switch cmd.Command {
	case "run_backup":
		name, _ := cmd.Payload["job"].(string)
		jobs, err := a.GetScheduledJobs()
		if err != nil {
			return controlplane.CommandResult{OK: false, Result: map[string]interface{}{"error": err.Error()}}
		}
		for _, j := range jobs {
			if j.Name == name || j.ID == name {
				job := j
				go a.executeScheduledJob(job) // long work off the check-in loop
				return controlplane.CommandResult{OK: true, Result: map[string]interface{}{"note": "backup dispatched"}}
			}
		}
		return controlplane.CommandResult{OK: false, Result: map[string]interface{}{"error": "unknown job: " + name}}
	default:
		return controlplane.CommandResult{OK: false, Result: map[string]interface{}{"error": "unsupported command: " + cmd.Command}}
	}
}

// ControlPolicy is THE gate the GUI and local API consult. Fail-closed
// semantics are inherited from Agent.CurrentPolicy (zero value = all off);
// a standalone install (no control server configured) is intentionally
// ungoverned and gets everything enabled locally.
func ControlPolicy() controlplane.Policy {
	if cpAgent == nil {
		return controlplane.Policy{FileRestore: true} // standalone: no MSP policy applies
	}
	return cpAgent.CurrentPolicy()
}

// ErrRestoreDisabled is surfaced verbatim in the GUI.
var ErrRestoreDisabled = errors.New("file restore is disabled on this machine by your administrator")

// ---------------------------------------------------------------------------
// Run reporting hand-off
// ---------------------------------------------------------------------------

// registerRunReporter is called by executeScheduledJob (which knows the
// job's display name) BEFORE StartBackup. backupID may be "".
func registerRunReporter(backupID, jobName, backupType string) {
	if cpClient == nil {
		return
	}
	rep := cpClient.NewRun(jobName, backupType)
	rep.Preparing()
	cpReportersMu.Lock()
	defer cpReportersMu.Unlock()
	if backupID != "" {
		cpReporters[backupID] = rep
		return
	}
	// CAVEAT (documented, accepted): jobs without a fixed BackupID share
	// one pending slot; two such jobs starting in the same instant could
	// swap labels. Scheduled jobs in practice carry a BackupID — this
	// fallback exists for ad-hoc/manual runs.
	cpPendingRep = rep
}

// takeRunReporter is called from attachControlPlaneHooks at BackupOptions
// construction. A manual backup with no registered reporter gets an ad-hoc
// one so manual runs still show up in the portal.
func takeRunReporter(backupID, backupType string) *controlplane.RunReporter {
	if cpClient == nil {
		return nil
	}
	cpReportersMu.Lock()
	defer cpReportersMu.Unlock()
	if rep, ok := cpReporters[backupID]; ok && backupID != "" {
		delete(cpReporters, backupID)
		return rep
	}
	if cpPendingRep != nil {
		rep := cpPendingRep
		cpPendingRep = nil
		return rep
	}
	rep := cpClient.NewRun("manual:"+backupID, backupType)
	rep.Preparing()
	return rep
}

// attachControlPlaneHooks decorates a fully-built BackupOptions with phase
// and result reporting. ONE call at each construction site:
//
//	attachControlPlaneHooks(&opts)
//
// It wraps (not replaces) OnResult and installs OnPhase, so every existing
// consumer keeps firing untouched.
func attachControlPlaneHooks(opts *BackupOptions) {
	kind := "directory"
	if opts.BackupType == "vm" {
		kind = "machine"
	}
	rep := takeRunReporter(opts.BackupID, kind)
	if rep == nil {
		return // control plane not configured
	}
	rep.SetPBSTarget(opts.BaseURL, opts.Datastore, opts.Namespace)

	var runningOnce sync.Once
	prevPhase := opts.OnPhase
	opts.OnPhase = func(phase string) {
		if phase == "running" {
			// First confirmation only: for VSS jobs this fires when the
			// shadow copy EXISTS (the product definition of "backing up");
			// multi-directory jobs confirm once per dir — report once.
			runningOnce.Do(rep.Running)
		}
		if prevPhase != nil {
			prevPhase(phase)
		}
	}

	prevResult := opts.OnResult
	opts.OnResult = func(s *BackupStatus) {
		if s != nil {
			tail := s.Message
			switch {
			case s.Outcome == OutcomeFailed && errorLooksVSS(s.Message):
				rep.VSSFailed(firstLine(s.Message))
			case s.Outcome == OutcomeFailed:
				rep.Failed(firstLine(s.Message), tail)
			case len(s.SkippedReadError) > 0 || len(s.Directories) > 0 && anyDirFailed(s.Directories):
				rep.Warning(opts.BackupType, s.BackupID, s.BackupTime,
					int64(s.TotalBytes), 0, firstLine(s.Message), tail)
			default:
				rep.Success(opts.BackupType, s.BackupID, s.BackupTime,
					int64(s.TotalBytes), 0, tail)
			}
		}
		if prevResult != nil {
			prevResult(s)
		}
	}
}

func anyDirFailed(dirs []DirResult) bool {
	for _, d := range dirs {
		if !d.OK {
			return true
		}
	}
	return false
}

// errorLooksVSS classifies a failure as VSS-side: the sentinel from
// backupDirectory wraps every error that occurred before the shadow copy
// was confirmed.
func errorLooksVSS(msg string) bool {
	return strings.Contains(msg, vssCreateFailedMarker)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
