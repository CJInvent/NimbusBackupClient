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

	// Restore the last known managed job set before the loop starts, so a
	// portal run_backup naming a managed job works during the window before
	// the first check-in completes — and so an agent that cannot reach the
	// server at all still knows what it was last told to run.
	//
	// Loaded HERE, by whoever hosts the agent, rather than at service
	// start: the GUI build can host one too (SaveControlPlaneFromMap ->
	// RestartControlPlane), and the cache is only meaningful where the
	// agent that fills it lives.
	loadManagedJobs()

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
		OnManagedJobs:  applyManagedJobsFromCheckin,
		OnPolicy: func(p controlplane.Policy) {
			writeDebugLog(fmt.Sprintf("[controlplane] policy applied: file_restore=%v", p.FileRestore))
		},
		OnPBSPollSchedule: func(intervalSeconds, offsetSeconds int) {
			updatePBSPollSchedule(intervalSeconds, offsetSeconds)
			writeDebugLog(fmt.Sprintf("[controlplane] PBS poll schedule: interval=%ds offset=%ds", intervalSeconds, offsetSeconds))
		},
	}
	cpStop = make(chan struct{})
	go cpAgent.Run(cpStop)
	a.startPBSPoller()
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
	a.stopPBSPoller()
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
		// policy_known says an agent exists in THIS process and has a
		// policy to report. The GUI gates on it: without it, an absent
		// policy is indistinguishable from a permissive one, which is how
		// the front end came to ignore org file-restore policy entirely.
		out["policy_known"] = true
		out["connected"] = st.Connected
		out["last_error"] = st.LastError
		out["checkin_seconds"] = st.CheckinPeriod
		out["policy_file_restore"] = st.Policy.FileRestore
		out["restrict_unmanaged_backups"] = st.Policy.RestrictUnmanagedBackups
		out["gui_read_only"] = st.Policy.GUIReadOnly
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
	if checkedAt := cachedPBSCheckedAt(); !checkedAt.IsZero() {
		out["pbs_last_checked_at"] = checkedAt.UTC().Format(time.RFC3339)
	}
	if reachable := cachedPBSReachable(); reachable != nil {
		out["pbs_reachable"] = *reachable
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
		// Cached result from the independent PBS poller (controlplane_pbspoll.go),
		// NOT a live call — see that file's top comment for why check-in
		// stopped triggering a fresh PBS network round-trip every ~120s.
		PBSReachable: cachedPBSReachable(),
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

// cpCheckPBSReachability answers the server dashboard's PBS status
// column/indicator: does this agent's own configured PBS server currently
// answer for it. nil (not Ptr(false)) when there is nothing meaningful to
// check yet -- a fresh install with no PBS configured at all -- so the
// server correctly shows "unknown" rather than a false "unreachable" for a
// machine that was never told where to back up in the first place. Uses
// the SAME Validate() bar the backup path itself already gates on
// (BaseURL/AuthID/Secret/... all present), not a looser one invented just
// for this check.
//
// Synchronous and bounded (CheckConnectivity's own 5s timeout): check-in
// already makes sequential blocking calls, and 5s is a small, bounded
// addition to a cycle that happens every ~120s -- not worth the added
// complexity of an async design for a result that check-in needs to
// include in this SAME payload anyway.
func cpCheckPBSReachability(cfg *Config) *bool {
	pbsCfg := cfg.EffectivePBS()
	if err := pbsCfg.Validate(); err != nil {
		return nil
	}
	client := &pbscommon.PBSClient{
		BaseURL: pbsCfg.BaseURL, AuthID: pbsCfg.AuthID, Secret: pbsCfg.Secret,
		Datastore: pbsCfg.Datastore, Namespace: pbsCfg.Namespace,
		CertFingerPrint: pbsCfg.CertFingerprint,
	}
	reachable, err := client.CheckConnectivity()
	if err != nil {
		// A problem with the check itself (couldn't build the request) --
		// not a real connectivity answer either way. Leave it unknown
		// rather than report a possibly-wrong true/false.
		writeDebugLog(fmt.Sprintf("[controlplane] PBS connectivity check failed to run: %v", err))
		return nil
	}
	return &reachable
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
	// Portal-requested run log fetch — see controlplane_runlog.go.
	if res, handled := a.cpHandleRunLogCommand(cmd); handled {
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

		// Managed jobs are searched FIRST and by the server's own name.
		// The server is asking for a job it defined; finding a local job
		// that happens to share the name and running that instead would be
		// the wrong backup, taken with the wrong paths, reported against
		// the org's job.
		for _, m := range currentManagedJobs() {
			sj := managedToScheduledJob(m)
			if m.Name == name || sj.ID == name || fmt.Sprintf("%d", m.ID) == name {
				go a.executeScheduledJob(sj, requestID)
				return controlplane.CommandResult{OK: true, Result: map[string]interface{}{"note": "managed backup dispatched"}}
			}
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

// applyManagedJobsFromCheckin is the OnManagedJobs hook. Named rather than a
// closure so the check-in loop's callback list reads as a list of behaviours.
func applyManagedJobsFromCheckin(jobs []controlplane.ManagedJob) {
	applyManagedJobs(jobs)
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

// UnmanagedBackupsPermitted is THE gate for a backup this machine authored
// itself — a local scheduler job, or one started at the console. Work sent by
// the control plane is managed by definition and never passes through here.
//
// An install with no control server is ungoverned by design: the project ships
// and is supported without NimbusControl, so an unmanaged install is a
// product, not a bypass.
func UnmanagedBackupsPermitted() bool {
	if cpAgent == nil {
		return true
	}
	allowed, viaOverride := cpAgent.UnmanagedBackupsEligible(unmanagedBackupsOverrideRequested(), 0)
	if viaOverride {
		noteUnmanagedOverrideUse()
	}
	return allowed
}

var unmanagedLogMu sync.Mutex
var unmanagedLoggedAt time.Time

// noteUnmanagedOverrideUse records that the local override took effect.
// Throttled, but never silent: overriding an administrator's policy has to
// leave a trail someone can find afterwards.
func noteUnmanagedOverrideUse() {
	unmanagedLogMu.Lock()
	defer unmanagedLogMu.Unlock()
	if time.Since(unmanagedLoggedAt) < 5*time.Minute {
		return
	}
	unmanagedLoggedAt = time.Now()
	last := cpAgent.Status().LastSuccess
	when := "never"
	if !last.IsZero() {
		when = last.Format(time.RFC3339)
	}
	msg := fmt.Sprintf("[controlplane] OVERRIDE: local AllowUnmanagedBackups flag is permitting a "+
		"locally-scheduled backup because the control server is unreachable (last successful "+
		"check-in: %s). Org policy restricts this machine to managed jobs; clear the flag once "+
		"the server is reachable again.", when)
	writeDebugLog(msg)
	writeBackupLog(msg)
}

// ErrUnmanagedBackupsDisabled is surfaced verbatim in the GUI and returned by
// the local API.
var ErrUnmanagedBackupsDisabled = errors.New(
	"backups started on this machine are disabled by your administrator; " +
		"scheduled work sent by the management server still runs")

// ErrRestoreDisabled is surfaced verbatim in the GUI.
var ErrRestoreDisabled = errors.New("file restore is disabled on this machine by your administrator")

// ---------------------------------------------------------------------------
// Run reporting hand-off
