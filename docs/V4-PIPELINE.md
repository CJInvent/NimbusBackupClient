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
| sets `opts.OnResult` | **yes** (`main.go:~1080`) | **no** |
| writes local job history | yes | yes, separately |

Two implementations of one operation, each ~150 lines of option assembly,
already diverging. `OnResult` is the divergence with teeth: it is the hook
that produces the per-snapshot status sidecar (`BackupStatusFilename`,
`nimbus-status.json.blob`) and the structured `BackupStatus`. The build that
sets it is the GUI. The build that runs on every managed machine does not.

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
- `HKLM\SOFTWARE\NimbusBackup`, `REG_DWORD`, following
  `gui/breakglass_windows.go` exactly: HKLM so setting it needs
  Administrator, because an override any logged-on user can flip is not an
  override. Read by the service, never by the GUI.

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
| unreachable, last policy forbade it | on | refused — cached policy holds |

The last row is the fail-closed one, and it matches the existing decision
that org file-restore policy stays authoritative during an outage
(`docs/CONTROL-PLANE.md`): during a ransomware event the needed operation is
a full-machine restore, which is not behind that gate at all.

The bottom two rows are why this cannot simply be "no hatch". The project is
public GPL-3 and works without NimbusControl; an install with no control
plane configured is a supported product, not a bypass.

### 3.2 `ModeStandalone` is split

It currently means two unrelated things (V4-CLIENT-CONFIG.md §5.2.1). Under
this design the meanings separate cleanly and the enum stops being a hazard:

- `ModeService` — the GUI, talking to the service. The only shipped GUI mode.
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

1. `startBackupDirect` and its option assembly (`gui/main.go:911-1120`)
2. The GUI-build `StartBackup` routing switch (`main.go:767-799`)
3. The GUI's own scheduler start (`main.go:244-262`) — the service schedules
4. `ShouldWarnVSS` / `GetModeDescription` (`gui/api/mode.go`) — both exist to
   explain standalone mode to the user, and both are French-only string
   literals in a codebase with an i18n catalog
5. `handleBackupStatus`'s job-ID lookup, after the deprecation release
6. The standalone CLI engines from the customer release (§7 item 2), and
   `directorybackup`'s private `ChunkState`/didx implementation with them
   once the CLI is a service client

Whatever is left after that is the pipeline.

---

## 6. Sequencing

The bug is worth fixing before the rewire lands, and it can be, because
steps 1-2 are additive and independently shippable.

1. **Run registry + `/runs/active` + `/runs/recent` in the service**, and
   register scheduler-triggered runs in it. Fixes the reported bug on its
   own: a scheduled backup becomes observable.
2. **GUI polls the registry** instead of its own history file. Start button
   still works as it does; it just no longer has a private view.
3. **Move option assembly into one service-side function**; the GUI build's
   `StartBackup` becomes an HTTP POST and nothing else.
4. **Drop the engine from the GUI build** (Q1 decides how).
5. **Read-only mode**, which by then is the ordinary panel minus the
   mutating endpoints, and is enforced by handlers already written.
6. **Managed jobs and schedules** (NimbusControl `V4-CLIENT-CONFIG.md`) land
   on top of a pipeline that has one entry point, which is the reason to do
   this first rather than after.

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
