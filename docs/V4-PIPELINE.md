# v4 — One Backup Pipeline, in the Service

Design record. Decisions are CJ's unless marked as a recommendation.

Related: `docs/V4-STATUS.md`, NimbusControl `docs/V4-CLIENT-CONFIG.md` (§5,
GUI lockdown and the standalone-mode toggle), `docs/CONTROL-PLANE.md`.

CJ's statement of intent, 2026-08-05:

> All backup components should be in the service, not the GUI. The GUI should
> just be a lightweight web-based front end. There should only be one clean
> backup pipeline located in the service with no dead-end code.

---

## 1. Why this is a rewire and not a refactor

The service and the GUI are **two separate binaries built from the same
package with opposing build tags**, and each carries its own copy of the
backup pipeline:

| | GUI build (`!service`) | Service build (`service`) |
|---|---|---|
| `App.StartBackup` | `gui/main.go:767` | `gui/app_service_stubs.go:39` |
| executes via | `startBackupDirect` (`main.go:911`) | inline, same function |
| engines called | `RunMachineBackup` / `RunBackupInline` | `RunMachineBackup` / `RunBackupInline` |
| sets `opts.OnResult` | **yes** (Wails event only) | **no** |
| writes local job history | yes | yes, separately |

Two implementations of one operation, each ~150 lines of option assembly,
already diverging.

**Correction to an earlier draft of this document.** It claimed `OnResult` was
the divergence with teeth, on the grounds that it writes the per-snapshot
status sidecar (`nimbus-status.json.blob`). That is wrong: the sidecar is
uploaded by `RunBackupInline` itself (`backup_inline.go:1225`), and `OnResult`
in the GUI build only emits a `backup:result` Wails event. The claim was made
from the call site's name without following it to the writer, and it went into
a commit message before it was checked.

The real divergences, found when the two copies were merged in §3.1, are worse
and less interesting-sounding. The SERVICE build — the one on every managed
machine — was missing all of these, which the GUI copy had:

- `security.ValidateBackupID` on the id that becomes a PBS snapshot path
- `security.ValidatePath` on every directory handed to the engine
- `pbsCfg.Validate()`, so a half-configured target fails with a reason
- the empty-target checks (`errDirRequired` / `errDiskRequired`)
- the `isAdmin()` check before raw `\\.\PhysicalDriveN` access

And one the GUI copy was missing: it read `DisableSplit` and `SplitSizeBytes`
from `a.config` rather than from the resolved `pbsCfg`, so a multi-PBS-only
configuration — precisely the one audit M-01/M-04 was raised about — ignored
the per-server split settings.

None of that was a decision. It is what two copies of one function look like
after a year of fixes landing in whichever copy the bug was reported against.

That is the shape of the whole problem: the *older* path — direct in-process
execution, inherited from the upstream `proxmoxbackupclient_go` standalone
tool — is the one that got maintained, and the service was fitted around it
rather than replacing it.

---

## 2. The reported bug, root-caused

CJ, on the latest full release: *the client could not detect when a scheduled
backup was occurring and show the status in the GUI when open.*

This is not a display bug. There are three independent reasons it cannot
work today, and fixing any one of them alone changes nothing.

**2.1 The local API has no way to ask "what is running".**
The only progress endpoint is `GET /backup/status/{jobID}`
(`gui/api/server.go:70`). The caller must already know the job ID. A job ID
is minted inside `handleBackup` (`server.go:134`) at the moment the GUI posts
`/backup` — so by construction the only run the GUI can observe is one it
started itself in this session. There is no list, no "current", no "recent".

**2.2 `/status` is stubbed to say nothing is running.**

```go
status := StatusResponse{
    Running:       true,
    Version:       "0.1.92", // TODO: get from build
    ActiveJobs:    0,        // TODO: track active jobs
```

`ActiveJobs` is a literal zero. The one field that could have answered the
question was never wired, and the version string is a hardcoded lie two
minors stale.

**2.3 A scheduled run never enters the progress map at all.**
`s.backupProgress` is written in exactly one place: `handleBackup`. The
scheduler calls `a.StartBackup` directly (`gui/scheduler.go:595`), which in
the service process resolves to the service build's implementation and runs
the engine in-process. It never passes through the HTTP handler, so it never
creates a progress entry. Even after fixing 2.1 and 2.2, a scheduled backup
would still be invisible.

**2.4 And the GUI is not polling for it anyway.**
`App.jsx:523` polls `GetJobHistory()` every 10 s. Job history is a file of
*finished* runs. Live progress reaches the GUI only through Wails events
emitted **in the GUI's own process**, which a service-executed backup cannot
emit into. The GUI's live view is a view of its own memory.

So: no endpoint, no tracking, no registration, no poll. Four layers, one
cause — the GUI was the original backup engine and still behaves like one.

---

## 3. Target architecture

**One pipeline. It lives in the service. The GUI is an HTTP client.**

```
  NimbusControl ──check-in──┐
                            ▼
                    ┌───────────────────────────────┐
                    │  SERVICE (LocalSystem)        │
                    │   scheduler ─┐                │
                    │   portal cmd ├─► RunRegistry ─┼─► engine
                    │   local API ─┘   (one entry)  │   (machine|directory)
                    └───────────┬───────────────────┘
                                │ local HTTP, token-authed
                    ┌───────────▼───────────────────┐
                    │  GUI (user session)           │
                    │   renders. executes nothing.  │
                    └───────────────────────────────┘
```

### 3.1 One entry point

Every way a backup can begin — the scheduler, a portal `backup_start`
command, the local API — calls **one** function in the service that assembles
`BackupOptions`, registers the run, executes, and finalizes. The GUI build
gets no backup engine linked into it at all.

The strongest form of "no dead-end code" available here is **build-time**:
`RunMachineBackup`, `RunBackupInline` and `startBackupDirect` should not be
reachable from the GUI binary. That is stronger than a policy check, and it
makes the standalone-mode question (V4-CLIENT-CONFIG.md §5.2) moot in the
default build — there is no code path to fall back *to*.

**Decision (CJ, 2026-08-05): the escape hatch is a SERVICE-side toggle, and
the GUI gets no engine.** This is the version that makes the rest of the
design cheap rather than the version that keeps an engine in the GUI behind
a flag.

What it means concretely:

- The GUI build does not link `RunMachineBackup`, `RunBackupInline` or any
  option assembly. Not gated — **absent**. A modified GUI has nothing to call.
- The toggle lives where the brains already are: the service, running as
  LocalSystem, always on. It governs whether the service may execute
  **unmanaged** work — local jobs authored on the machine rather than by
  NimbusControl.
- `HKLM\SOFTWARE\NimbusBackup`, `REG_DWORD` `AllowUnmanagedBackups`,
  following `gui/breakglass_windows.go` exactly: HKLM so setting it needs
  Administrator, because an override any logged-on user can flip is not an
  override. Read by the service, never by the GUI. **Built** —
  `controlplane/unmanaged.go` holds the decision (pure and time-injected,
  like `breakglass.go`), the org key is `restrict_unmanaged_backups`, and
  the gate sits at the two entry points that are unmanaged by definition:
  `executeScheduledJob` when its `requestID` is empty (nobody asked for it)
  and the service's `StartBackup` (the console and the local API). A
  control-plane `run_backup` command carries a request id and is never
  gated — an org restricting a machine to managed jobs must not stop the
  org from running one.

**The property that makes it safe is the one break-glass already has: locally
set is necessary but not sufficient.** The service is the thing the control
plane talks to, so an org policy key can render the toggle inert while the
server is reachable. A registry flag in the GUI could never have had that —
the GUI is not on the wire. Precedence:

| Control plane | Registry toggle | Result |
|---|---|---|
| reachable, org forbids unmanaged | either | refused, and reported |
| reachable, org permits unmanaged | on | unmanaged jobs run |
| reachable, org permits | off | managed jobs only |
| none configured (unmanaged install) | on | the standalone product works |
| unreachable > 5 min, last policy forbade it | on | allowed, logged as an override |
| unreachable, last policy forbade it | off | refused — cached policy holds |

The floor is 5 minutes, shorter than break-glass's 15, and that difference is
pinned by a test so nobody merges them into one constant: break-glass guards
reading data OUT of backups, where waiting is an inconvenience; this guards
TAKING a backup, where waiting is the window in which the data can be lost.

One thing came out the opposite way round from the rest of the policy system.
Every other capability is named for the permission, so a `Policy` zero value
denies and an agent that has never checked in fails closed. This key is named
for the RESTRICTION, so the zero value permits. Failing closed here would mean
a machine that cannot reach the server stops backing up — the outcome the
product exists to prevent — and would kill every existing unmanaged install on
upgrade. Pinned by `TestPolicyZeroValuePermitsUnmanagedBackups`.

The refusal row matches the existing decision
that org file-restore policy stays authoritative during an outage
(`docs/CONTROL-PLANE.md`): during a ransomware event the needed operation is
a full-machine restore, which is not behind that gate at all.

The bottom two rows are why this cannot simply be "no hatch". The project is
public GPL-3 and works without NimbusControl; an install with no control
plane configured is a supported product, not a bypass.

### 3.2 `ModeStandalone` is split

It currently means two unrelated things (V4-CLIENT-CONFIG.md §5.2.1). Under
this design the meanings separate cleanly and the enum stops being a hazard:

- `ModeService` — the GUI, talking to the service. The only mode in which
  anything can be started.
- `ModeServiceUnavailable` — the service did not answer. Was called
  `ModeStandalone`, and the name was load-bearing in the wrong direction: it
  read as a capability when the condition it describes is a failure. Nothing
  branches on it now except to refuse.
- `ModeInProcess` — *I am the service*, execute here. Never true in the GUI.
- `ModeDetached` — deleted. With the engine gone from the GUI there is no
  third mode: the service either runs the work or nothing does.

### 3.3 The run registry

The service owns a registry of runs, and it is the single thing both the
status API and the control-plane reporter read from. One run record:

```
run_id          service-minted, stable, survives GUI restarts
job_id          the scheduled job, or "" for an ad-hoc run
trigger         schedule | portal | manual | startup
state           preparing | running | finalizing | done
started_at      UTC
progress        percent, bytes done/total, chunks new/reused/failed
current_dir     what it is working on now
result          BackupStatus, once done
```

`BackupProgressStats` and `BackupStatus` (`gui/backup_status.go`) already
carry every field needed. Nothing new has to be designed; it has to be
*owned* by the service and *readable* by anyone who asks.

### 3.4 The local API grows the endpoints that were missing

- `GET /runs/active` — the answer to 2.1. Zero or more running runs, with
  full live stats. This is what makes a scheduled backup visible.
- `GET /runs/recent?days=7` — the seven-day history CJ asked for, served from
  the service rather than the GUI's own file.
- `GET /status` — real `ActiveJobs`, real version from the build stamp, plus
  per-destination PBS connection state and control-plane reachability.
- `GET /runs/{run_id}` — one run, live or finished.

`/backup/status/{jobID}` stays for one release as a redirect to
`/runs/{run_id}`, then goes.

### 3.5 The GUI polls, and only polls

One poller, one endpoint set, same code path whether the run was started by
the Start button, the scheduler, or the portal. **The Start button stops
being special.** It posts to `/runs` and then watches the same
`/runs/active` every other client watches — which is precisely the property
CJ asked for: *it should show the status accurately, as if it was started
from the start backup button.* The way to guarantee that is for there to be
no "as if": one observation path, always.

Wails events remain useful as a latency optimization *within* the GUI process
(a poll landing 2 s late is fine, but instant feedback on a button press is
nicer). They must never be the only source, or this bug returns.

---

## 4. Read-only mode — what the panel shows

CJ's specification, verbatim in substance: a status panel for the connections
to servers, the backup status over the last seven days, and the statistics of
any currently running backup.

| Panel | Source |
|---|---|
| Connection to each PBS destination | `GET /status`, per-destination reachability |
| Connection to NimbusControl | `GET /status`, last successful check-in |
| Last seven days of runs | `GET /runs/recent?days=7` |
| Currently running backup | `GET /runs/active` — percent, bytes, chunks, current directory, elapsed |

Nothing else renders. Not disabled controls — **absent** ones, matching the
portal's `View::cap` no-leak rule: a denied control is not in the markup.

Enforcement is in the service's handlers, not the GUI's rendering. A modified
GUI that draws a Start button gets an HTTP error when it presses it.

---

## 5. What has to be deleted

"No dead-end code" is the requirement, so this is a list of removals, not
just additions:

1. ~~`startBackupDirect` and its option assembly~~ — gone
2. ~~The GUI-build `StartBackup` routing switch~~ — gone; there is one path
3. ~~The GUI's own scheduler start~~ — gone, along with the GUI's
   control-plane check-in loop and its VSS cleanup. All three were guarded by
   "the service did not answer a probe", so a service merely slow to start
   produced a GUI running a scheduler, a check-in loop and a VSS cleanup in
   parallel with the service doing the same
4. ~~`ShouldWarnVSS` / `GetModeDescription`~~ — gone
5. `handleBackupStatus`'s job-ID lookup, after the deprecation release
6. The standalone CLI engines from the customer release (§7 item 2), and
   `directorybackup`'s private `ChunkState`/didx implementation with them
   once the CLI is a service client

Whatever is left after that is the pipeline.

---

## 5a. What the build tags now separate

Collapsing the pipeline into the service left the GUI build carrying a lot of
machinery it could no longer reach. `golangci-lint`'s `unused` check found it
immediately — fifteen findings, every one a piece of backup-execution support —
and the fix was to move each to the side that actually uses it, not to silence
the linter.

| Now service-only | Why |
|---|---|
| `backup_pipeline.go` | the engine (§3.1) |
| `scheduler_run.go` | the loop drivers: `StartScheduler`, `checkAndRunScheduledJobs`, `HandleStartupRun`, `CleanupAbandonedJobs`, `RecalculateNextRuns`. The GUI edits jobs; the service runs them |
| `controlplane_runreport.go` | `attachControlPlaneHooks` and its helpers — a front end that cannot run a backup has nothing to report |
| `runs_attach.go` | hooking the engine into the run registry |
| `api_callbacks.go` | the engine → local-API progress relay, plus the callbacks map that used to sit on `App` |
| `pipeline_support.go` | backup cancellation, and the post-backup Exchange trigger |
| `exchange_post_windows.go` | the tasks that run `diskshadow.exe` and PowerShell against live Exchange databases. `windows && service`: both tags load-bearing |
| `service.go`, `service_main.go` | the Windows service host. `service.go` was `windows` only, so the GUI binary contained the whole service |

`scheduler.go` keeps job CRUD **and `executeScheduledJob`**, because a
control-plane `run_backup` command can be delivered to whichever process hosts
the agent, and in the GUI that path reaches `StartBackup` — an HTTP POST to the
service. What it must NOT do there is announce the run locally: that is behind
`announceLocalRun`, real in the service and a no-op in the GUI, because the
service announces the run on its own side and a second record would sit in
"preparing" forever.

### The Wails event seam is gone

§3.5 said events would remain as a latency optimization inside the GUI process.
That stopped being possible at step 4. The only emitter was the pipeline, and
the pipeline is service-only, where `emitBackupEvent` was the no-op stub — so
the events were emitted nowhere at all, and the front end's handlers for them
were unreachable. Both sides are deleted. **Polling is the only observation
path**, which is what §3.5 wanted anyway; the poller now also resolves the
terminal outcome, since nothing pushes it any more.

### Both build configurations are linted

`golangci-lint` ran on linux+default only, so the service build — the one on
every managed machine, and now the one holding most of the code — was never
linted. A second pass runs it with `--build-tags=service`.

`unused` is disabled in that pass, and only there. It reports symbols with no
visible caller, a question that only has a meaningful answer where every caller
IS visible; the service build excludes GUI-only files, so it would flag every
helper the front end owns. The default pass keeps `unused` and covers the
shared code.

---

## 5b. The GUI does not evaluate org policy

Found while auditing what step 4 left behind, and it predates the rewire.

`ControlPolicy().FileRestore` gates every browse and restore call in
`imagebrowse_core.go` and `controlplane_browse.go`. Those are Wails bindings,
so on a managed machine **they run in the GUI process** — and the shared
implementation read `cpAgent`, which is nil there, and treated a nil agent as
"standalone install, nothing to enforce":

```go
if cpAgent == nil {
    return controlplane.Policy{FileRestore: true}
}
```

The GUI only ever populated `cpAgent` in the old standalone mode, so on any
machine where the service was running — the normal case — org file-restore
policy did not apply to the front end at all. Step 4 made it universal by
removing the last path that populated it.

The fix is not to give the GUI an agent. It is for the GUI to **ask the
process that has one**: `ControlPolicy` is now split by build tag, and the GUI
implementation reads the effective policy from `/controlplane/status`, cached
for ten seconds because a directory walk consults it hundreds of times. The
service has already folded break-glass into the value it reports, so the GUI
takes it and does not second-guess it.

**It fails closed**, which is the opposite of the choice made for unmanaged
backups (§3.1) and for the same underlying reason: the costs run in opposite
directions. Refusing a restore during an outage is an inconvenience with a
documented override; permitting one is the data leaving the building. An
install with no control server configured stays permissive — ungoverned by
design, not by accident.

---

## 6. Sequencing

The bug is worth fixing before the rewire lands, and it can be, because
steps 1-2 are additive and independently shippable.

1. ~~**Run registry + `/runs/active` + `/runs/recent` in the service**, and
   register scheduler-triggered runs in it.~~ **Done.** `api.RunRegistry` is
   the single store; `gui/runs_glue.go` opens a record beside
   `registerRunReporter` when the scheduler decides to run, and
   `attachRunRegistry` adopts it at `BackupOptions` construction rather than
   opening a second one. A scheduled backup is now observable at
   `/runs/active` from the moment it is scheduled, minutes before the first
   chunk uploads. `/status` reports a real `ActiveJobs` and a stamped
   version instead of `0` and the literal `"0.1.92"`.
2. **GUI polls the registry.** Done for live runs: `GetActiveRuns` /
   `GetRecentRuns` are bound to the front end, and a three-second poll drives
   the SAME progress state the Wails events drive, so the card renders
   identically however the run began. Elapsed time is measured against the
   SERVICE's clock and the run's real start rather than against first sight,
   which is what makes a GUI opened mid-backup show a correct rate and ETA
   instead of ones anchored to when the window happened to open.

   Still on `GetJobHistory()`: the history TABLE. Swapping it to
   `/runs/recent` is part of the read-only panel in step 5, since that is
   where its shape is decided. Nothing about the reported bug depends on it —
   a running backup is now visible whether or not the table changes. Start button
   still works as it does; it just no longer has a private view.
3. ~~**Move option assembly into one service-side function.**~~ **Done.**
   `gui/backup_pipeline.go` is the single pipeline; both builds call
   `runBackupPipeline`. The only genuinely build-specific thing left is
   whether the process has a Wails window to emit into, isolated behind
   `emitBackupEvent` (real in the GUI build, a no-op in the service).
   `pollBackupProgress` is deleted — a per-job poller relaying the same
   numbers through events was a second observation path, and the front end
   now polls `/runs/active` for every run. The GUI build's `StartBackup`
   becoming an HTTP POST and nothing else lands with step 4.
4. ~~**Drop the engine from the GUI build.**~~ **Done.** One build tag on
   `backup_pipeline.go`, which is the only caller of `RunBackupInline` and
   `RunMachineBackup`, leaves both unreferenced in the GUI build and the
   linker drops them. Verified against the shipped binary, not the source:
   `go tool nm` finds zero engine symbols in `NimbusBackup.exe` and 15 in
   `NimbusBackupSVC.exe`, and CI now asserts that on every build — a build
   tag is exactly the kind of thing a later refactor removes without
   noticing.
5. **Read-only mode.** The ENFORCEMENT half is built:
   `gui/api/readonly.go` refuses every request that is not on an explicit
   allowlist while `gui_read_only` is set, wired in
   `authMiddleware(readOnlyMiddleware(mux))` — inside auth, so an
   unauthenticated prober gets 401 and learns nothing rather than 403 and
   learns the machine is managed and locked.

   **The list is an allowlist, and that is the design.** Enumerating the
   mutating routes instead would leave every route added later permitted
   until somebody remembered to list it, and "somebody remembers" is not a
   control. Method is pinned alongside path, so a reader that grows a POST
   branch later cannot quietly widen what a locked agent will do.

   Read-only does not stop the machine backing up. Scheduled work and
   control-plane commands originate inside the service and never touch this
   API; what stops is the console driving them.

   The PANEL is built too: `src/StatusPanel.jsx`, rendered **instead of**
   the tabbed UI rather than alongside it with the controls disabled. A
   greyed-out Start button still tells a user the capability exists and
   invites them to find out why it is off; an absent one does not.

   It draws from three polled endpoints and nothing else — `/connections`,
   `/runs/active`, `/runs/recent` — so it has no path to any state the
   service has not published. The seven-day view is a per-day rollup showing
   the **worst** outcome of each day, plus the last ten runs in full: a
   machine backing up hourly has ~168 runs in a week, and a day with eleven
   successes and one failure is a day something went wrong, so showing it
   green because the last run passed would be worse than no panel.

   Tri-state reachability survives into the CSS. `-unknown` is neutral, not
   a paler red: "nobody has checked" is not a degraded state, and colouring
   it like one sends a technician to inspect a firewall for a config
   problem.
6. **Managed jobs and schedules** (NimbusControl `V4-CLIENT-CONFIG.md`) land
   on top of a pipeline that has one entry point, which is the reason to do
   this first rather than after.

   Server side is built (`backup_jobs`, delivery on the check-in,
   server-derived expectations). Client side receives the set, caches it in
   `managed_jobs.json` — a SEPARATE file from `scheduled_jobs.json`, because
   the managed set is replaced wholesale and the only safe wholesale replace
   is one with nothing else in the container — and can run a managed job on
   a portal `run_backup`.

   **Managed jobs now fire on their own schedules.**
   `controlplane/calendar.go` is the Go evaluator, pinned against the same
   fixture file the PHP parser is tested with, and
   `gui/managed_scheduler.go` evaluates it on the scheduler tick. Fire
   history lives in a THIRD file (`managed_job_state.json`) because
   `managed_jobs.json` is replaced wholesale every check-in — storing "last
   fired" there would reset it every two minutes and turn a daily job into
   a backup storm.

   Two rules, both about NOT starting backups: a job seen for the first
   time is SEEDED rather than fired, so authoring a job at 14:00 whose
   schedule says 02:00 does not start a backup on every machine in the org
   the moment it is saved; and a missed window COLLAPSES to one run,
   because catching up on backups whose moment has passed is pointless when
   the only state anyone wants is the current one.

   `ScheduleTime` is still left empty on an adapted managed job. It is the
   legacy HH:MM field, the local scheduler keys on it, and filling it would
   give that scheduler a second and wrong opinion about when the job runs.

---

## 7. The CLI binaries

CJ's requirement: the CLI must not be able to bypass MSP or org policy.

**Today it bypasses everything, because it was never inside anything.**
`directorybackup/`, `machinebackup/` and `nbd/` are the upstream
`proxmoxbackupclient_go` tools, shipped in every release (`make cli`, S12
smoke, `cli-${{ runner.os }}` artifacts). Grepping all three for
`controlplane`, `Policy` or `file_restore` returns nothing. `directorybackup`
builds its own `PBSClient` from flags and its own `ChunkState` — a third
independent copy of the chunking pipeline, alongside the GUI's and the
service's.

**The honest ceiling first.** Anyone holding PBS credentials and a shell can
run Proxmox's own `proxmox-backup-client`. No amount of work on our CLI
changes that, and a design that claims otherwise is lying about its threat
model. What policy enforcement actually rests on is elsewhere and already
partly built: per-org PBS namespaces with per-org tokens, quota enforcement
that revokes PBS write access, and secrets sealed with a DPAPI/TPM-backed DEK
so the stored credentials are not readable as text.

Given that ceiling, three things are worth doing, in order:

1. **The CLI becomes a client of the service, not an engine.** It talks to
   the same local API the GUI does — token-authed, same handlers, same
   policy gates — and inherits every refusal for free. It stops being a
   fourth pipeline and becomes an automation surface, which is the version
   of "CLI" an MSP actually wants: scriptable, and no more privileged than
   the console.
2. **Stop shipping the standalone engines in the customer release.** They are
   recovery and development tools. If they remain in the release, they are a
   supported way to run a backup with no policy in the path, and the MSI's
   Authenticode signature vouches for them.
3. **Credentials stay with the service.** The enforcement that survives a
   local administrator is not a check in our code; it is that the PBS token
   which can write to the org's namespace is held sealed by the service and
   not handed to a process that asks nicely.

Item 1 is the one that satisfies the requirement as stated. Items 2 and 3
are what make it true rather than nominal.

---

## Open decisions

1. **Seven days, or seven days *and* a count?** A machine backing up hourly
   has ~168 runs in a week. Recommendation: the panel shows a per-day rollup
   with the worst outcome of each day, and the last ten runs in full.
2. **Does read-only hide the log viewer?** It is read-only by nature and is
   the first thing a technician wants, but it can contain paths and PBS
   hostnames. Recommendation: keep it, since a locked GUI that cannot show
   why a backup failed sends every question to the MSP helpdesk.
