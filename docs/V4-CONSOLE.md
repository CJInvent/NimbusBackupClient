# v4: the console is a client of the service, and nothing else

**Status (2026-10-17).** Specification only; no code changes. Written against
`v4_dev` at `835da16` (dev release `v1.01.00-dev.244`). Every measurement below
was taken from that commit with the CI-pinned toolchain (`go1.26.9`), building
`GOOS=windows` with the shipped GUI tags (`desktop,wv2runtime.download,production`).
The local build is a faithful model of the shipped one: 13,268,992 bytes stripped
here against 13,459,968 shipped (the difference is CGO and the version resource).

**Decided (CJ, 2026-10-17):** D1 (b), the folder picker moves into the service;
D2 (b), the service installs WebView2 and the console drops the downloader;
D3, `cleanup_legacy.go` is deleted. F1 became its own specification,
`docs/V4-LOCAL-API.md` (the named pipe, caller identity, the elevated Browse
window), which replaces this document's C4. Build order across both documents:
C1, C2, C3 here, then P1 to P5 there.

Companion documents: `docs/V4-PIPELINE.md` (the backup rewire, CJ's rule of
2026-08-05), `docs/V4-RESTORE.md` (the restore rewire), `docs/V4-GUI-AUDIT.md`
(the screens; this document is the Go half of its S8 and the ground S5 stands
on), `docs/INDEPENDENCE-GOALS.md`.

CJ's statement, 2026-10-17:

> The client GUI should not contain hardly anything except what interacts with
> the service. The GUI has nothing to do with direct PBS connections. All
> functional code should be in the service, the GUI is just a WebUI that
> interacts with the Service API.

The rule is not new. V4-PIPELINE.md §3 stated it for backup in August, and
V4-RESTORE.md applied it to restore. Both rewires were done, and both moved
*execution*. Everything else stayed: configuration, secrets, the control-plane
agent, scheduled-job writes, PBS management, provisioning, storage identity.
This document is the rest of the job.

---

## 1. What the GUI actually is today

### 1.1 It compiles most of the service

The GUI and the service are one Go package (`gui/`) built twice, split by the
`service` build tag. The GUI build compiles **77 files**. Seven are GUI-only
(1,631 lines). The other **70 are compiled into both binaries: 13,302
lines**, including:

| Compiled into the GUI | What it is |
|---|---|
| `backup_inline.go` (1,449 lines) | the directory backup engine, `RunBackupInline` |
| `machine_backup_windows.go` (1,222) | the image backup engine, `RunMachineBackup` |
| `controlplane_glue.go` (681) | the control-plane agent host: enrollment, check-in loop, command dispatch |
| `backupkey_store.go` (549), `backupkey_gate.go`, `backupkey_ephemeral.go` | the org backup-key vault and the verification gate |
| `agentkey_store.go` (526), `agentkey_register.go`, `agentkey_rotate.go`, `agentkey_open.go` | this machine's X25519 identity and its private key's custody |
| `secrets.go`, `secrets_windows.go`, `tpm_windows.go` | the master DEK: DPAPI and TPM unwrap |
| `pbs_credential.go` (406), `pbs_credential_open.go` | the machine's own PBS credential, applied from the server |
| `datapath_identity.go`, `datapath_mode.go`, `datapath_grant.go` | the data-path client certificate, mode and grants |
| `provisioning.go`, `provisioning_registry_windows.go` | MSI provisioning-profile ingestion |
| `managed_jobs.go`, `scheduler.go` | the managed job set and local-job file writes |
| `storage_*.go`, `disk_inventory.go` | storage identity and inventory |
| `config.go` | `config.json`, including secret decryption on load |

### 1.2 Wails makes every exported `App` method live

The GUI binds one struct to the webview: `Bind: []interface{}{app}` in
`gui_options.go`. Wails enumerates its methods by reflection, and that forces
the Go linker to keep **every exported method of `App`** and everything those
methods reach. `App` is also the service's type: the local API server calls it
through `api.BackupHandler`, and `service.go` calls it directly. So any exported
`App` method in a file without the `service` tag is a webview binding.

There are **83** of them in the GUI build. The front end calls **52**. The
other **31** are bound but never called by any screen, and several are the
service's own entry points:

`StartControlPlane`, `StopControlPlane`, `RestartControlPlane`,
`SaveControlPlaneFromMap`, `ControlPlaneStatusMap`, `ApplyProvisioningProfile`,
`SaveConfig`, `SaveConfigFromMap`, `SavePBSServerFromMap`, `DeletePBSServerByID`,
`SetDefaultPBSByID`, `PinServerFingerprint`, `TestConnection`, `ReloadConfig`,
`GetConfig`, `DiagnoseConfig`, `GetPBSServer`, `GetHostname`,
`GetScheduledJobsForAPI`, `SaveScheduledJobFromMap`, `UpdateScheduledJobFromMap`,
`DeleteScheduledJobFromMap`, `AddJobHistory`, `StorageIdentityStatusMap`,
`ApproveStorageIdentityFromMap`, `SetupSystemTray`, `MinimizeToTray`,
`ShowFromTray`, `UpdateTrayTooltip`, `OpenSaveFileDialog`, `OpenRestoreDestDialog`.

All 83 are in the shipped binary's symbol table, and they pull in what they
reach. Traced from the linker's dependency dump and confirmed in the symbol
table of the unstripped build:

| Linked into the GUI | Chain |
|---|---|
| `controlplane.(*Client).Enroll` | `App.StartControlPlane` |
| `controlplane.(*Client).RegisterKey` | `App.StartControlPlane → ensureAgentKeyRegistered` |
| `controlplane.(*Client).FetchPBSCredential` | `App.StartControlPlane → applyPBSTargetFromCheckin → withAgentKeyFetcher` |
| `crypto/x509.CreateCertificateRequest`, `crypto/ecdsa.GenerateKey` | `App.StartControlPlane → applyDatapathFromCheckin → renewDatapathIdentity` |
| `pbscommon.(*PBSClient).CheckConnectivity` | `App.StartControlPlane → startPBSPoller → runPBSPollLoop` |
| `pbscommon.(*PBSClient).TestConnection` | `App.TestConnection`, `App.TestPBSConnection` |
| `pbscommon.FetchServerFingerprint` | `App.GetServerFingerprint` |
| `controlplane.ParseProfile` | `App.ApplyProvisioningProfile` |
| `main.decryptSecret` | `main.main → NewApp → LoadConfig` (runs at every GUI start) |
| `klauspost/compress/zstd` decoder | `pbscommon`'s package `init`: importing `pbscommon` at all links it |

And because Wails calls `reflect.Type.Method`, the linker also keeps every
exported method of every type the program puts in an interface. That is why
`controlplane.(*Client).DownloadUpdate` and `RequestDatapathCert` are linked
although nothing in the GUI calls them.

This is also the answer to "why did the MSI grow 400 KB when we added the
proxy": the data-path identity code went into an untagged file, the GUI hosts
the check-in loop, and so the certificate-signing code was linked into both
binaries.

### 1.3 The GUI still behaves as a standalone client when the service is down

V4-PIPELINE.md §3.2 renamed the old standalone mode to
`ModeServiceUnavailable`, and backup and restore refuse in it. The other
bindings do not. Seven write bindings check whether the service answered and,
when it did not, **write `config.json` directly from the user's session**:
`SaveConfig`, `AddPBSServer`, `UpdatePBSServer`, `DeletePBSServer`,
`SetDefaultPBSServer`, `PinPBSServerFingerprint`, `SaveControlServerConfig` (which
then calls `SaveControlPlaneFromMap → RestartControlPlane`, so **the GUI starts
its own control-plane agent**: enrolls, checks in, renews the data-path
certificate). Others never delegate at all:

| Binding | What it does in the user's session |
|---|---|
| `SaveScheduledJob`, `UpdateScheduledJob`, `DeleteScheduledJob` | writes `scheduled_jobs.json` (GUI audit finding 3: the lock never sees these) |
| `GetScheduledJobs`, `GetJobHistory` | reads the service's files directly |
| `TestPBSConnection` | builds a `pbscommon.PBSClient` and dials PBS with the stored token |
| `GetServerFingerprint` | dials an arbitrary URL from the GUI |
| `ListPhysicalDisks` | opens `\\.\PhysicalDriveN` from the GUI |
| `GetExchangeStatus`, `QueryExchangeLogMode` | probes Exchange from the GUI |
| `GetSecurityWarnings` | probes machine posture from the GUI |
| `ListPBSServers`, `GetDefaultPBSID`, `GetConfigWithHostname`, `GetLastBackupDirs` | read the GUI's own copy of `config.json`, decrypted at startup |

`LoadConfig` runs at GUI start and calls `decryptSecret` on every stored secret.
Whether that succeeds for a standard user depends on the ACLs of `config.json`
and `master.key`, which inherit ProgramData's (V4-STATUS raised item, still
open). `secrets_windows.go` says why the DEK is DPAPI *machine* scope: "the
LocalSystem service and a user-context standalone GUI must both be able to
unwrap the same DEK". That reason ends with this work (section 7, follow-up 2).

### 1.4 Why CI said the GUI had no engine

Both checks are denylists. "Assert the GUI compiles no engine" names ten files
(`backup_pipeline.go`, `restore_inline.go`, and so on) and passes because those
ten are service-only; `backup_inline.go` and `machine_backup_windows.go` are not
on it. "Assert GUI links no engine symbols" greps eight function names and
passes because the linker pruned those eight. Neither asks the question that
matters: *what does the GUI depend on at all?*

---

## 2. The target

```
  console/  (own Go module, own main, user session)
    Wails window + tray + frontend assets
    one bound type, Console: every method is one local-API call
    depends on: gui/api, wails, systray, x/sys     ← enforced
             │
             │ local API (token-authed, 127.0.0.1:18765)
             ▼
  gui/  (the service, LocalSystem; no `service` tag any more)
    everything else: engines, agent, keys, secrets, config, jobs, PBS
```

1. **A package boundary, not a build tag.** The console is its own module
   (`console/`, with its own `go.mod`). It cannot import the service's code
   because that code is not in its module graph; a mistake is a compile error,
   not a linker accident. Build tags have already got this wrong twice in the
   GUI's direction (§1.4's engine files, §1.2's proxy identity), and they will
   keep doing so while one package means two programs.
2. **One bound type, holding nothing.** `Console` has a context, an `api.Client`
   and the tray state. Every exported method is a binding the front end uses,
   and every binding is a single call to the service. There is no `Config`, no
   decrypted secret, no file the service also writes.
3. **No fallback.** When the service does not answer, the console says so and
   does nothing (GUI audit §2, "Service down": one sentence, nothing else
   renders). The MSI always installs the service; a console without a service
   is a broken install, not a mode.
4. **The `service` build tag disappears.** `gui/` *is* the service. Files tagged
   `!service` leave it; files tagged `service` lose the tag; `windows && service`
   becomes `windows`. Every stub that existed only so the GUI view compiled is
   deleted.

---

## 3. Every binding, after

The 52 bindings the front end calls keep their names and arguments, so the
screens do not change (the restore rewire did the same). 31 are not carried
over. "NEW" marks a route the service does not have yet (section 4).

| Binding | After | Today |
|---|---|---|
| `GetSystemInfo` | `GET /status` (hostname, version); `os` and service reachability are the console's own facts. **`is_admin` removed**: no screen reads it | local |
| `GetVersion` | the console's own build stamp | local |
| `IsReadOnly` | `GET /controlplane/status` `gui_read_only` (as now, via `controlplane_policy_gui.go`) | API |
| `GetConfigWithHostname` | `GET /status` `configuration` (the handler already returns it) | local copy of `config.json` |
| `GetLastBackupDirs` | `GET /status` `configuration.last_backup_dirs` — **NEW field** | local |
| `GetSecurityWarnings` | `GET /status` `security_warnings` — **NEW field**; the service computes posture | local probe |
| `GetExchangeStatus`, `QueryExchangeLogMode` | `GET /exchange` — **NEW** | local probe |
| `ListPhysicalDisks` | `GET /disks` — **NEW**, served from the same enumeration as `disk_inventory.go` | opens the device from the GUI |
| `GetControlServerStatus` | `GET /controlplane/status`, no local fallback | API, else local |
| `SaveControlServerConfig` | `POST /controlplane/save`, no local fallback | API, else writes config and **starts an agent** |
| `GetConnections`, `GetActiveRuns`, `GetRecentRuns` | unchanged | API |
| `StartBackup`, `StopBackup` | unchanged | API |
| `GetScheduledJobs` | `GET /jobs` | reads the file |
| `SaveScheduledJob`, `UpdateScheduledJob`, `DeleteScheduledJob` | `POST /jobs/create`, `/jobs/update`, `/jobs/delete/{id}` (routes exist; this closes GUI audit finding 3) | writes the file |
| `GetJobHistory` | **removed**; the history table reads `GetRecentRuns` (section 4.2) | reads the file |
| the 14 restore bindings | unchanged (already API) | API |
| `CheckDownloadSpace` | `POST /restore/query` op `space` — **NEW op**, declared in `api/restore.go`'s table with the `file_restore` right, computed by the process that writes the bytes | local |
| `ListDrives`, `ListFolders`, `CreateFolder`, `DefaultSaveDir` | stay in the console through C3; V4-LOCAL-API P3 moves them to `/fs/list` and `/fs/mkdir`, served as the caller (D1) | local |
| `ListPBSServers`, `GetDefaultPBSID` | `GET /pbs/servers` — **NEW** (sanitized, plus the default id) | local copy |
| `AddPBSServer`, `UpdatePBSServer` | `POST /pbs/save`, no local fallback | API, else writes config |
| `DeletePBSServer`, `SetDefaultPBSServer`, `PinPBSServerFingerprint` | `/pbs/delete/{id}`, `/pbs/default`, `/pbs/fingerprint`, no local fallback | API, else writes config |
| `TestPBSConnection` | `POST /pbs/test` — **NEW**; refused in data-path proxy mode like every PBS setting | `pbscommon` client in the GUI |
| `GetServerFingerprint` | `POST /pbs/discover` — **NEW**; TLS handshake only, no token sent (as today) | dials from the GUI |
| `GetStorageIdentityStatus`, `ApproveStorageIdentity` | `/storage/identity` (exists) | API, with local `*FromMap` twins bound |
| `SetTrayLanguage` | console-local (the tray is the console's) | local |

Four files call `window.go.main.App` directly instead of through
`src/lib/bindings.js`: `StorageIdentityPanel.jsx`, `useMachineContext.js`,
`useStatusPanel.js`, and `i18nContext.jsx` (for `SetTrayLanguage`). They move
behind `bindings.js`, so that file is the one complete list the console's
binding test pins against (section 5).

---

## 4. What the service gains

### 4.1 Routes

All additive, all behind `authMiddleware`, none on the `readonly.go` allowlist
unless named:

| Route | Returns | Locked view |
|---|---|---|
| `GET /pbs/servers` | `[]PBSServer` sanitized (no secret), `default_id`; empty and `datapath_proxy: true` in proxy mode | no |
| `POST /pbs/test` `{id}` | ok or the PBS error, coded | no |
| `POST /pbs/discover` `{url}` | the leaf certificate's SHA-256 fingerprint | no |
| `GET /disks` | `[]PhysicalDiskInfo` | no |
| `GET /exchange` | `ExchangeStatus` and `ExchangeLogMode` | no |
| `GET /status` | adds `security_warnings` and `configuration.last_backup_dirs` | yes (already) |
| `/restore/query` op `space` | `SpaceCheck` | no (restore is never on it) |

`handleBackupStatus` (V4-PIPELINE §5 item 5, kept "for one release") has no
caller in `api/client.go`, the Go code or the front end today; C1 deletes it.

### 4.2 History survives a restart

`GetJobHistory` reads `job_history.json`. The status panel and `GetRecentRuns`
read the run registry, which lives in memory: after a service restart
`/runs/recent` is empty, so switching the history table to it as V4-PIPELINE
§6 step 2 planned would lose every run before the last reboot. The locked view
already has that gap today.

So the registry persists its finished runs (the last 30 days, one file, written
on each terminal transition with the existing atomic writer) and loads them at
service start. That file **replaces** `job_history.json`: the pipeline's
history write and `CleanupAbandonedJobs` move onto the registry (a run found
`running` at start is closed as interrupted, which is what
`CleanupAbandonedJobs` does to the old file), and `GetJobHistory`,
`AddJobHistory` and the file are deleted. One history, which closes GUI audit
finding 10's local half. The table's redesign stays GUI audit S11.

### 4.3 The service sheds GUI code

`tray.go` and `storage_tray_icon.go` are tagged `windows`, not `!service`, so
the service links the tray library and the Wails runtime. Measured in the
service binary: systray and its dependencies 46 KB of code, Wails 12 KB,
`image/*` 103 KB. They move to the console with `icon.go`.

---

## 5. How it is enforced

The current two checks (§1.4) are deleted, because a package boundary replaces
what they were approximating. In their place, four checks, each of which fails
rather than passes on an empty input:

1. **Module allowlist.** `go list -deps` on `console/` for `GOOS=windows`, module
   paths only. Allowed: the console itself, `…/gui/api`, Wails v2 and its
   dependency set, `getlantern/systray` and its dependency set, `golang.org/x/sys`,
   `golang.org/x/net` (Wails' HTML parser), `golang.org/x/exp`, and the standard
   library. Anything else fails, named. The list is written out in the step, as
   `readonly.go` writes out its routes: an allowlist fails on what nobody
   thought to forbid. Today the GUI depends on eleven modules outside that list
   (`controlplane`, `pbscommon`, `retry`, `security`, `snapshot`,
   `klauspost/compress`, `dchest/siphash`, `st-matskevich/go-vss`, `x/crypto`,
   `x/text`, `x/time`). Precondition: the list contains `wails/v2`, so an empty
   result cannot pass.
2. **Binding allowlist.** A Go test in `console/` lists `Console`'s exported
   methods by reflection and compares them with the names exported by
   `frontend/src/lib/bindings.js`. Both directions: a method the front end
   does not use is a binding nobody needs; a name the front end uses that the
   console lacks is a broken screen. Plus a front-end test that `window.go`
   appears only in `bindings.js`.
3. **One call per binding.** A source test over `console/*.go` (excluding
   `main.go`, `tray*.go` and, until V4-LOCAL-API P3, the picker): no `os.WriteFile`, `os.Create`,
   `os.Rename`, `os.Remove`, `registry.`, `exec.Command`, `net.Dial` outside the
   API client. Crude by design; it exists to make the next "just read the file
   here, it's quicker" a red build.
4. **Size budget.** The Build GUI job prints the console's stripped size and
   fails above a budget recorded in the workflow: **11.2 MB** after phase C3,
   lowered to 9.6 MB by V4-LOCAL-API P1 (D2). A budget set close to the measured size
   turns the next unexplained 400 KB into a red build before release rather than
   a question after it.

The lint configuration simplifies with the tag: `gui/` gets one golangci-lint
pass **with `unused` enabled**, which the service code has never had (the
`--build-tags=service` pass disables it, V4-PIPELINE §5a), and `console/` gets
its own. Compile Views (S2) becomes service linux, service windows, console
windows.

---

## 6. Decisions for CJ

**D1. The folder picker.** `ListDrives`, `ListFolders`, `CreateFolder` and
`DefaultSaveDir` list and create folders for choosing a backup source or a
restore destination (178 lines, standard library plus `GetLogicalDrives`).
(a) **Keep them in the console**, reading as the user. (b) Move them to the
service, sharing `controlplane_fsbrowse.go`'s confinement rules with the
portal's browser. **Recommendation: (a), until finding F1 is fixed.** Listing
and creating folders as SYSTEM for any caller holding the shared token would
let any logged-on user enumerate every profile on a terminal server and create
folders anywhere. After F1 gives the service a caller identity, (b) is the
better design, because the service is the process that reads and writes those
paths. Either way the size effect is negligible.

**Settled 2026-10-17 (CJ):** (b), in the service, listing as the caller.
`docs/V4-LOCAL-API.md` gives the service the caller identity this waited for
(its section 5.3).

**D2. The WebView2 runtime download.** The console is built with
`wv2runtime.download`: if WebView2 is missing, Wails downloads Microsoft's
installer. That downloader links `net/http`'s full client with TLS and HTTP/2.
Measured on a prototype console (Wails, tray, API client, the real frontend):

| Prototype | Stripped |
|---|---|
| standard API client, `wv2runtime.download` (what C3 delivers) | 10,968,576 |
| minimal API client, `wv2runtime.download` | 10,936,832 |
| standard API client, `wv2runtime.error` | 10,844,160 |
| **minimal API client, `wv2runtime.error`** | **9,318,400** |

TLS leaves the console only when **both** go: Wails' downloader and our own
client's use of `http.DefaultTransport` (§ C4). WebView2 ships with Windows 11
and current Windows 10, but is not guaranteed on every Windows Server image,
which is what the downloader is for. (a) Keep the downloader: console about
11.0 MB. (b) Make the runtime the **service's** job (it already links TLS and
runs as SYSTEM; on start it checks for the Evergreen runtime and installs it
machine-wide if missing) and build the console with `wv2runtime.error`: console
about 9.3 MB. **Recommendation: (b).** It is the same rule this document
applies everywhere: the console does no installing. **Decided (CJ,
2026-10-17): (b).** Built in V4-LOCAL-API P1.

**D3. `cleanup_legacy.go`.** On every GUI start it deletes a scheduled task and
an HKCU Run value left by pre-MSI versions. It has to run as the user (HKCU),
so it cannot move to the service. **Recommendation: delete it**, with
`cleanup_legacy_stub.go`. A leftover entry starts the console at login, which
is now harmless. **Decided (CJ, 2026-10-17): delete.** In C3.

---

## 7. Found while investigating, not fixed here

**F1. The local API cannot tell callers apart.** The token file grants
`INTERACTIVE` (`api/acl_windows.go`), which includes every RDP session, and the
API authenticates the token, not the caller. Restore takes a destination path
from the request (`restore_service.go`, `DestPath`, and `RestoreModeOriginal`)
and the service writes there as SYSTEM (for volume-file restores, applying the
stored DACLs, `restore_acl_windows.go`). No check on the destination was found
in the restore path. On a multi-user machine, any logged-on user who can get a file into a backup can
ask the service to put it anywhere, including `C:\Windows\System32`. Org
`file_restore` policy gates it on managed machines; a standalone install
permits it. Not verified on a machine. This document makes the local API the
console's only path to anything, which makes this boundary load-bearing. Fix
direction for its own spec: a named pipe (`\\.\pipe\NimbusBackup`) with a DACL
for INTERACTIVE and Administrators, and per-request caller identity from
`GetNamedPipeClientProcessId` and the client's token, so the service can
require Administrators for restore-to-path and confine non-admin destinations
to the caller's own profile. It also gives the console the minimal transport
C4 wants, with no port on the loopback interface.

**Specified 2026-10-17:** `docs/V4-LOCAL-API.md` (CJ chose the named pipe,
caller identity, and an elevated Browse window behind an Elevate button).

**F2. Machine-scope DPAPI.** Once no user process needs the DEK (after C2), the
reason recorded in `secrets_windows.go` for machine scope is gone. The DEK file
can take the narrow ACL `keyacl_windows.go` gives `backup-key.json`, and the
protector question can be reopened. Its own change, after C2.

---

## 8. The work, in order

Each phase is one pull request into `v4_dev`. Spec, then tests, then code;
docs move with the code.

### C1. The service API completes the surface (additive)

Section 4.1's routes and fields, section 4.2's persisted registry. No console
change; nothing calls the new routes yet.

Tests first: one handler test per route in `api/` (method, auth, sanitized
output: a fixture server with a secret must not return it); `readonly_test.go`
extended so every new route is refused while locked; the registry round trip
(write, restart, read; a `running` record at start becomes interrupted); the
`space` op declared in the restore table, so `restore_ops_test.go`'s three
lists still agree.

### C2. Every binding becomes one API call (behavior change)

In place, still in `gui/` under `!service`. Every binding in section 3 is
rewritten to its "After" column. Deleted from the GUI's view:
`delegateConfigWrites`, every local fallback, `TestConnection` and
`pinFingerprintLocal`'s GUI path, `LoadConfig` at GUI start, the `config`
field from the GUI's use of `App`, the GUI's `ControlPlaneStatusMap` fallback,
`GetJobHistory` (the history table switches to `GetRecentRuns`; render snapshot
updated, nothing else about the table changes). The 31 unused bindings leave
the GUI's bound surface: the service's own entry points move behind the
`service` tag until C3 removes it, and the four tray methods and `GetHostname`,
which the GUI calls internally, become unexported.

This is the risky phase, and it is small: with the service stopped, every
write that used to succeed now returns the existing "service unavailable"
error, which today's screens already show. GUI audit S5 later replaces that
with the Service-down screen; C2 does not wait for it.

Tests first: a fake local API answering every route, and one test per binding
that it calls the expected route and nothing else; the GUI audit S8 test
(every `/jobs*` write refused while locked) moves here; the front-end render
snapshots for the history table.

Effect on size: partial. With nothing bound reaching them, the agent, the key
stores and the engines drop out of the link; what remains until C3 is whatever
the untagged files' imports drag in through package initializers (`pbscommon`'s
zstd decoder is one). The CI size step from section 5 lands here as a
print-only step, so C3's budget starts from a measured number.

### C3. The console module (structure)

`console/` with its own `go.mod`, `main.go` (window, flags, crash report,
single-instance lock), `bindings.go` (the `Console` type), `tray.go`,
`storage_tray_icon.go`, `icon.go`, `restore_bindings.go` (already thin; it keeps
translating polled restore state into the Wails events the screens listen
for), the picker (until V4-LOCAL-API P3), the few error codes it returns itself, and a small logger
(section 9). `git mv gui/frontend console/frontend`; `wails.json` and
`build/` move with it. `restore_types.go` and `restore_wire.go` (the shapes a
restore speaks in) move into `gui/api`, since both sides use them.

In `gui/`: the seven `!service` files are gone (moved or deleted, section 9);
the `service` tag is removed everywhere; `App` loses `ctx`, `mode`,
`isServiceProcess`, `apiClient` and `lastVolumeTruncated`; `api/mode.go`'s
`ModeDetector` and the modes go if the console's one reachability check
replaces them.

CI: the four checks of section 5 replace the engine file-set and `nm` probe
steps; the Build GUI job runs `wails build` in `console/`; Frontend Gate's
working directory moves; the WiX copy steps are unchanged (same output names).
Budget set to 11.2 MB.

Expected: console about 11.0 MB stripped (prototype 10,968,576; the real
console carries more binding code than the prototype). About 2.3 MB off the
GUI, 0.6 to 0.8 MB off the MSI (bracketed by gzip and xz; LZX lands between).

### C4. Transport and runtime (superseded)

`api.Client` stops using `http.DefaultTransport`: one request per connection,
`net.Dial` to the loopback port, `req.Write`, `http.ReadResponse`. About 30
lines, prototyped for this measurement. The service is unaffected (it serves
HTTP; it does not use the client). If D2 is (b), the runtime check moves into
the service and the console is built with `wv2runtime.error`.

Expected with D2 (b): console about 9.3 MB, 1.05 to 1.5 MB off the MSI in
total with C3. Budget lowered to 9.6 MB.

**Superseded 2026-10-17:** CJ chose the named pipe, so C4's loopback client is
not built. The transport and the runtime move are V4-LOCAL-API.md phase P1.

---

## 9. Every file the GUI compiles today, after

**Moves to `console/`** (rewritten where marked):

| File | Note |
|---|---|
| `main.go` | rewritten: window, flags, crash report, startup; no `pbscommon`, `security` or config |
| `gui_options.go` | binds `Console`, not `App` |
| `restore_bindings.go` | on `Console`; `security.ValidatePath` calls dropped (the service validates) |
| `controlplane_policy_gui.go` | `IsReadOnly` and the cached policy read |
| `tray.go`, `storage_tray_icon.go`, `icon.go` | the service stops linking systray, Wails and `image/*` |
| `pathpicker.go` | until V4-LOCAL-API P3, which moves it to the service (D1) |
| `version.go` | the console's own stamp |

**Split:**

| File | Console side | Service side |
|---|---|---|
| `app_types.go` | new `Console` type | `App` without the console fields |
| `errcodes.go` | the codes the console returns itself (`errServiceUnavailable`, `errServiceComm`, the restore argument checks) | the rest |
| `restore_types.go`, `restore_wire.go` | — | move to `gui/api` (shared shapes) |
| `storage_identity.go` | `GetStorageIdentityStatus`, `ApproveStorageIdentity` (API calls) | `StorageIdentityStatusMap`, `ApproveStorageIdentityFromMap` |
| `fileutil_core.go` | — | `evaluateSpace` serves the `space` op and the download block |
| `diskspace_windows.go` | `logicalDriveRoots`, `volumeLabel`, and its own copy of `driveSpace` (about 30 lines) for the picker, until V4-LOCAL-API P3 deletes them from the console | `driveSpace`, used by `evaluateSpace` |

**Deleted:** `announce_run_gui.go` (the no-op twin of `announce_run_service.go`),
`logging_variant_gui.go`, `download.go` (its check becomes the `space` op; its
dialog stub has no caller), `cleanup_legacy.go` and `cleanup_legacy_stub.go`
(D3), `tray_stub.go` (the console is Windows-only), and every `!windows` stub
whose only purpose was the GUI's Linux compile view.

**Stays in the service and leaves the console entirely** (no longer compiled
into any user-session binary): `admin_windows.go`, `agentkey_*.go`,
`api_wrappers.go`, `backup_inline.go`, `backup_meta*.go`, `backup_status.go`,
`backupkey_*.go`, `breakglass_windows.go`, `config.go`,
`credential_storage.go`, `controlplane_fsbrowse.go`, `controlplane_glue.go`,
`controlplane_pbspoll.go`, `controlplane_runlog.go`, `datapath_*.go`, `disk_inventory.go`,
`exchange*.go`, `image_snapshot_windows.go`, `keyacl_windows.go`,
`log_rotation.go`, `logcat.go`, `logging.go`, `logqueue.go`, `logredact.go`,
`logredirect.go`, `machine_backup_windows.go`, `managed_jobs.go`,
`org_suspended.go`, `pbs_credential*.go`, `pbs_server.go`, `pbslog_glue.go`,
`provisioning*.go`, `runreport_decide.go`, `scheduler.go`, `secposture*.go`,
`secrets*.go`, `selfupdate.go`, `snapshot_bounds.go`, `storage_handle_windows.go`,
`storage_identity_parse.go`, `storage_inventory_*.go`, `tpm_windows.go`,
`volume_mount_paths.go`.

**The console's logging.** The service's logging module stays the service's (it
pushes WARN/ERROR to the control plane, which a user process must not do). The
console writes a size-capped file under `%LOCALAPPDATA%\NimbusBackup\` with its
own lifecycle and failed API calls. It holds no secrets to redact; it logs
server ids, never URLs.

---

## 10. Measurements, for the record

Built at `835da16`, `go1.26.9`, `GOOS=windows`, `CGO_ENABLED=0`, `-trimpath`.

| Binary | Stripped bytes | gzip -9 | xz -9 |
|---|---|---|---|
| GUI today (local) | 13,268,992 | 5,297,914 | 4,123,024 |
| GUI as shipped, dev.244 | 13,459,968 | | |
| Prototype console, as C3 | 10,968,576 | 4,461,674 | 3,553,092 |
| Prototype console, as C4 with D2 (b) | 9,318,400 | 3,827,108 | 3,070,664 |
| Service today (local) | 11,464,192 | | |

Code (`T` symbols) in the GUI today: 5,280 KB; in the C3 prototype: 4,308 KB.
The largest packages present in the GUI and absent from the console:
`golang.org/x/net/http2` 121 KB, `controlplane` 81 KB, `klauspost/compress/zstd`
63 KB plus `huff0` and `fse`, `image/png` 34 KB, `x/text/unicode/norm` 28 KB,
`x/crypto/blake2b` 23 KB, the extra `crypto/ecdsa`, `rsa` and `x509` signing
paths (about 100 KB together), and 228 KB of our own `package main`.

The honest summary: most of the console's size is Wails, the Go runtime and
`net/http`. Cutting the functional code takes about 2.3 MB; the remaining
1.6 MB of decision D2 is HTTP and TLS machinery. The size was the right
telltale. The larger gain is that a process running as the logged-on user stops
holding the agent, the keys and the engines.
