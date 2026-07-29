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
	"pbscommon"
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
		// Distinguish "the org grants this" from "the org denies it but the
		// control server is unreachable and an admin set the local override".
		// The UI must never present the second as if it were the first.
		if bg := BreakGlassInEffect(); bg {
			out["break_glass_file_restore"] = true
			out["policy_file_restore"] = true
		}
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
	// Report an active break-glass override on every check-in. This is the
	// only way the MSP ever learns it happened: the override is admissible
	// precisely BECAUSE the server was unreachable, so the agent cannot tell
	// anyone at the time. The first reachable check-in is the first chance.
	inv := controlplane.Inventory{
		Jobs:                  []controlplane.InventoryJob{},
		BreakGlassFileRestore: BreakGlassInEffect(),
	}
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
		requestID, _ := cmd.Payload["request_id"].(string)
		jobs, err := a.GetScheduledJobs()
		if err != nil {
			return controlplane.CommandResult{OK: false, Result: map[string]interface{}{"error": err.Error()}}
		}
		for _, j := range jobs {
			if j.Name == name || j.ID == name {
				job := j
				go a.executeScheduledJob(job, requestID) // long work off the check-in loop
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
//
// A managed agent may additionally honour the local break-glass flag, but
// ONLY while the control plane is genuinely unreachable — see
// controlplane/breakglass.go. A reachable server that says no still means no.
func ControlPolicy() controlplane.Policy {
	if cpAgent == nil {
		return controlplane.Policy{FileRestore: true} // standalone: no MSP policy applies
	}
	p := cpAgent.CurrentPolicy()
	if !p.FileRestore && cpAgent.BreakGlassEligible(emergencyFileRestoreRequested(), 0) {
		noteBreakGlassUse()
		p.FileRestore = true
	}
	return p
}

var breakGlassLogMu sync.Mutex
var breakGlassLoggedAt time.Time

// noteBreakGlassUse records that the emergency override took effect. Throttled
// because ControlPolicy is consulted on every browse and restore call, but
// never silent: an override of an administrator's policy has to leave a trail
// someone can find afterwards.
func noteBreakGlassUse() {
	breakGlassLogMu.Lock()
	defer breakGlassLogMu.Unlock()
	if time.Since(breakGlassLoggedAt) < 5*time.Minute {
		return
	}
	breakGlassLoggedAt = time.Now()
	last := cpAgent.Status().LastSuccess
	when := "never"
	if !last.IsZero() {
		when = last.Format(time.RFC3339)
	}
	msg := fmt.Sprintf("[controlplane] BREAK-GLASS: local EmergencyFileRestore flag is enabling file restore "+
		"because the control server is unreachable (last successful check-in: %s). "+
		"Org policy has file restore DISABLED; clear the flag once the server is reachable again.", when)
	writeDebugLog(msg)
	writeBackupLog(msg)
}

// BreakGlassInEffect reports whether the local override is currently
// substituting for an unreachable control server, so the UI can say so rather
// than silently showing a capability the org disabled.
func BreakGlassInEffect() bool {
	if cpAgent == nil {
		return false
	}
	if cpAgent.CurrentPolicy().FileRestore {
		return false // granted by policy, not by the override
	}
	return cpAgent.BreakGlassEligible(emergencyFileRestoreRequested(), 0)
}

// ErrRestoreDisabled is surfaced verbatim in the GUI.
var ErrRestoreDisabled = errors.New("file restore is disabled on this machine by your administrator")

// ---------------------------------------------------------------------------
// Run reporting hand-off
// ---------------------------------------------------------------------------

// registerRunReporter is called by executeScheduledJob (which knows the
// job's display name) BEFORE StartBackup. backupID may be "".
func registerRunReporter(backupID, jobName, backupType, requestID string) {
	if cpClient == nil {
		return
	}
	rep := cpClient.NewRun(jobName, backupType)
	if requestID != "" {
		rep.SetRequestID(requestID) // before Preparing(), so even the FIRST report carries it
	}
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
// and result reporting, and returns a FINALIZER the caller must invoke with
// the engine's error return:
//
//	finish := attachControlPlaneHooks(&opts)
//	err := RunMachineBackup(opts)
//	finish(err)
//
// It wraps (not replaces) OnResult and installs OnPhase, so every existing
// consumer keeps firing untouched.
//
// # WHY THE FINALIZER EXISTS
//
// The hooks below hang off opts.OnResult, which the DIRECTORY engine
// (backup_inline.go) emits at every exit. The MACHINE engine
// (machine_backup_windows.go, RunMachineBackup) never calls OnResult at all
// -- it reports through OnComplete and its error return instead. So a
// machine backup used to post "preparing" at registration and then NOTHING,
// ever: no success, no failure, no VSS abort. The portal showed every
// image backup stuck in "preparing" forever, "Last successful run: Never
// run", and a real VSS_E_UNEXPECTED failure was completely invisible
// server-side even though the client logged it plainly. On a fleet whose
// default_backup_mode is an image mode -- i.e. every job is type=machine --
// that meant the control plane never learned the outcome of ANY backup.
//
// Rather than thread OnResult through every exit path of a large
// Windows-only engine, the finalizer is a backstop: if the engine returned
// without OnResult having fired, report the outcome from the error return.
// That makes it impossible for ANY engine, present or future, to leave a
// run dangling in a non-terminal state -- the failure mode here was silence,
// and silence is exactly what a monitoring product must never produce.
func attachControlPlaneHooks(opts *BackupOptions) func(error) {
	kind := "directory"
	if opts.BackupType == "vm" {
		kind = "machine"
	}
	rep := takeRunReporter(opts.BackupID, kind)
	if rep == nil {
		return func(error) {} // control plane not configured
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

	prevMilestone := opts.OnMilestone
	opts.OnMilestone = func(checkpoint, level, message string) {
		rep.Event(checkpoint, level, message)
		if prevMilestone != nil {
			prevMilestone(checkpoint, level, message)
		}
	}

	// Guards the finalizer: set by the OnResult path below, read after the
	// engine returns. Both happen on the engine's goroutine (OnResult is
	// called synchronously by the engine, the finalizer immediately after
	// it returns), but the mutex keeps that safe if an engine ever reports
	// its result from a worker goroutine.
	var (
		reportedMu sync.Mutex
		reported   bool
	)
	markReported := func() {
		reportedMu.Lock()
		reported = true
		reportedMu.Unlock()
	}

	prevResult := opts.OnResult
	opts.OnResult = func(s *BackupStatus) {
		if s != nil {
			markReported()
			tail := s.Message
			switch {
			case s.Outcome == OutcomeFailed && errorLooksVSS(s.Message):
				rep.VSSFailed(firstLine(s.Message))
			case s.Outcome == OutcomeFailed:
				rep.Failed(firstLine(s.Message), tail)
			case len(s.SkippedReadError) > 0 || len(s.Directories) > 0 && anyDirFailed(s.Directories):
				rep.Warning(opts.BackupType, s.BackupID, s.BackupTime,
					int64(s.TotalBytes), 0, firstLine(s.Message), tail)
				cpStampSnapshotNotes(opts, rep, s.BackupID, s.BackupTime)
			default:
				rep.Success(opts.BackupType, s.BackupID, s.BackupTime,
					int64(s.TotalBytes), 0, tail)
				cpStampSnapshotNotes(opts, rep, s.BackupID, s.BackupTime)
			}
		}
		if prevResult != nil {
			prevResult(s)
		}
	}

	return func(err error) {
		reportedMu.Lock()
		already := reported
		reportedMu.Unlock()
		if already {
			return // the engine reported its own outcome; nothing to add
		}
		if err != nil {
			msg := err.Error()
			if errorLooksVSS(msg) {
				rep.VSSFailed(firstLine(msg))
			} else {
				rep.Failed(firstLine(msg), msg)
			}
			return
		}
		// Succeeded, but this engine gave us no BackupStatus, so the PBS
		// snapshot coordinates and byte counts are not available here --
		// they are reported as zero/empty rather than guessed. Recording
		// the SUCCESS is still strictly better than leaving the run in
		// "preparing" forever, and closing that metadata gap is exactly
		// what end-to-end backup-job correlation is for.
		rep.Success(opts.BackupType, opts.BackupID, time.Now().Unix(), 0, 0, "")
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

// cpStampSnapshotNotes attaches this run's Backup Job ID to its PBS
// snapshot (pbscommon.PBSClient.SetSnapshotNotes), so the snapshot itself
// -- not just the server's own records -- can be traced back to the run
// that made it. opts already carries everything needed (BaseURL/AuthID/
// Secret/Datastore/Namespace/CertFingerprint): no new field threaded
// through BackupStatus, no second PBS connection reused from the backup
// itself -- this is a fresh, standalone HTTP call.
//
// ONLY called from the OnResult path (the directory engine, which always
// reports a real BackupStatus with the ACTUAL s.BackupID/s.BackupTime PBS
// recorded), never from the finalizer's blind-success fallback. That
// fallback (attachControlPlaneHooks's returned func, used today only when
// the machine engine reports no BackupStatus at all) has no trustworthy
// backup-time to target -- guessing one risks silently tagging nothing,
// or the wrong snapshot, rather than the one that was just created. This
// is a known, documented gap (see attachControlPlaneHooks's own comment
// on RunMachineBackup never emitting OnResult): machine-backup snapshots
// do not get a Job ID stamped until that engine is updated to report a
// real BackupStatus like the directory engine already does.
//
// Best-effort and async: called after the backup already succeeded, so a
// slow or unreachable PBS must never delay -- or appear to affect -- the
// run's own already-decided outcome.
func cpStampSnapshotNotes(opts *BackupOptions, rep *controlplane.RunReporter, backupID string, backupTime int64) {
	if opts.BaseURL == "" {
		return // defensive only: a successful backup implies PBS was configured
	}
	pbs := &pbscommon.PBSClient{
		BaseURL: opts.BaseURL, AuthID: opts.AuthID, Secret: opts.Secret,
		Datastore: opts.Datastore, Namespace: opts.Namespace,
		CertFingerPrint: opts.CertFingerprint, // note the casing difference from BackupOptions' own field
	}
	runUUID := rep.RunUUID()
	go func() {
		note := "nimbus-job:" + runUUID
		if err := pbs.SetSnapshotNotes(opts.BackupType, backupID, backupTime, note); err != nil {
			writeDebugLog(fmt.Sprintf("[controlplane] failed to stamp PBS snapshot notes for run %s: %v", runUUID, err))
		}
	}()
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
