# NimbusBackupClient — Architecture

Windows backup agent for **Proxmox Backup Server (PBS)**: a Wails v2 (Go +
React) GUI plus a Windows service, one binary, controlled at fleet scale by
the **NimbusControl** server (separate repo). Upstream lineage: fork of
`tizbac/proxmoxbackupclient_go`.

**North star:** the industry-leading zero-trust Windows backup agent.
Every design decision below serves that in priority order:
**data safety → security → performance → polish.** When two sections of this
document appear to conflict, the earlier priority wins. For a backup client
the first priority is literal: a backup that silently commits torn or
truncated data is worse than one that fails loudly.

This document has three parts: the **system design** (what the thing is), the
**development rules** (laws every change must obey), and the **roadmap**
(phases from the current state to beta, including the CI smoke-test ledger).

Current version: see `gui/wails.json` → `productVersion` (0.2.150 at time of
writing). This structure mirrors NimbusControl's `ARCHITECTURE.md`; the two
documents are siblings and the agent↔server wire contract lives in the
NimbusControl repo (`docs/AGENT-API.md`), with client-side notes in
`docs/CONTROL-PLANE.md`.

---

# Part I — System design

## What it is

Two processes, one compiled codebase:

* **GUI process** — the Wails desktop app. Runs in the user's session,
  unprivileged. Build tag: `!service`.
* **Service process** — `NimbusBackup` Windows service, runs as
  **LocalSystem**. Owns scheduling, privileged VSS snapshots, the
  control-plane loop, and is the **single writer of `config.json`**.
  Build tag: `service`.

They communicate over an authenticated loopback HTTP API. Which
`main`/entrypoint you get is selected by the `service` build tag; the same
packages back both.

Backup modes: files/folders ("directory" mode, PXAR + DIDX) and full disks
("machine" mode, a bootable raw image via VSS into FIDX). Browse and restore
for both, multiple PBS targets, scheduled jobs from the service, outbound-only
attachment to NimbusControl.

## Module layout (Go workspace)

`go.work` composes the modules; `gui/go.mod` `replace` directives bind them:

| Module | Role |
|---|---|
| `gui/` | The Wails app **and** the service (same package `main`, split by build tag). Frontend in `gui/frontend/` (React/Vite). Local API package in `gui/api/`. |
| `pbscommon/` | PBS protocol client: HTTP/2 backup/reader sessions, fixed/dynamic index (FIDX/DIDX), chunking (buzhash), PXAR read/write, catalog, `FIDXReaderAt`/`DIDXReaderAt` lazy chunk readers, upload rate limiting. The wire layer. |
| `snapshot/` | VSS snapshot creation (Windows) via `st-matskevich/go-vss`; `nop_snapshot.go` for non-Windows; diagnostics via the package-level `LogFn` hook (declared platform-neutral — see rule 3). |
| `imagebrowse/` | Userspace file browsing INSIDE image backups: GPT/MBR parsing + NTFS/FAT/exFAT read-only filesystems behind one `Filesystem` interface over an `io.ReaderAt`. Real mkfs-made images in `testdata/`. ReFS + BitLocker detected and refused with reasons. |
| `controlplane/` | NimbusControl attachment: enrollment, check-in loop, command drain, forward-only run reporting. **Stdlib-only by law** (rule 10). |
| `machinebackup/`, `directorybackup/` | Older standalone CLI paths. The GUI's inline implementations (`gui/backup_inline.go`, `gui/machine_backup_windows.go`) are what the app actually runs — when changing backup behavior, edit `gui/`. |
| `nbd/` | Linux-only Network Block Device tooling for mounting/restoring machine images. |
| `clientcommon/`, `pkg/` (`retry`, `security`, `logger`) | Shared helpers. |

## Build-tag compile scopes — the single most important thing to internalize

CI has failed repeatedly on this. Files in `gui/` fall into three scopes:

1. **Shared** (`package main`, no constraint) — compiled into **both** GUI and
   service builds (`config.go`, `api_wrappers.go`, `secrets.go`,
   `backup_inline.go`, `errcodes.go`, `pbslog_glue.go`, …).
2. **`//go:build !service`** — GUI only (`main.go`: Wails entrypoint, bound
   `App` methods, GUI-side delegation).
3. **`//go:build service`** — service only (`service_main.go`,
   `app_service_stubs.go`).

Orthogonally, `//go:build windows` vs `!windows` split platform code
(`secrets_windows.go`/`secrets_nonwindows.go`, `tpm_windows.go`,
`exchange_windows.go`, `machine_backup_windows.go`, …). That gives **three
real compile views**: linux+GUI (what the linter and `go test` see),
windows+GUI (the shipped app), and windows+service (the shipped service).
linux+service is not a view — `service.go` is `windows`-only and `main.go` is
`!service`, so it has neither `NimbusService` nor a `main()` and could never
be built or shipped. A symbol referenced from a shared file must exist in
every REAL view — see rule 3 and smoke S2, both born from real breakage.

### The lint trap

`gui/.golangci.yml` runs on Linux, `GOOS=linux`, no tags. Consequences:

* The linter sees the **`!service` + `!windows`** view only. `windows`-gated
  files are never linted (only compiled by the Windows build job).
* A `windows`-only caller does **not** satisfy `unused` for a symbol declared
  in a shared file. New shared/exported helpers need a Linux-visible caller
  (this bit `writeCatLog`; fixed by also calling it from `backup_inline.go`).
* Enabled: `errcheck`, `govet`, `ineffassign`, `staticcheck` (all checks incl.
  ST1005 error-string style), `unused`. `gosec` runs separately at
  `-severity high -confidence high` (G304/G204/G703 are the recurring ones;
  mirror existing `#nosec`/`_ =` patterns).

## Process model & config flow

`config.json` lives at `C:\ProgramData\NimbusBackup\config.json`. ProgramData
files are owned by whoever wrote them first, so an unprivileged GUI cannot
overwrite a service-owned file. Therefore **all config writes flow through the
service**: GUI `App` methods call `delegateConfigWrites()`; with a service
present they POST to the local API and the service performs the write
(`api_wrappers.go:SavePBSServerFromMap` etc.). Standalone GUIs (no service)
write directly. The control-plane settings follow the same path
(`/controlplane/save`).

Progress/stats: the engine invokes `OnProgress`/`OnStats`/`OnComplete`
callbacks; in service mode these fill a per-job progress map (`gui/api/
server.go`) which the GUI polls (`/backup/status/{id}`) and re-emits as Wails
events. Percent is 0..1 internally, 0–100 at the API boundary.

## Local API — the only cross-process boundary

Authenticated loopback server `127.0.0.1:18765`, package `gui/api/`. Auth is
a shared token (`X-Nimbus-Token`, constant-time compare) in a file whose DACL
is restricted to SYSTEM/Administrators/INTERACTIVE (`acl_windows.go` —
per-application ACLs don't exist on Windows; well-known SIDs are the correct
domain-independent construct). The `BackupHandler` interface (`server.go`) is
the contract the service implements.

| Route | Purpose |
|---|---|
| `GET /status` | Service status + sanitized config snapshot |
| `POST /backup`, `GET /backup/status/{id}` | Start a backup; poll progress/stats |
| `GET/POST /jobs`, `/jobs/create`, `/jobs/update`, `/jobs/delete/{id}` | Scheduled job CRUD |
| `POST /pbs/save`, `/pbs/delete/{id}`, `/pbs/default`, `/pbs/fingerprint` | PBS server CRUD, default, TOFU pin |
| `POST /config/save` | Persist full config |
| `GET /controlplane/status`, `POST /controlplane/save` | NimbusControl attachment state / settings |

All inputs cross a typed JSON boundary; `exec.Command` uses fixed command
names; no shell/SQL surface.

## NimbusControl attachment (control plane)

The agent **dials out only** (CGNAT/Starlink-safe); the server never connects
in. `controlplane/` implements: enrollment (one-time org token → per-agent
32-byte secret, sealed by the secret store below), a server-cadenced check-in
loop (inventory up; command drain — `run_backup`; resolved policy set,
**fail-closed**), and forward-only run reporting with VSS-confirmed phases
(`preparing`/`running`/`vss_failed`/terminal + the PBS snapshot triple). Glue:
`gui/controlplane_glue.go`; the loop runs in the SERVICE (or a standalone
GUI). Alerting (failure/missed/VSS) is a **server** responsibility — SMTP was
removed from the client in 0.2.130 (`smtp_*`/`alert_email` keys are ignored).

Config flags a control server reads/toggles (sanitized — secrets never
returned): `upload_limit_mbps`, `usevss`, `exchange_aware`,
`exchange_log_truncation`, `control_server_url`, `control_cert_fp`. Read-only
posture surfaced: `GetSecurityWarnings()` (CPU speculation-control / Windows
Update staleness), `GetExchangeStatus()`, `QueryExchangeLogMode()`.

## Security model (zero-trust posture)

* **Single privileged writer** — config writes are service-only.
* **Secrets at rest** — `secrets.go`: PBS token secrets AES-256-GCM sealed
  under a random DEK (`encv1:`). DEK wrapped by a protector chain,
  strongest-that-works, round-trip-verified at creation: **TPM**
  (`tpm_windows.go`, ncrypt PCP, RSA-2048) → **DPAPI machine scope** →
  **plaintext** (loudly logged fallback). Keys auto-upgrade to a stronger
  protector on load (DEK re-wrap only). Decrypt failure ⇒ empty secret +
  "re-enter", never a crash. `master.key` sits beside `config.json`.
* **Local API auth** — shared token, constant-time compare, DACL'd token file.
* **Enrollment secrecy** — the enroll token is one-time and wiped; the agent
  secret lives in the same sealed store; the server stores sha256 only.
* **Untrusted path data** — PXAR entry names and filenames parsed out of a
  backup image never reach `filepath.Join` directly; `security.SafeJoin` /
  `SafeBaseName` screen them syntactically and then prove containment, so a
  crafted or corrupted image cannot write outside the restore destination.
  This matters most in the service, which restores as LocalSystem.
* **Residual risk** — malware on an already-compromised host can request
  DPAPI/TPM unwrap in-process; posture warnings make hardware-mitigation gaps
  visible but userland cannot fully close this.

## Backup engine

* **Directory mode** — `backup_inline.go`: streams PXAR, DIDX dynamic chunking
  with dedup, junction/locked-file skip with reporting, optional auto-split
  for large first backups (`backup_split_api.go`, `backup_analysis.go`).
* **Machine mode** — `machine_backup_windows.go`: per physical disk, enumerate
  partitions, VSS-snapshot mounted volumes, stream raw + snapshot regions into
  a fixed 4 MB chunk pipeline → FIDX. 8 hasher/upload workers over a **128 MB
  elastic pipeline**; one **shared zstd encoder** across workers (per-chunk
  encoder construction was the dominant upload-path CPU/GC waste). Reader
  failures abort via `readerErr` **before** the index closes — a truncated
  image is never committed. Per-destination `TryLock` prevents concurrent
  sessions on one backup group. ETA is byte-based over the whole run, padded
  ×1.15, rendered as `≥` (instantaneous rates lie under chunk-reuse bursts).
* **VSS** — `snapshot/win_snapshot.go`: `SetBackupState(false, bootable,
  VSS_BT_COPY, false)` — **copy-only, component-less, writers participate**.
  App-consistent (SQL/Exchange writers freeze during `DoSnapshotSet`) but does
  **not** truncate logs, by design, so it never disturbs other backup
  products' chains. Busy-shadow retry with one VSS service reset; per-writer
  last-error diagnostics; the full lifecycle (creation, shadow ID, device
  path, elapsed, failures with HRESULT) logs through `snapshot.LogFn` into the
  app debug log — in both processes.
* **Restore** — `restore_inline.go` (+ search/cache): snapshot tree browse,
  selective restore, metadata sidecar (`.nimbus_backup_meta.json`) with
  cross-host guard.
* **Bandwidth limit** — `pbscommon` `rateLimitedConn` token bucket on the
  session TLS socket; writes ≤1 KB bypass so HTTP/2 control frames aren't
  starved.
* **Exchange (application-aware)** — detection via the registry hive (2007→
  2019 incl. v15 minor refinement); `exchange_aware` runs a post-backup EMS
  health probe on success; `exchange_log_truncation` truncates the
  **supported** way — a diskshadow writer-participating full backup whose
  `end backup` makes the Exchange writer truncate committed logs; volatile
  shadow discarded. **Never** manual `.log` deletion. Every EMS/diskshadow
  command's outcome is logged with exit code.

## Browsing files inside image backups

Directory backups have a pxar catalog; image backups are raw disks (one
`*.img.fidx` per disk), so we parse the image ourselves:

* **`pbscommon.FIDXReaderAt`** — `io.ReaderAt` over a fixed index: index
  downloaded once, 4 MB chunks fetched on demand with an LRU, each verified by
  SHA-256. Only chunks a read touches are fetched.
* **`imagebrowse`** — GPT (authoritative) / MBR (fallback) parsing; per-
  partition boot-sector sniffing (NTFS / exFAT / FAT / BitLocker via the
  `-FVE-FS-` OEM id). NTFS via go-ntfs (pure Go, read-only). Output shaped as
  `SnapshotEntry` so the same Browse tree renders both backup types.
* **Full-tree listing is a sequential $MFT scan** (`TreeLister`, the WizTree
  technique): stream $MFT once, rebuild paths from parent references — moves
  ~the MFT's size instead of a large fraction of the volume. The fast path is
  tested to produce the identical tree to the generic walk.
* **Capability interfaces** on an opened filesystem: `TreeLister`, `Planner`
  (exact $MFT extent plan for prefetch), `StreamLister` (ADS),
  `SecurityReader` (SecurityId→$Secure/$SDS with legacy inline-0x50
  fallback). The GUI type-asserts; a missing capability is exactly what greys
  the matching restore option, with the reason shown.
* **One restore workflow** — options are best-effort per file AFTER data
  placement: mtimes, then ADS, then the security descriptor LAST (a
  restrictive DACL could lock us out of a file we still need to write streams
  onto). Metadata failures warn; the data is already safe.
* **Not a mount.** No WinFsp/Dokan, no driver, no admin — pure in-process
  byte parsing. (`nbd/` is the separate Linux-only restore path.)
* **ReFS is deliberately unsupported** — no mature pure-Go parser exists and
  guessing at undocumented structures in a restore tool risks returning
  corrupt files. ReFS and BitLocker are refused with actionable messages.
* Used vs allocated: allocated from the partition table; used from the
  filesystem ($Bitmap / FAT / exFAT bitmap), bounded, reported as unknown
  rather than guessed.
* Portal-delegated browsing: the service answers `image_partitions` /
  `image_scan` / `image_dir` / `image_extract` over the agent command channel
  (same core, same `file_restore` policy gate); extractions upload as ZIP
  artifacts, **data-only by design** (no ACLs/ADS in browser downloads).

## Why there are no native file dialogs

Wails native dialogs take an uncatchable **native COM fault** in this app: the
process dies outright — tray icon and all — no Go panic, nothing in logs,
nothing `recover()` can intercept, in BOTH processes. The picker is rendered
in the webview (`components/PathPicker.jsx`) over three plain Go methods
(`ListDrives`, `ListFolders`, `CreateFolder`) plus `DefaultSaveDir`. Pure Go +
DOM: no COM, no shell APIs, no way to fault the process. See rule 8.

## i18n & error codes

* Frontend: `gui/frontend/src/i18n/translations.js` — **fr / en / es at full
  key parity** (395 × 3 at time of writing), enforced mechanically by
  `npm run i18n-audit` (`scripts/i18n-audit.mjs`), which also fails on
  hardcoded strings in JSX and runs as `prebuild` before every frontend
  build. It found 66 hardcoded strings on first run.
* Backend: all user-facing Go errors are canonical `[NB-xxxx]` codes
  (`errcodes.go`): `1xxx` config, `2xxx` backup, `3xxx` restore/search. Logs
  record code + stable English base (grep-able, language-independent); the
  GUI's `localizeMessage()` swaps the base per `err_NBxxxx` keys, preserving
  any ` :: <detail>` suffix.

## Theming, font size, accessibility

Proxmox-flavored utilitarian UI, 3px radius, deliberately the **same
appearance model as the NimbusControl portal** so both surfaces match:

* `data-theme` = light|dark|(absent=auto/OS) — structural colors.
  `data-accent` = orange(default, absent)|pink|forest|sky — accent-family
  variables ONLY, two shades each (brighter on light, muted on dark) so dark
  mode is never blinded. Default accent Proxmox orange `#e57000`.
* `data-fontsize` = small|medium|large via CSS `zoom` on `<html>`
  (WebView2-only technique; this app runs nowhere else). **OpenDyslexic**
  (OFL-1.1, bundled via `@fontsource`) is the global font at all sizes — zero
  runtime network dependency, ships in the MSI.
* Zero flash of wrong theme/size: an inline `<head>` script applies both
  localStorage keys before first paint; `HeaderControls.jsx` owns changes.
* One control type for Theme/Font Size/Language: `components/Dropdown.jsx`,
  side by side in normal document flow.
* Token law, `.tab-content` law, and the header-positioning law are rules 7
  and 7a below — each earned by a real regression.

## Logging

Writers `logging_gui.go`/`logging_service.go`, rotation in `log_rotation.go`,
files under `C:\ProgramData\NimbusBackup\` (`service-*.log`, `backup-*.log`).
`writeDebugLog`/`writeBackupLog`; verbose per-category lines via
`writeCatLog` behind `-logcat pbs,chunks,security,api|all` (default quiet — a
931 GB machine backup is ~240k chunks; chunk logging is additionally throttled
to 1/256). `pbscommon.DebugLogFn` and `snapshot.LogFn` route shared-package
diagnostics into the same log so nothing prints into the void in a service.

## Build & release

* `wails.json` `productVersion` is the release version.
* CI (`.github/workflows/build-and-release.yml`) on `v*` tags and PRs:
  check-deps → **security** (gosec ×2 + golangci-lint) → **test** →
  build-cli (3 OS) / build-gui (Windows: Wails GUI + service build with a
  goversioninfo `.syso` so `NimbusBackupSVC.exe` carries proper version
  metadata — an AV-false-positive mitigation) → **release** (tag-gated:
  archives, SHA256SUMS, Sigstore build-provenance attestation, VirusTotal
  links gated on 0 detections, generated notes).
* MSI via WiX (`installer/wix/`), incl. the KEEP_CONFIG uninstall dialog.
* Release flow: bump `wails.json`, commit, tag `vX.Y.Z`, push tag. A failed
  release is fixed **on the same version**: commit the fix, force-move the
  tag (rule 17).

---

# Part II — Development rules

Non-negotiable laws. A change that violates one is wrong even if it works.
Where a rule has a war story, it is cited — these are not hypotheticals.

1. **Never commit a torn or truncated backup.** Reader failures abort the
   pipeline **before** the index closes; per-destination `TryLock` forbids
   concurrent sessions on a group; run reporting is forward-only with
   VSS-confirmed phases. A loud failure beats a quiet lie — data safety
   outranks everything else in this document.
2. **The service is the single writer of `config.json`.** GUI paths delegate
   through the local API when a service is present. New settings follow the
   delegation path or they don't ship.
3. **Every referenced symbol must exist in all three real compile views**
   (linux+GUI, windows+GUI, windows+service — linux+service is not a
   shippable configuration). Declarations shared code depends on live in
   platform-neutral files; `windows`-only files hold only `windows`-only
   symbols. This applies to test files too: a `runtime.GOOS` skip does not
   stop the compiler. Both directions have burned us: `snapshot.LogFn`
   declared in a `windows`-gated file broke the entire Linux CI leg at
   v0.2.150 (test AND security jobs — lint compiles too); `unused` fails
   shared symbols whose only caller is `windows`-gated (`writeCatLog`).
   Smoke S2 exists to catch this class pre-push.
4. **VSS snapshots are copy-only and writer-participating** —
   component-less, never log-truncating, so we never disturb another backup
   product's chain. Exchange log truncation is exclusively the supported
   diskshadow path; manual `.log` deletion is forbidden.
5. **User-facing errors are `[NB-xxxx]` codes.** Logs keep the stable English
   base; the GUI localizes. No hardcoded French (or any language) in Go —
   that cleanup is done and stays done.
6. **i18n parity is enforced, not promised.** fr/en/es at equal key counts;
   no strings in JSX bypassing `t()`; `i18n-audit` gates every frontend
   build. (The multi-server table once shipped hardcoded French and ignored
   the language selector entirely; the audit found 66 such strings.)
7. **Theming via tokens only; accents are overlays, not palettes.** A literal
   color in a component rule is a review rejection. Accents override only the
   accent-family variables with per-base shades (full standalone accent
   palettes blinded a dark-mode user once — do not reintroduce them).
   7a. **Load-bearing UI laws:** `.tab-content{display:none}` +
   `.active{display:block}` is what makes the top tabs work — it was dropped
   in a theme rewrite once and every pane rendered stacked; check it first if
   tabs "do nothing". No `position:absolute` controls in the header (caused a
   real overlap of theme toggle over language switcher).
8. **No native COM dialogs. Ever.** `wailsruntime.SaveFileDialog` /
   `OpenDirectoryDialog` kill the process with an uncatchable COM fault (both
   processes; observed repeatedly). The webview PathPicker is the only
   sanctioned picker.
9. **The local API is the only cross-process boundary.** Shared token,
   constant-time compare, DACL'd token file; typed JSON in; fixed
   `exec.Command` names; secrets never in responses.
10. **The agent dials out; nothing dials in.** Enrollment tokens are one-time
    and wiped; per-agent secrets live in the sealed store; policy resolution
    is fail-closed when the server is unreachable. `controlplane/` stays
    **stdlib-only** — it is the security-critical module and its supply chain
    stays empty. New dependencies anywhere require written justification in
    this file.
11. **Secrets are sealed or absent.** Everything sensitive at rest goes
    through the `encv1:` DEK chain (TPM → DPAPI → loud plaintext fallback),
    round-trip-verified, auto-upgrading. Decrypt failure means "re-enter",
    never a crash. Nothing secret in code, logs, or commits — a PAT and PBS
    credentials have leaked into logs before; rotation is standing hygiene,
    prevention is the law.
12. **The user picks the partition — always.** Auto-selecting the first NTFS
    volume put people inside WinRE. `ListImagePartitions` returns every
    partition with filesystem + sizes; `partIndex < 1` is an error, never a
    default.
13. **Space safety before bytes move.** Downloads/restores compute
    usage-after: BLOCK if it won't fit, WARN at ≥90%. Restore metadata is
    best-effort AFTER data placement — mtimes, then ADS, then the security
    descriptor LAST (a restrictive DACL could lock us out of a file we still
    need to write). An over-cap folder errors rather than silently partial.
14. **Image browsing is read-only forensics, and image paths are untrusted
    input.** No mounts, no drivers, no writes to the image, and no guessing:
    unsupported (ReFS) or locked (BitLocker) filesystems are refused with the
    reason. A restore tool that guesses at structures returns corrupt files
    with a straight face. Names taken from an image's own directory entries
    are attacker- or corruption-controlled — nothing in NTFS/FAT on disk
    forbids a separator or a `..` component, and `filepath.Join` RESOLVES
    `..` rather than rejecting it — so every join of backup-derived path data
    goes through `security.SafeJoin`, never `filepath.Join`.
15. **Every feature ships with tests, and CI runs them.** The smoke ledger
    (Part III, Phase 1) is law: a smoke test listed there is either green in
    CI or has an open phase item; "passes locally" is not a state. Red main
    is an incident. The class of bug a smoke would have caught, found in the
    field, adds that smoke in the fix commit.
16. **Windows-only behavior claims require Windows validation.**
    Compile-clean ≠ works: VSS, DPAPI/TPM, registry, diskshadow, icacls, and
    Exchange paths lint on Linux but prove nothing there. What can run on a
    `windows-latest` runner runs there (see smokes S6–S9); what needs real
    hardware (Exchange, TPM presence, boot-verify) is called out as a manual
    acceptance item, never silently assumed.
17. **Releases are tags, and a failed release keeps its number.** Artifacts
    come only from the tag workflow (attested, checksummed, VT-gated). When a
    tagged release fails CI, fix on the same version and force-move the tag —
    version numbers mark what shipped, not how many attempts it took.
18. **Docs move with code.** A change that alters behavior described in
    README / ARCHITECTURE / CONTROL-PLANE / the NimbusControl AGENT-API
    updates the doc in the same commit. This file's "current version" line
    and Phase 0 are part of that contract.

---

# Part III — Roadmap

Phases are strictly ordered; each phase's exit criteria gate the next. Every
item lands with rules 1–18 applied.

## Phase 0 — Current state (shipped, v0.2.150)

Verified by CI on every tag; delivered since the fork:

* Service-as-brain architecture: single-writer config, authenticated local
  API with DACL'd token, live service-mode progress/stats, scheduled jobs,
  VSS cleanup at start.
* Secrets: `encv1:` AES-256-GCM under TPM→DPAPI→fallback chain with
  auto-upgrade.
* NimbusControl attachment: enrollment, check-in/command/policy loop
  (fail-closed), forward-only VSS-phase run reporting; SMTP alerting removed
  in favor of server-side alerts; portal-delegated image browsing over the
  command channel (data-only ZIPs).
* Engine: directory (PXAR/DIDX, auto-split) + machine (FIDX, 4 MB pipeline,
  shared zstd encoder, 128 MB elastic buffer, abort-before-index-close);
  upload rate limit; honest byte-based ETA; VSS diagnostics through
  `snapshot.LogFn` (nothing prints into the void in a service).
* Image browse/restore: GPT/MBR + NTFS/FAT/exFAT read-only stack, $MFT
  fast-tree with plan-driven prefetch, ADS + security descriptors, unified
  restore ordering, partition picker law, ReFS/BitLocker refusals.
* UI: full theme system (base + accent overlays, font sizes, OpenDyslexic),
  webview PathPicker (COM dialogs retired), fr/en/es i18n with the audit
  gate (395 × 3), `[NB-xxxx]` error codes.
* Release engineering: tag-driven pipeline with gosec + golangci-lint gates,
  provenance attestation, checksums, VT-gated links, MSI with KEEP_CONFIG
  uninstall, service `.syso` version metadata.

Known debts entering Phase 1: **CI executes tests for only two of the
workspace's modules** (`gui`, `controlplane`) — `pbscommon`, `imagebrowse`,
`snapshot`, and `pkg/*` suites exist but never run in CI (GOWORK=off +
per-module `go test` means dependencies compile, their tests don't);
cross-view compile breakage is caught only when the matching CI leg happens
to run (rule 3's war story); MSI install/uninstall is a manual doc
(`MSI_UNINSTALL_TEST.md`); machine-image restore has never been
boot-verified end-to-end; Authenticode signing pending (SignPath);
`FEATURES_STATUS.md` drifts.

## Phase 1 — CI truth: the smoke-test ledger (shipped)

The control server's lesson, imported: its HTTP smoke suite found a role that
every unit test swore worked and that had never existed in the database. The
client's equivalent blind spots are below. **This table is the authoritative
ledger of smoke tests that must be in CI**; each row is either green in CI or
an open item — no third state.

**0. CI did not run on the default branch.** Before anything else: the
workflow triggered on `main`/`develop`/`github-release` and `v*` tags, but the
default branch is `master`. Every historical run was a tag run, so the entire
suite only ever executed at release time — which is how a Linux build break
reached the v0.2.150 tag instead of failing the commit that caused it. A smoke
ledger is worthless behind a trigger that never fires; `master` and PRs to it
now run every gate.

| # | Smoke test | Proves | Runner | Status |
|---|---|---|---|---|
| S1 | **Workspace test sweep** — `go test -race` across `pbscommon`, `imagebrowse`, `snapshot`, `clientcommon`, `pkg/*`, `gui/api`, and the CLI modules | The protocol layer, filesystem parsers and shared helpers pass on every push; also the only Linux compile check `nbd/`, `directorybackup/` and `machinebackup/` get | ubuntu | **green** |
| S2 | **Compile-view smoke** — `go vet` on `gui/` for linux+GUI, windows+GUI and windows+service | No symbol referenced in shared code is missing from a shippable compile view — the v0.2.150 `snapshot.LogFn` failure class, in seconds instead of at tag time. The two Windows views were previously proven only by the Windows build, 20 minutes later and behind other gates | ubuntu | **green** |
| S3 | **Local API smoke** — the real server on a real listener through the real `authMiddleware`: token absent/wrong/prefix/suffix, browser-`Origin` refusal, 1 MiB body cap, every route's auth + method gate, jobs/PBS/config/controlplane delegation, progress relay round-trip, handler-error propagation | The auth gate and every JSON contract the GUI and NimbusControl depend on, at the wire level rather than via direct method calls | ubuntu | **green** (10 tests) |
| S4 | **Control-plane loop smoke** — enroll → check-in → command drain → forward-only report, incl. 429 retry | The agent↔server contract in `docs/AGENT-API.md` survives refactors | ubuntu | **partial** — runs in CI as `controlplane` unit tests; explicit fail-closed-policy and cert-mismatch assertions still to add |
| S5 | **Image-browse fixture smoke** — real mkfs NTFS/FAT12/16/32/exFAT images in `imagebrowse/testdata`: partition parse → list → $MFT fast-tree ≡ generic walk → extract → ReFS/BitLocker refused. Plus `parser_robustness_test.go`: ~2,300 mutated images and adversarial geometries must fail, never panic, hang, or allocate from image-controlled sizes | The restore-side parsers against ground-truth filesystems AND against hostile ones — a backup image is untrusted input | ubuntu | **green via S1** (previously never executed) |
| S6 | **PXAR/index round-trip** — chunker determinism + size distribution, FIDX spans/EOF/bad-magic, catalog round-trip | The wire formats we write are the ones we can read back | ubuntu | **green via S1** — and it immediately found the chunker suite was measuring zeros (below). A pxar *writer→reader* round-trip is still to be written |
| S7 | **Secret-store smoke (Windows)** — seal/unseal through the real chain, corrupt/tampered/foreign-key degradation to "re-enter", protector auto-upgrade preserving the DEK | Dev rule 11 on real Windows crypto. Runners have no TPM, so the load-bearing assertion is that the chain does **not** fall through to `plaintext` | windows | **green** |
| S8 | **Artifact identity + API credential (Windows)** — `EnsureToken` idempotence and the icacls DACL (locale-independent: no inherited `(I)` ACEs); shipped binaries exist, are plausible, and `NimbusBackupSVC.exe` carries CompanyName/ProductName/ProductVersion | The token guarding the privileged API is really restricted, and the `.syso` AV-false-positive mitigation has not silently stopped linking | windows | **green** |
| S9 | **MSI install/uninstall smoke** — `msiexec /qn` install → service registered + binaries present → uninstall with `KEEP_CONFIG` both ways → config preserved / removed accordingly | The installer customers actually run, and the uninstall contract that a customer's PBS credentials survive unless they say otherwise | windows | **green, non-blocking** — advisory until it has a track record on hosted runners |
| S10 | **VSS snapshot smoke (Windows)** — real snapshot through the production path; asserts the 0.2.150 diagnostics reach `LogFn`, then the lifecycle and cleanup | The most privileged thing the product does. The diagnostics assertion holds even where a runner refuses to snapshot — silence means the "prints into the void in a service" regression is back | windows | **green, non-blocking** — same reason |
| S11 | **Frontend gate, early** — `npm run i18n-audit` + full frontend build on ubuntu | Parity/hardcoded-string violations fail in a minute on a cheap runner instead of 20 minutes into the Windows Wails build, where the audit previously only ran as a `prebuild` hook | ubuntu | **green** |
| S12 | **CLI start smoke** — every `dist/proxmoxbackup-*` runs `-h` on its build OS under a portable watchdog; empty output or a startup crash fails | Cross-compiled binaries at least start (init panics, missing dynamic deps) before they reach a customer | all three | **green** |

Explicitly **out of CI**, and labeled rather than pretended: full machine-image
restore with `nbd` map + boot-verify (needs a real PBS + KVM lab — Phase 3),
Exchange health/truncation behavior (needs a real Exchange host — rule 16), and
TPM-present protector paths (runners have no TPM; S7 covers the DPAPI leg, the
TPM leg is a lab checklist item).

### What the ledger found on its first run

Landing S1 immediately produced the thing it was built to produce — bugs in
code CI had never executed:

* **The `snapshot` suite did not compile on Linux at all.** `snapshot_test.go`
  carried no build tag but referenced `SymlinkSnapshot` and
  `getAppDataFolder`, which exist only in the `windows`-gated file. The tests
  guarded themselves with *runtime* `runtime.GOOS` skips, which do not stop the
  compiler. Split into `snapshot_windows_test.go`; the module now builds and
  passes on Linux for the first time. This is dev rule 3 in the test tree
  rather than the source tree — the same class, one directory over.
* **`pkg/logger` failed 4 of its 5 tests.** `Init(w, level)` was wrapped in
  `sync.Once`, so every call after the first silently discarded its writer and
  level. Not merely a test artifact: any process that re-initializes its logger
  (after loading config, or a service restarting its sink) would keep writing
  to the original destination with no error anywhere. `Init` now rebinds, with
  the global held in an `atomic.Pointer` so `Get()` racing `Init()` is
  race-clean. Nothing in the workspace imports this package yet — worth
  deciding in Phase 4 whether it is adopted or deleted, but shipping a helper
  whose entry point ignores its arguments is worse than either.

* **`pbscommon`'s chunker suite measured nothing.** `chunkData` read
  `c.chunk_size` after `Scan` returned — but `Scan` zeroes `chunk_size`, the
  rolling hash and the window immediately before returning a boundary, so
  every chunk measured 0. The min/max and average-size tests failed once they
  finally ran; worse, `TestChunkerDeterministic` had been *passing* the whole
  time, because two runs of all-zeros compare equal. The chunker itself is
  fine: reading the boundary from `Scan`'s return value shows a 912 KB average
  against a 1 MB target over 114 chunks, which is what a content-defined
  chunker should look like. A green test that asserts nothing is worse than a
  missing one — it buys false confidence in the dedup layer.

### Security review findings

A follow-on audit of the paths Phase 1 had just made testable:

* **Image restore could write outside the destination (fixed).** The
  image-restore path joined filenames taken from the image's own $MFT / FAT
  directory entries straight onto the destination. The PXAR path guards this
  exact class explicitly; the image path did not. The parsers reject a
  component equal to `..`, so a well-formed volume was never affected — but
  nothing rejected a separator INSIDE a name, and a crafted or corrupted
  image can carry `..\..\..\Windows\System32\evil.dll` in a name field,
  because that is a Windows API restriction rather than an on-disk format
  one. `filepath.Join` then resolves the `..` instead of refusing it. The
  service performs delegated restores as LocalSystem, so the ceiling was an
  arbitrary write as SYSTEM. Now routed through `security.SafeJoin` /
  `SafeBaseName` (flattening does not sanitize: `Base("..")` is `".."`).
  Refused entries are counted and reported separately from metadata
  warnings — a refused file was NOT restored, and saying otherwise would be
  a lie about backup contents.
* **PBS certificate pinning was skippable on resumed sessions (fixed).**
  `pbscommon` pinned via `VerifyPeerCertificate`, which is not invoked when a
  TLS session is resumed — while `controlplane` already used
  `VerifyConnection` for precisely that reason, with a comment saying so. The
  same defect class, ruled on in one module and not the other. Now both use
  `VerifyConnection`. Nothing sets a `ClientSessionCache` today, so this was
  defence in depth rather than a live hole; the unpinned branch also gained
  an explicit TLS 1.2 floor.
* **A certificate pin mismatch was retried for 40 seconds (fixed).** The
  control-plane retry ladder treated every transport error as transient. A
  pin mismatch is permanent — the certificate will not become the pinned one
  by waiting — so under an active MITM the agent hammered the impostor on
  every cycle while delaying the error an operator needs to see. Now a
  sentinel (`ErrCertPinMismatch`) marks it non-retryable. Side effect: the
  controlplane suite went from 60s to under 4s, because the pre-existing pin
  test had been walking that ladder.
* **"Fail-closed" was true only at startup (documented, knob added).** The
  agent denies everything before its first check-in, but once a capability is
  granted it stayed in force forever while the server was unreachable — so
  blocking the agent's egress to the control plane, trivial from the same
  LAN, freezes its capabilities and a later revocation never lands. That is
  an availability/security tradeoff rather than an outright bug, so the
  behavior is now bounded by an OPT-IN `Agent.PolicyMaxAge` (default: the
  historical behavior, unchanged) and both branches are tested. Deciding the
  default is a product call, tracked for Phase 4.
* **A crafted exFAT boot sector could OOM the process (fixed).** The spec
  bounds a cluster to 32 MB — `SectorsPerClusterShift` may not exceed
  `25 - BytesPerSectorShift` — but the parser bounded
  `SectorsPerClusterShift` alone at 25, which is not the same check.
  `bpsShift=9` with `spcShift=22` yields a 2 GB `clusterSize` that passed
  every later validation, and the reader allocates a whole cluster per read
  (`readRun`, `readDirBytes`, `ExtractFile`). A 64 KB crafted boot sector was
  enough to get the process OOM-killed; with the old check restored the
  regression test dies with `signal: killed`, with the fix it passes in
  milliseconds. Browsing an image must survive a corrupted one — this is the
  DoS half of dev rule 14.
* **The rest of the parsers held up.** ~2,300 mutated images plus deterministic
  adversarial geometries (integer overflow in `numFATs * fatSize`, volumes
  claiming more space than the image holds, root clusters past the end,
  truncated volumes) produced no panic, hang, or unbounded allocation. Cluster-
  chain traversal already has cycle detection and bounds in both FAT and
  exFAT, and the large reads are capped (`maxFAT`, `exMaxBitmapBytes`,
  `exMaxDirBytes`, `exMaxChainCluster`). That is a real negative result, and
  it is now a permanent test (`imagebrowse/parser_robustness_test.go`, ~8s in
  S1) rather than a one-off audit. The first version of that fuzz reached only
  5.6% of the parsers because it never satisfied the filesystem sniffer —
  coverage was checked precisely because a fuzz that bounces off validation
  proves nothing, which is the same failure mode as the chunker test above.
* **Dependencies.** `npm audit` reports one high and one moderate against the
  frontend toolchain (vite `server.fs.deny` bypass on Windows alternate
  paths, launch-editor NTLMv2 hash disclosure via UNC, esbuild dev-server
  request forgery). All are DEV-SERVER only — the shipped artifact is a Go
  binary with pre-built embedded assets — but they are live risks on a
  Windows developer workstation running `wails dev`. On the Go side the
  `gui` module still pins `golang.org/x/net v0.12.0`, which predates the
  HTTP/2 Rapid Reset and CONTINUATION-flood fixes (`pbscommon` is on
  v0.23.0), and the Wails library (v2.8.0) trails the pinned CLI (v2.12.0).
  Scheduled with the Phase 4 dependency review; none is reachable from
  untrusted input in the shipped agent today.

S2 also corrected this document. It was first written to check **four**
views ({GUI, service} × {windows, linux}), and the linux+service leg failed
immediately with `undefined: NimbusService` — not a defect, but an artifact
of asserting a configuration that cannot exist: the service is a Windows
service, so on Linux there is neither a service implementation nor a `main()`.
The rule and the job now describe the three views the product actually ships.
A gate that fails on an impossible configuration teaches people to ignore
gates.

Both bugs had been in the tree, untested, across many releases. Neither was
reachable by any gate that existed before this phase.

Exit criteria met: every row is green-in-CI or consciously deferred with a
reason; the `test` job no longer stands for a tenth of the workspace; a rule-3
violation cannot reach a tag build again; and the gates run on the branch
development actually happens on.

## Phase 2 — MSI provisioning pipeline (the NimbusControl contract)

Implements the client side of NimbusControl Phase 6 against the frozen
`docs/MSI-PROVISIONING.md` interface: per-org install profiles (server URL,
org enrollment token, default backup mode baked in), an MSI build that
consumes a profile and produces a preconfigured installer, and the signing
hook so profiles are verifiable. First-boot behavior: enroll with the baked
token, adopt the delivered default mode, wipe the token (rule 10).

Exit: a profile downloaded from a NimbusControl org page produces an MSI
that enrolls itself on first service start in a lab VM; S9 extended to cover
the preconfigured variant; the contract doc versions locked between repos.

## Phase 3 — Engine correctness milestones

1. **Multi-volume single-snapshot-set** — machine backup currently snapshots
   each volume in a separate VSS set (separate instants); a database split
   across volumes (SQL data on D:, logs on E:) is torn across volumes.
   Requires extending go-vss to `AddToSnapshotSet` all volumes before one
   `DoSnapshotSet`. Single-volume layouts are unaffected today.
2. **Restore acceptance** — the one milestone never exercised end-to-end:
   machine-image restore via `nbd` map + boot-verify in the PBS+KVM lab, as
   a documented, repeatable runbook (out-of-CI by declaration above).
3. **CBT read-time skip** — stays out of scope: upload-side dedup already
   handles unchanged chunks; a read-time skip needs a signed kernel filter
   driver.

Exit: split-volume test case captures one instant; a machine image restored
and booted, with the runbook committed.

## Phase 4 — Beta hardening

1. **Authenticode signing** — SignPath OSS certificate integrated into
   build-gui; S8 extended to assert a valid signature; the release-notes
   false-positive banner retired.
2. **`FEATURES_STATUS.md` resolved** — folded into this document or deleted;
   drift ends either way (rule 18).
3. **Dependency review** — written justification per direct dependency in
   this file (rule 10's clause), supply-chain pass over the Wails/go-ntfs/
   go-vss surface. Concretely: bump `golang.org/x/net` in `gui` off v0.12.0,
   align the Wails library with the pinned v2.12.0 CLI, refresh the frontend
   toolchain for the dev-server advisories, and add `govulncheck` +
   `npm audit` as CI gates so this list maintains itself.
4. **Decide the `PolicyMaxAge` default** — whether a server-granted
   capability should expire when the control plane cannot be reached. Today
   it does not, which means blocked egress freezes policy; the mechanism
   exists and is tested, only the default is open.
5. **Docs freeze** — README (both languages), ARCHITECTURE, CONTROL-PLANE,
   MULTI_PBS guides current; upgrade notes since 0.2.x.

Exit = **beta**, aligned with NimbusControl v0.9.0: smoke ledger fully green,
no known data-safety or security debt, signed artifacts, provisioning
round-trip demonstrated org-to-agent.
