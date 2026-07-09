# Nimbus Backup — Architecture & Developer Guide

> Orientation document for a developer (or AI session) picking up this codebase.
> It describes the module layout, the two-process model, the build-tag rules that
> govern what compiles where, the control-plane API the future control server will
> use, the security model, and planned work. For end-user docs see `README.md`;
> for a feature status matrix see `FEATURES_STATUS.md`.

Current version: see `gui/wails.json` → `productVersion` (0.2.128 at time of
writing). Upstream lineage: fork of `tizbac/proxmoxbackupclient_go`.

---

## 1. What it is

A Windows backup client for **Proxmox Backup Server (PBS)** with a Wails v2
(Go + React) GUI and a Windows **service**. It backs up files/folders
("directory" mode) and full disks ("machine" mode, a bootable FIDX image via a
VSS snapshot), browses and restores snapshots, supports multiple PBS targets,
and runs scheduled jobs from the service.

Two processes, one binary:

- **GUI process** — the Wails desktop app the user interacts with. Runs in the
  user's session, unprivileged. Build tag: `!service`.
- **Service process** — `NimbusBackup` Windows service, runs as **LocalSystem**.
  Owns scheduling, privileged VSS snapshots, and is the **single writer of
  `config.json`**. Build tag: `service`.

They communicate over an authenticated loopback HTTP API (see §5). The same
compiled packages back both; which `main`/entrypoint you get is selected by the
`service` build tag.

---

## 2. Module layout (Go workspace)

`go.work` composes several modules:

| Module | Role |
|---|---|
| `gui/` | The Wails app **and** the service (same package `main`, split by build tag). Frontend in `gui/frontend/` (React/Vite). Also contains the local API package `gui/api/`. |
| `pbscommon/` | PBS protocol client: HTTP/2 backup/reader sessions, fixed/dynamic index (FIDX/DIDX), chunking (buzhash), PXAR archive read/write, catalog. The wire layer. |
| `snapshot/` | VSS snapshot creation (Windows) via `st-matskevich/go-vss`. `nop_snapshot.go` for non-Windows. |
| `machinebackup/`, `directorybackup/` | Older standalone CLI backup paths. The GUI's own inline implementations (`gui/backup_inline.go`, `gui/machine_backup_windows.go`) are what the app actually runs. |
| `nbd/` | Network Block Device tooling for mounting/restoring machine images. |
| `clientcommon/`, `pkg/` | Shared helpers. |

> The GUI deliberately reimplements backup inline (`gui/*_inline.go`,
> `gui/machine_backup_windows.go`) rather than calling the CLI modules, so it can
> stream progress/stats through callbacks. When changing backup behavior, edit
> the `gui/` implementations.

---

## 3. Build tags — the single most important thing to internalize

CI has failed repeatedly on this, so read carefully.

Files in `gui/` fall into three compile scopes:

1. **Shared** (`package main`, no build constraint) — compiled into **both** the
   GUI and service builds. Examples: `config.go`, `api_wrappers.go`,
   `secrets.go`, `exchange.go`, `errcodes.go`, `logcat.go`, `alerts.go`,
   `backup_inline.go`, `app_types.go`.
2. **`//go:build !service`** — GUI build only. `main.go` (Wails entrypoint,
   Wails-bound `App` methods, GUI-side service delegation).
3. **`//go:build service`** — service build only. `service_main.go`,
   `app_service_stubs.go`.

Orthogonally, **`//go:build windows`** vs **`//go:build !windows`** split
platform code (e.g. `secrets_windows.go` / `secrets_nonwindows.go`,
`tpm_windows.go`, `exchange_windows.go`, `machine_backup_windows.go`).

### The lint trap

`gui/.golangci.yml` runs **on Linux, `GOOS=linux`, with no build tags set**.
Consequences:

- The linter compiles the **`!service`** (GUI) view and the **`!windows`** view.
- **`//go:build windows` files are never seen by the linter** (only by the
  Windows build/CI `go build`). A `windows`-only function used only by other
  `windows` code is therefore safe from the `unused` linter.
- **A `windows`-only caller does NOT satisfy `unused` for a symbol declared in a
  shared file.** If you add a shared/exported helper, it needs a caller that
  compiles under Linux (shared or `!windows`). This is the exact failure that
  bit `writeCatLog` (its only caller was in `machine_backup_windows.go`); it was
  fixed by also calling it from `backup_inline.go` (shared).

Enabled linters: `errcheck`, `govet`, `ineffassign`, `staticcheck` (all checks:
ST1005 error-string style, QF quickfixes, SA), `unused`. `gosec` runs as a
separate step at `-severity high -confidence high` (G304/G204/G703 are the ones
we hit; mirror existing `#nosec`/`_ =`/`exec.Command` patterns).

### Local pre-push checklist (avoids the CI round-trips)

- `gofmt -e` every touched file; `gofmt -w` to auto-fix drift.
- Every new shared/exported func has a Linux-visible caller.
- `errcheck`: wrap deferred `Close`/`Remove` as `defer func(){ _ = x() }()`;
  handle `CombinedOutput`/`Marshal`/`Unmarshal` errors.
- `staticcheck` ST1005: `fmt.Errorf`/`errors.New` strings lowercase, no trailing
  punctuation. (Log strings via `writeDebugLog` are exempt — they're not errors.)
- Frontend: `esbuild src/App.jsx --jsx=automatic` to parse-check; keep the FR/EN/ES
  translation blocks at equal key counts.

The sandbox can't reach `golang.org` (module proxy blocked), so a full `go build`
/ `staticcheck` run isn't possible there; the checklist above plus `gofmt -e` is
the validation path.

---

## 4. Process model & config flow

`config.json` lives at `C:\ProgramData\NimbusBackup\config.json`. Because
ProgramData files are owned by whichever identity wrote them first, an
unprivileged GUI cannot overwrite a service-owned file. Therefore:

**All config writes flow through the service** (Phase 1). GUI-side `App` methods
(`AddPBSServer`, `UpdatePBSServer`, `DeletePBSServer`, `SetDefaultPBSServer`,
`SaveConfig` in `main.go`) call `delegateConfigWrites()`; when a service is
present they POST to the local API and the **service** performs the write via
the `BackupHandler` methods in `api_wrappers.go`. Standalone GUIs (no service)
write directly. Key helpers: `main.go:delegateConfigWrites()`, `toMap()`;
service side: `api_wrappers.go:SavePBSServerFromMap` etc.

Progress/stats flow: the backup engine invokes `OnProgress`/`OnStats`/
`OnComplete` callbacks. In service mode these update a per-job progress map
(`gui/api/server.go`), which the GUI polls via `/backup/status/{id}` and
re-emits as Wails events (`backup:progress`, `backup:stats`, `backup:complete`).
Percent is a 0..1 fraction internally, scaled to 0–100 at the API boundary.
Callback registration is shared: `api_wrappers.go:SetProgressCallbacks` +
`notifyProgress/Stats/CompleteCallbacks`.

---

## 5. Control-plane API — what a control server ties into

Authenticated loopback server: `127.0.0.1:18765`, package `gui/api/`
(`server.go`, `client.go`, `auth.go`, `types.go`, `mode.go`). Auth is a shared
token (`X-Nimbus-Token`, constant-time compare) in a file whose DACL is
restricted to SYSTEM/Administrators/INTERACTIVE (`acl_windows.go`). The
`BackupHandler` interface (`server.go`) is the contract the service implements.

Current routes:

| Route | Purpose |
|---|---|
| `GET /status` | Service status + sanitized config snapshot |
| `POST /backup` | Start a backup (registers progress callbacks) |
| `GET /backup/status/{id}` | Poll progress/stats for a job |
| `GET/POST /jobs`, `/jobs/create`, `/jobs/update`, `/jobs/delete/{id}` | Scheduled job CRUD |
| `POST /pbs/save`, `/pbs/delete/{id}`, `/pbs/default` | PBS server CRUD + default |
| `POST /pbs/fingerprint` | Pin a TOFU certificate fingerprint |
| `POST /config/save` | Persist full config |

**Control server integration (implemented, v0.2.129+):** the design landed
inverted from the sketch above — the agent dials OUT to the NimbusControl
server (CGNAT/Starlink-safe); the server never connects in. The `controlplane/`
module (stdlib-only, unit-tested) implements enrollment (one-time token → per-
agent secret, sealed by the Phase 2/3 secret store), a server-cadenced check-in
loop (inventory → missed-backup expectations; command drain — `run_backup`;
resolved policy set, fail-closed), and forward-only run reporting with
VSS-confirmed phases (`preparing`/`running`/`vss_failed`/terminal + PBS
snapshot triple). Glue lives in `gui/controlplane_glue.go`; the loop runs in
the SERVICE (`NimbusService.run`) or in a standalone GUI. The GUI reads
connectivity via the local API (`/controlplane/status`) and writes settings
via `/controlplane/save` (single-writer rule). Wire contract:
NimbusControl repo `docs/AGENT-API.md`; client-side notes:
`docs/CONTROL-PLANE.md`.

### Config flags a control server can read/toggle

Exposed in `GetConfigWithHostname()` (sanitized; secrets are never returned) and
persisted in `config.json`:

| Flag | Meaning |
|---|---|
| `upload_limit_mbps` | Upload bandwidth cap (token bucket on the PBS TLS socket) |
| `exchange_aware` | Run app-aware Exchange post-backup health tasks |
| `exchange_log_truncation` | Truncate Exchange transaction logs after a successful backup |
| `usevss` | Use VSS shadow copies |
| `control_server_url`, `control_cert_fp` | NimbusControl attachment (agent id + sealed secret are managed automatically; enroll token is one-time and wiped) |

> **SMTP alerting was removed in 0.2.130** — failure/missed/VSS alerting is a
> control-server responsibility now (`alerts.go` deleted; `smtp_*`/`alert_email`
> config keys are ignored if present in old config.json files).

Read-only posture/detection surfaced to the GUI and available to a control
server: `GetSecurityWarnings()` (CPU speculation-control / Windows Update
staleness), `GetExchangeStatus()` (installed/version/aware/highlight),
`QueryExchangeLogMode()` (circular-logging state).

---

## 6. Security model (zero-trust posture)

- **Config writes: service-only** (Phase 1) — single privileged writer.
- **Secrets at rest** (Phase 2/3) — `secrets.go`. PBS token secrets and the SMTP
  password are AES-256-GCM sealed under a random DEK (`encv1:` prefix). The DEK
  is wrapped by a protector chain, strongest-that-works, verified by a
  round-trip at creation: **TPM** (`tpm_windows.go`, ncrypt Platform Crypto
  Provider, RSA-2048) → **DPAPI** machine scope (`secrets_windows.go`) →
  **plaintext** (loudly logged fallback; non-Windows or no DPAPI). Existing keys
  auto-upgrade to a stronger protector on load (DEK re-wrap only; secret format
  never changes). Decrypt failure ⇒ empty secret + "re-enter", never a crash.
  `master.key` sits beside `config.json`.
- **Local API auth** — shared token, constant-time compare, ACL-restricted token
  file (`auth.go`, `acl_windows.go`). Per-application ACLs don't exist on Windows
  (DACLs bind to principals, not binaries); well-known SIDs are the correct,
  domain-independent construct.
- **Input safety** — all API inputs cross a typed JSON boundary; no shell/SQL
  injection surface. `exec.Command` is used with fixed command names.
- **Residual risk** — local malware on an already-compromised host can request
  DPAPI/TPM unwrap in-process; the posture warnings make hardware-mitigation
  gaps visible but userland cannot fully close this.

---

## 7. Backup internals (quick map)

- **Directory mode** — `backup_inline.go` streams a PXAR archive, DIDX dynamic
  chunking with dedup, junction/locked-file skip with reporting, optional
  auto-split for large first backups (`backup_split_api.go`, `backup_analysis.go`).
- **Machine mode** — `machine_backup_windows.go`. Per physical disk: enumerate
  partitions, VSS-snapshot mounted volumes, stream raw + snapshot regions into a
  fixed-size (4 MB) chunk pipeline → FIDX. 8 hasher/upload workers; channels are
  buffered to decouple the disk reader. Reader failures abort via a `readerErr`
  channel **before** the index is closed (prevents a truncated image being
  committed). A per-destination `TryLock` prevents concurrent sessions to the
  same backup group.
- **VSS** — `snapshot/win_snapshot.go` via go-vss: `SetBackupState(false,
  bootable, VSS_BT_COPY, false)` — **copy-only, component-less, writers
  participate**. App-consistent (SQL/Exchange writers freeze during
  `DoSnapshotSet`) but **does not truncate logs** (by design, so it doesn't
  disturb other backup products). This is why Exchange log truncation is a
  separate opt-in (§8).
- **Restore** — `restore_inline.go` (+ `restore_search.go`, `restore_cache.go`):
  snapshot tree browse, selective restore, metadata sidecar
  (`.nimbus_backup_meta.json`) for in-place restore with cross-host guard.
- **Bandwidth limit** — `pbscommon/pbsapi.go:rateLimitedConn` token bucket on the
  session TLS socket; writes ≤1 KB bypass it so HTTP/2 control frames aren't
  starved.
- **Alerts** — `alerts.go`: on failure, email with tails of the newest
  `service-*.log` / `backup-*.log` (64 KB window), STARTTLS/implicit-TLS, certs
  verified, best-effort/async. The `[NB-2004]` already-running rejection is
  excluded from alerting.

---

## 8. Exchange (application-aware)

- **Detection** (`exchange_windows.go:detectExchange`) — registry hive
  `HKLM\SOFTWARE\Microsoft\ExchangeServer\vN\Setup`; all versions
  (v8=2007, v14=2010, v15 refined to 2013/2016/2019 via `MsiProductMinor`).
- **`exchange_aware`** — runs post-backup health probe (EMS
  `Get-MailboxDatabase -Status`) on **success only**.
- **`exchange_log_truncation`** — because the snapshot is copy-only, logs on
  non-circular databases accumulate until the volume fills. When enabled,
  truncation is done the **supported** way: a diskshadow writer-participating
  full backup whose `end backup` makes the Exchange writer truncate committed
  logs (`runExchangeLogTruncation`). Volatile shadow, discarded — only the
  truncation side effect is wanted. **Never** manual `.log` deletion.
- **`QueryExchangeLogMode()`** — lazy EMS query of circular-logging state; the
  GUI highlights the truncation toggle only when logs actually accumulate.
- Every Exchange command's outcome is logged with exit code
  (`runExchangeCommand`).

> Validate diskshadow + EMS behavior on real Exchange hardware before fleet use —
> these paths are compile/lint-verified but not runnable in CI.

---

## 9. Internationalization

- Frontend: `gui/frontend/src/i18n/translations.js` — three locales **fr / en /
  es** at **full key parity** (parity is a hard invariant; keep counts equal —
  currently 302/302/302). Switcher: one of three `Dropdown` instances in
  `components/HeaderControls.jsx` (see #9b — Theme/Font Size/Language are the
  same component, one row, by design). Context/fallback: `i18n/i18nContext.jsx`.
- **No hardcoded strings in JSX bypassing `t()`** — this broke once already
  (the multi-server list table, its status cells, and its Save/Update/Cancel
  buttons were hardcoded French and did not respond to the language selector
  at all). Any new user-facing string in `App.jsx` must go through `t()` in
  all three languages before merge.
- Backend errors: all user-facing Go errors are canonical `[NB-xxxx]` codes
  (`errcodes.go`): `1xxx` config, `2xxx` backup, `3xxx` restore/search. Logs
  record the code + a stable English base (grep-able, language-independent). The
  GUI's `localizeMessage()` swaps the base for the active language via
  `err_NBxxxx` translation keys, preserving any ` :: <detail>` suffix. **No
  hardcoded French remains in Go** — new user-facing errors must use a code.

---

## 9b. Theming, font size, and accessibility

- **Design language:** Proxmox-flavored — dense bordered panels, dark app
  bar, 3px corner radius, no external fonts/CDNs. Full token system lives in
  `gui/frontend/src/index.css`; conceptually shared with the NimbusControl
  portal for a consistent look across both surfaces.
- **Base theme + accent (two orthogonal axes)**: `data-theme` = light/dark/
  (absent = auto/OS) sets structural colors; `data-accent` = orange(default,
  absent)/pink/forest/sky overlays ONLY the accent-family variables. Each
  accent ships two shades — brighter on light base, darker/muted on dark base
  — so a dark-mode user is never blinded by a saturated accent. Default accent
  is **Proxmox orange** (`#e57000`); choosing the orange swatch removes the
  attribute. Base mode lives in the Theme dropdown; accents are a swatch row
  in that dropdown's footer. Persisted: `localStorage['nimbus.theme']` +
  `['nimbus.accent']`. **Do not turn accents back into full standalone
  palettes** — that blinded the user once (dark-mode fanatic + saturated
  light accents).
- **Three font sizes** — small (baseline)/medium/large — applied via CSS
  `zoom` on `<html>` per `data-fontsize` (WebView2/Chromium-only technique;
  this app never runs anywhere else, so it's safe and avoids a risky
  px-to-rem rewrite of the whole stylesheet). Persisted to
  `localStorage['nimbus.fontsize']`.
- **OpenDyslexic** (OFL-1.1, via `@fontsource/opendyslexic`) is the global
  font at **all three** sizes, not just large. Bundled at build time
  (`main.jsx` imports `400.css`/`700.css`); zero runtime network dependency,
  ships inside the MSI.
- **Zero flash of wrong theme/size**: `index.html` has a small inline
  `<script>` in `<head>` that reads both localStorage keys and sets the
  `data-*` attributes on `<html>` before first paint, before React mounts.
  `components/HeaderControls.jsx` re-applies on mount (keeps React state and
  the DOM attribute in sync) and owns every subsequent change.
- **One control type for all three selectors**: `components/Dropdown.jsx` is
  a themed custom listbox (not native `<select>` — can't be styled/animated
  cross-platform) used identically for Theme, Font Size, and Language,
  rendered side by side in `.header-controls` in normal document flow. The
  font-size trigger shows a literally-sized "A" glyph per option instead of
  text. **Do not reintroduce `position: absolute` controls in the header** —
  that caused a real overlap bug (theme toggle drawn on top of the language
  switcher) once already.
- **`.tab-content` visibility is load-bearing CSS**: the four top tabs
  (Servers/Backup/Browse/About) render all panes unconditionally in JSX and
  rely entirely on `.tab-content{display:none} .tab-content.active{display:
  block}` in `index.css` to show only the active one. This rule was
  accidentally dropped during a theme rewrite once (all four tabs rendered
  stacked on one page, buttons looked inert) — if the tabs ever appear to
  "do nothing" again, check this rule first, before the click handlers.

---

## 10. Logging

- Writers: `logging_gui.go`, `logging_service.go`, rotation in
  `log_rotation.go`. Files under `C:\ProgramData\NimbusBackup\`
  (`service-*.log`, `backup-*.log`). `writeDebugLog` / `writeBackupLog`.
- **Log categories** (`logcat.go`) — launch flag `-logcat pbs,chunks,security,api`
  (or `all`) enables verbose per-category lines via `writeCatLog(catX, ...)`.
  Default is quiet (keeps log volume bounded; a 931 GB machine backup is ~240k
  chunks). Verbose call sites move onto `writeCatLog` incrementally.

---

## 11. Build & release

- `wails.json` `productVersion` is the release version (bump per release).
- CI (`.github/workflows/build-and-release.yml`) triggers on `v*` tags: jobs
  check-deps → security (gosec + golangci-lint) → test → build-cli/build-gui →
  release (gated on `refs/tags/v*`). A lint failure skips everything downstream.
- MSI via WiX (`installer/wix/`, `MSI_BUILD_GUIDE.md`). Local build scripts:
  `build_gui.bat`/`.sh`, `build_cli.bat`, `Makefile`.
- Release flow: bump `wails.json`, commit, tag `vX.Y.Z`, push tag.

---

## 12. Planned / backlog work

Delivered through 0.2.128: service-only config writes, live service-mode
progress/stats, upload rate limit, DPAPI+TPM secret encryption, failure-alert
emails, security-posture warnings, token-file ACL, log categories, full i18n
(fr/en/es) with coded errors, application-aware Exchange (detection, health,
log truncation).

Open items:

- **Control server (Phase 4 proper)** — remote transport + mutual-auth agent
  enrollment, pull-vs-push config, fleet dashboard, log shipping. The
  authenticated local API + service-as-brain is the substrate; needs a design
  pass before code. Start from `gui/api/` and `INTEGRATION.md`.
- **Multi-volume single-snapshot-set** — machine backup currently snapshots each
  volume in a separate VSS set (separate instants). A database split across
  volumes (e.g. SQL data on D:, logs on E:) is torn across volumes. Fix needs
  go-vss extended to `AddToSnapshotSet` all volumes before one `DoSnapshotSet`.
  Only bites split-volume layouts; single-volume is fine.
- **pbscommon stdout diagnostics** — the handshake trace prints to stdout
  (invisible in the service). Thread a logger into the shared package so it lands
  in `writeDebugLog`/`writeCatLog(catPBS)`.
- **Restore acceptance test** — machine-image restore via `nbd` map + boot-verify
  is the one milestone not yet exercised end-to-end.
- **CBT** — already handled for *upload* (chunk dedup); a read-time skip would
  need a signed kernel filter driver (out of scope).
- **Authenticode signing** — pending SignPath OSS certificate (see README).
- **`FEATURES_STATUS.md`** — historically drifts; keep it or fold into this doc.

---

## 13. Operational footnotes for a new session

- Secrets have appeared in prior development logs. Standing hygiene: rotate the
  PBS API token and any GitHub PAT used for pushes; never hardcode them.
- Windows-only code paths (VSS, DPAPI, TPM/ncrypt, registry, diskshadow, icacls,
  `NtQuerySystemInformation`) compile- and lint-check in CI but need real Windows
  (and, for Exchange, a real Exchange host) to validate behavior.
- When in doubt about where code compiles, re-read §3 before pushing.
