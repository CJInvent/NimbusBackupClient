# NimbusControl integration (control-plane branch)

Implements the NimbusControl agent contract (server repo: `docs/AGENT-API.md`,
v1). Standalone installs are untouched: everything is a no-op while
`control_server_url` is empty in config.json.

## What's here

**`controlplane/` (new module, stdlib-only, unit-tested):**
`Client` (enroll / check-in / run reports / command results; 2s→8s→30s
jittered backoff on 429/5xx; optional SHA-256 leaf pin), `Agent` loop
(server-driven interval, floor 30 s; panic-isolated command handlers;
policy fails CLOSED before first contact), `RunReporter` (forward-only
phase posting, terminal latch, log-tail clipping keeps the END).

**Glue (`gui/controlplane_glue.go`) + surgical edits:**
- `config.go`: `control_server_url`, `control_enroll_token` (wiped after
  use), `control_agent_id`, `control_secret` (sealed via the existing
  DEK/TPM `encryptSecret`), `control_cert_fp`.
- `App.StartControlPlane()`: enroll-once + loop start. **Call it from
  service startup after config load** (one line, site not wired — pick the
  spot in `main.go` service path).
- Inventory: every enabled scheduled job at 86 400 s (daily HH:MM model) —
  feeds server missed-backup expectations.
- Commands: `run_backup` dispatches `executeScheduledJob` (existing
  runningJobs de-dup = idempotence).
- `backup_inline.go`: `BackupOptions.OnPhase`; `backupDirectory` emits
  `"running"` **inside the VSS callback** (shadow copy confirmed) or at
  read-start for non-VSS; errors before VSS confirmation are wrapped with
  the `vssCreateFailedMarker` sentinel → reported as `vss_failed`.
- `scheduler.go`: `registerRunReporter(job.BackupID, job.Name, …)` before
  `StartBackup` so run reports carry the same name as inventory (required
  for the server to clear the missed-backup latch).
- Both `BackupOptions` sites call `attachControlPlaneHooks(&opts)` — wraps
  `OnResult` (terminal classification: success/warning/failed/vss_failed
  + PBS triple from `BackupStatus.BackupID/BackupTime`) without disturbing
  existing consumers.
- `restore_inline.go`: `RestoreSnapshotInline` and
  `ListSnapshotContentsInline` gate on `ControlPolicy().FileRestore`
  (server-resolved agent > org > global, default **off**; standalone = on).
  Every caller (GUI, local API, future CLI) inherits the gate.
- `controlplane/schedule.go`: `NextAligned(now, intervalSeconds,
  offsetSeconds)` — the ONE shared primitive for "wait until my next turn
  on a server-assigned, epoch-aligned schedule". Used by both `Agent.Run`'s
  own check-in cadence and `gui/controlplane_pbspoll.go`'s independent PBS
  poll. Epoch-aligned (not "interval seconds after I last ran") so a
  fleet-wide restart event never resynchronizes every agent onto an
  identical clock.
- `gui/controlplane_pbspoll.go`: independent, server-scheduled PBS
  connectivity polling, decoupled from the ~120 s check-in cadence.
  Default 1800 s (30 min) until the server assigns otherwise. Reuses
  `cpCheckPBSReachability`/`pbscommon.PBSClient.CheckConnectivity`
  unchanged — only *when* it runs changed, not what it does. Check-in's
  inventory now reports the cached result (`cachedPBSReachable()`) instead
  of performing a live PBS call on every cycle. Server assigns the
  interval/offset via `CheckinResponse.PBSPollIntervalSeconds/
  PBSPollOffsetSeconds` (`Agent.OnPBSPollSchedule` callback — same shape as
  the existing `OnPolicy` push). This is NOT the manual "Test" button
  (`App.TestPBSConnection` → `App.TestConnection`), which stays a separate,
  heavier, on-demand check for validating freshly-edited PBS settings.

## What's here (schedule fields, v1.1 of the check-in contract)

`CheckinResponse` gained three fields alongside the pre-existing
`checkin_seconds`:

- `checkin_offset_seconds` — this agent's slot in the check-in grid.
  Applied via `Agent.checkinOffset`; `Agent.Run`'s regular (non-first)
  ticks now wait on `NextAligned`, not a flat `time.After(interval)`.
- `pbs_poll_interval_seconds` / `pbs_poll_offset_seconds` — schedule for
  the independent PBS poller above.

All three are server-authoritative and delivered fresh on every check-in
(no separate endpoint) — reconnection, a changed interval, or a changed
fleet size all propagate within one check-in cycle with no client-side
reconfiguration. See NimbusControl `docs/AGENT-API.md` for the
server-side offset assignment (`Nimbus\Agents\PollSchedule`).

## Known gaps / verify on a real build

1. **Not compile-verified in CI sandbox**: `proxy.golang.org` egress is
   blocked there, so `gui/` couldn't be built (deps unfetchable). The new
   `controlplane` module compiles and its tests pass standalone; all `gui/`
   edits are parse-checked only. Run `go build ./...` + the test suite
   before merging.
2. `StartControlPlane()` call site (see above) — one line in service init.
3. `bytes_uploaded` is reported as 0 (chunk *counts* are tracked, not byte
   sums). Wire real uploaded-byte accounting later if billing wants it.
4. GUI settings page for the control-server fields + surfacing
   `ErrRestoreDisabled` nicely; the enforcement itself already works.
5. Manual backups without a registered reporter appear as `manual:<id>` —
   two simultaneous no-BackupID jobs could swap labels (commented in glue).
