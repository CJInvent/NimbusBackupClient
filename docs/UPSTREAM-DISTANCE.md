# Distance from the upstream projects

**Status (revised 2026-10-11).** A measurement and a list, requested by CJ: how much of
this repository is still the code it was forked from, which files, and what it
would take to stop being related to it. No code changed. Companion to
`docs/V4-GUI-AUDIT.md`; the goals and the order of work are in
`docs/INDEPENDENCE-GOALS.md`.

**Direction (CJ, 2026-10-11):** all RDEM-authored code is to be replaced;
Tiziano's code is kept only as a reference for behavior. This work happens
**after the features reach full stability**, not before.

## 1. Lineage, as git shows it

* `tizbac/proxmoxbackupclient_go` (Tiziano Bacocco, first commit 2023-12-02) is the
  original. Its `master` also contains the `rdem` history: a second root commit on
  2026-03-17 ("Proxmox Backup Client Go with GUI") and about 435 commits by `rdem`
  (plus 101 by Richard Demongeot in `rdemsystems/NimbusBackupClient`), merged back.
  Other contributors: mjb-is, Giuseppe Pagano, Rene Jochum and a few one-offs.
* `rdemsystems/NimbusBackupClient` is the product this repository was started from.
* This repository's history begins 2026-08-25 (64 commits); it shares **no git
  ancestry** with either, so `git blame` cannot answer the question. The
  measurement below compares file contents.

## 2. Method and its limits

For every Go, JavaScript and CSS file in the upstream tree, `git diff --numstat
-M40%` against `HEAD` gives lines deleted; lines **unchanged** = upstream lines minus
lines deleted. Binary files, lock files, documents and `node_modules` are
excluded. Limits: it counts lines, so renamed identifiers and reformatting register
as change; trivial lines (braces, imports) register as unchanged; a file moved
and heavily edited can be missed (rename detection is 40%). Authorship split
uses `git blame` on `tizbac/master`, scaled by survival, and is an estimate.

## 3. The numbers

| Baseline | Upstream code lines | Still unchanged in `HEAD` | Share of upstream | Share of our 66,295 code lines |
|---|---|---|---|---|
| Tiziano's own code, last commit before the rdem history (2026-03-06, `6fcd5e0`) | 4,112 in 18 files | 3,801 | **92%** | **5.7%** |
| `rdemsystems/NimbusBackupClient` `master` | 45,014 in 174 files | 14,938 | 33% | 22.5% |
| `tizbac/master` today (tizbac + rdem + others) | 41,574 in 156 files | 15,090 | 36% | 22.8% |

Who wrote the roughly 12,600 surviving lines in the files that kept half or more
of their content (blame on `tizbac/master`): about **81% rdem**, **18% Tiziano**,
**2% other contributors**.

So: Tiziano's original engine (PBS client, pxar writer, chunker, directory and
machine backup, Windows VSS) is still almost entirely here, 92% of it, but it is
about 4,100 lines of our 66,000. The larger shared body, about 11,000 more lines,
is RDEM's work: the restore engine, search, pxar and catalog readers, the log
rotation, the GUI shell.

### 3a. Is any of it a dependency? No.

The shared files are source files that live in the fork, not libraries. This
repository has no `vendor/` directory; third-party libraries are declared in
`go.mod`, `go.sum` and `package.json` and were excluded from every count (as were
`node_modules`, lock files, built `dist`, binaries and documents). One caveat for
the protocol code: `pbscommon` implements formats that Proxmox Backup Server
defines (chunker, archive and index formats), and the buzhash table is almost
certainly Proxmox's published constants. Those constants and format facts are not
the fork's to claim, and whether Proxmox's own license says anything about them is
not checked here.

### 3b. Where it sits, and whose it is (re-run 2026-10-11)

Recounted against `tizbac/master` with non-blank lines only, so these totals
(about 12,300 shared lines) differ slightly from the 15,090 above, which counted every
line. Authorship is `git blame` on `tizbac/master` by commit identity, scaled by
how much of each file survives. It says who committed the lines, not who owns them.

| Area | Shared lines | RDEM | Tiziano | Other |
|---|---|---|---|---|
| Service and GUI Go (`gui/`) | 4,777 | 3,743 | 953 | 81 |
| PBS protocol (`pbscommon`) | 2,736 | 2,272 | 430 | 34 |
| Web UI (`gui/frontend`) | 1,810 | 1,280 | 525 | 5 |
| Other Go (backup engines, snapshot, nbd, pkg) | 1,833 | 1,087 | 671 | 75 |
| Tests | 1,192 | 1,065 | 126 | 1 |
| **Total** | **12,348** | **9,446** | **2,705** | **197** |

Answer to "is the RDEM code the web UI?": no. The web UI is 15% of what is shared.
Most of it is the service-side pipeline: backup and restore engines, the PBS
client, and the archive and catalog readers. The largest shared files by
surviving lines: `App.jsx` 1,718 of 2,876; `pbsapi.go` 909; `backup_inline.go`
860; `pxar.go` 747; `restore_inline.go` 635; `pxar_reader.go` 460;
`backup_analysis.go` 365; `log_rotation.go` 341.

*Since this was measured:* GUI audit S1 deleted `backup_analysis.go`, and S2
(2026-10-17) split `App.jsx` into `screens/`, `hooks/` and `lib/`. The split
moved lines without rewriting them, so the shared share of the web UI is
unchanged until the measurement is re-run against the new paths.

## 4. What is shared, by file

**A. Essentially untouched (80% or more of lines unchanged)** (19 files, 5361 upstream lines, 5003 still unchanged)

| File (upstream path) | Lines | Unchanged |
|---|---|---|
| `pbscommon/pxar.go` | 896 | 96% |
| `gui/restore_inline.go` | 831 | 84% |
| `pbscommon/pxar_reader.go` | 533 | 93% |
| `gui/backup_analysis.go` | 426 | 98% |
| `gui/log_rotation.go` | 387 | 100% |
| `gui/restore_search.go` | 352 | 84% |
| `pbscommon/catalog_reader.go` | 234 | 98% |
| `pkg/security/sanitize.go` | 232 | 96% |
| `pbscommon/didx_reader.go` | 200 | 97% |
| `clientcommon/mail.go` | 194 | 80% |
| `gui/backup_status.go` | 183 | 90% |
| `gui/restore_cache.go` | 175 | 99% |
| `clientcommon/icon.go` | 143 | 100% |
| `pbscommon/buzhash.go` | 141 | 100% |
| `gui/backup_meta_windows.go` | 132 | 100% |
| `gui/backup_split_api.go` | 108 | 91% |
| `gui/api_wrappers.go` | 94 | 94% |
| `gui/cleanup_legacy.go` | 68 | 88% |
| `clientcommon/win_locking.go` | 32 | 100% |

**B. Modified but recognizably the same file (30% to 80%)** (29 files, 13442 upstream lines, 7705 still unchanged)

| File (upstream path) | Lines | Unchanged |
|---|---|---|
| `gui/frontend/src/App.jsx` | 3079 | 62% |
| `pbscommon/pbsapi.go` | 1526 | 71% |
| `gui/backup_inline.go` | 1318 | 75% |
| `gui/scheduler.go` | 954 | 32% |
| `gui/config.go` | 889 | 37% |
| `gui/api/server.go` | 763 | 47% |
| `machinebackuplib/windows.go` | 664 | 58% | moved to `machinebackup/windows.go`
| `directorybackup/main.go` | 601 | 66% |
| `gui/api/client.go` | 570 | 51% |
| `nbd/main.go` | 448 | 54% |
| `snapshot/win_snapshot.go` | 365 | 36% |
| `directorybackup/config.go` | 259 | 72% |
| `machinebackup/main.go` | 249 | 31% |
| `gui/service.go` | 233 | 51% |
| `gui/pbs_server.go` | 206 | 30% |
| `nbd/fidxserver.go` | 183 | 68% |
| `gui/backup_meta.go` | 147 | 57% |
| `gui/api/auth.go` | 135 | 39% |
| `pkg/retry/retry.go` | 134 | 73% |
| `gui/tray.go` | 112 | 78% |
| `gui/app_types.go` | 109 | 30% |
| `pbscommon/didx_assembler.go` | 107 | 69% |
| `gui/api/mode.go` | 99 | 36% |
| `gui/api/types.go` | 79 | 72% |
| `gui/frontend/src/i18n/i18nContext.jsx` | 66 | 60% |
| `gui/service_main.go` | 60 | 70% |
| `gui/tray_stub.go` | 30 | 63% |
| `gui/admin_windows.go` | 29 | 41% |
| `snapshot/nop_snapshot.go` | 28 | 67% |

**C. Same path, rewritten (under 30% unchanged, 100 or more lines)** (24 files, 8140 upstream lines, 610 still unchanged)

| File (upstream path) | Lines | Unchanged |
|---|---|---|
| `gui/main.go` | 2124 | 22% |
| `machinebackuplib/machinebackup.go` | 723 | 0% |
| `pbscommon/crypt.go` | 654 | 13% |
| `machinebackuplib/linux.go` | 459 | 0% |
| `gui/disklist_windows.go` | 452 | 0% |
| `snapshot/linux_snapshot.go` | 395 | 0% |
| `gui/service_api.go` | 384 | 0% |
| `gui/app_service_stubs.go` | 297 | 11% |
| `gui/diskpin.go` | 246 | 0% |
| `gui/disklist_linux.go` | 214 | 0% |
| `gui/crypt_key.go` | 203 | 0% |
| `gui/backup_control.go` | 202 | 0% |
| `gui/token_elevated.go` | 179 | 0% |
| `gui/api/token_rotate.go` | 165 | 0% |
| `gui/backup_meta_linux.go` | 165 | 0% |
| `gui/logging_gui.go` | 158 | 0% |
| `gui/logging_service.go` | 158 | 0% |
| `gui/service_unix.go` | 157 | 0% |
| `gui/brand.go` | 144 | 0% |
| `snapshot/linux_ioctl.go` | 140 | 0% |
| `gui/single_instance_windows.go` | 137 | 0% |
| `gui/api/server_machine_shared.go` | 135 | 0% |
| `gui/token_windows.go` | 130 | 0% |
| `gui/api/reauth.go` | 119 | 0% |

**D. Front end files still sharing 30% or more** (2 files, 3145 upstream lines, 1961 still unchanged)

| File (upstream path) | Lines | Unchanged |
|---|---|---|
| `gui/frontend/src/App.jsx` | 3079 | 62% |
| `gui/frontend/src/i18n/i18nContext.jsx` | 66 | 60% |

Tests carried over almost unchanged: 8 files, 1388 lines.

## 5. Shared things that are not code

| Item | Where | Note |
|---|---|---|
| Module path `github.com/tizbac/proxmoxbackupclient_go/gui` | `gui/go.mod`, 13 Go files | Mechanical rename; touches every import |
| Application logo | `gui/logo.webp` (identical blob) | Replace |
| Tray/window icon bytes | `clientcommon/icon.go` (100% identical, 143 lines) | Replace the icon, then the file is ours |
| Screenshots | `docs/screenshots/*` (3, identical) | Delete or retake |
| Windows installer license text | `installer/wix/License.rtf` (identical) | The GPL-3 text itself; stays |
| `LICENSE` | GPL-3 | Stays (see section 6) |
| Build scripts and lint config | `build.bat`, `build_cli.bat`, `build_full_cli.bat`, `installer/wix/build.bat`, `gui/.golangci.yml`, `installer/config.example.json` | Replace or delete |
| Vendor strings | About tab, header, MSI properties, release workflow, `wails.json` | `docs/V4-GUI-AUDIT.md` Appendix A (step S3) |
| Fork documents | `MULTI_PBS_*.md`, `README.fr.md`, `RELEASE_NOTES.md` | `V4-GUI-AUDIT.md` Appendix C |

Also found while measuring, unrelated to lineage: four **compiled binaries are
tracked in git**: `gui/gui.exe`, `image-regression.test.exe`,
`storage-regression.test.exe`, `vss-regression.test.exe`. They should not be in
the repository.

## 6. What credit requires

This is not legal advice; ask counsel before relying on it. The project is
GPL-3. As far as I know the license requires the copyright and license notices of
any derived code to stay with it for as long as that code is in the program. That
makes the credit a duty, not a courtesy, for as long as files in groups A to C
remain, and it covers **both Tiziano Bacocco and RDEM Systems**, since most of
the shared code is RDEM's. Removing the vendor branding from the screens (audit
step S3) does not change that; the notice moves to the licenses line.

It also means "not related" is only reachable by replacing the files, not by
deleting the credit. There are three tiers:

1. **Cheap, mechanical, do first:** module path, logo, icon, screenshots, build
   scripts, committed binaries, vendor strings, the split-backup feature
   (`gui/backup_analysis.go`, 453 lines in our tree and 98% unchanged from upstream, and
   `gui/backup_split_api.go`, 104 lines and 91%, removed with the feature CJ decided to drop). Removing the
   feature alone sheds about 530 shared lines.
2. **Moderate:** group B, the files that were substantially edited. Each is
   a rewrite of a file we mostly already rewrote.
3. **Expensive (now the stated direction, after stability):** group A, above all the PBS wire format in `pbscommon`
   (`pxar.go` and `pxar_reader.go` about 1,430 lines, `buzhash.go`, `didx_reader.go`,
   `catalog_reader.go`, `pbsapi.go` about 1,500) and the restore engine
   (`gui/restore_inline.go` 835 lines). The formats are fixed by Proxmox Backup
   Server, so an honest independent implementation ends up shaped like the
   original; replacing them is real engineering, and a clean-room version needs
   an author who has not been reading the current files. Whether the original is
   "solid and needs no revision" I have not assessed: the carried-over tests
   (`chunking_test.go`, `catalog_reader_test.go`, the `snapshot` tests) are the
   evidence to look at first, and I have not run them here.

CJ's direction of 2026-10-11 replaces my earlier recommendation (tier 1 only):
all RDEM code goes, in all three tiers, after the features are stable. The order,
the provenance rule for rewritten files, the safety net and the dev-release
mapping are in `docs/INDEPENDENCE-GOALS.md`. The credit stays until the last
shared file is gone, and the module rename, logo and icon replacement and removal
of the committed binaries can still be done early since they touch no behavior.

Tier 1 also covers three places the first pass missed: the release text in
`.github/workflows/build-and-release.yml` (company name, copyright, vendor links
at lines 1026-1030 and 1580-1631), the attestation hint naming
`rdemsystems/NimbusBackupClient` (line 1499), and `installer/wix/Product.wxs`
(manufacturer, registry key, help and contact links) plus `installer/DEPLOYMENT.md`
and `installer/BUILD_INSTRUCTIONS.md`.
