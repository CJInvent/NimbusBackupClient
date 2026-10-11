# Independence from the fork: goals

**Status (2026-10-11).** Goals and order only; no code changes. CJ's direction:
all RDEM-authored code is replaced, Tiziano's code is kept as a reference for
behavior only, the client is branded by the server, and the web UI is rebuilt in
many small files. **This work starts after the features reach full stability.**
Each goal below is the input to one specification, written before any code, as
usual. Measurements are in `docs/UPSTREAM-DISTANCE.md`; the screen rules are in
`docs/V4-GUI-AUDIT.md` (4.11 small files, 4.12 branding).

## 0. The gate: when this work may start

A proposal for CJ to confirm or change: the program starts when (a) no feature row
in `docs/V4-STATUS.md` is open, (b) the live tests on the bed machines (the
`v4-browse-live-tests` list and the Appendix D items of the GUI audit) have all
passed on one dev release, and (c) CJ says so. Until then the only work from this
program that is allowed is what the GUI audit already schedules (S1 to S13),
because it touches screens and not the engine.

## 1. The goals

| # | Goal | Done when | Shared lines it removes | Risk |
|---|---|---|---|---|
| G1 | **Our own identity.** Module path, logo, icon, screenshots, build scripts, committed binaries, vendor strings in the app, installer, release text and metadata | `scripts` check finds no import of the old module path, no tracked `.exe`, no `rdem`/`tizbac` string outside the credit line | about 150 (`icon.go`), plus the non-code items in section 5 of the distance document | none |
| G2 | **Branding from the server.** Product name, logo, accent, support contact reach the agent with the check-in and are drawn per 4.12 | A managed machine shows the organization's name and logo; a standalone one shows the neutral mark; no third-party fetch | 0 (new) | low |
| G3 | **Web UI rebuilt in small files.** `App.jsx` retired screen by screen; `i18nContext.jsx` rewritten; no file over 250 lines | `App.jsx` deleted; `gui/frontend` has no file shared with upstream; the size report passes | 1,810 (UI) | low |
| G4 | **Utilities.** `log_rotation`, `mail`, `sanitize`, `retry`, `win_locking`, `backup_status`, `api_wrappers`, `cleanup_legacy` | Each replaced by a file written from its behavior spec, tests pass | about 1,100 | low |
| G5 | **The service pipeline.** `backup_inline.go`, `restore_inline.go`, `restore_search.go`, `restore_cache.go`, `scheduler.go`, `config.go`, `api/server.go`, `api/client.go`, `service.go`, `main.go` remnants | Same, plus the live round trip on the bed | about 3,500 | medium |
| G6 | **Backup engines.** `directorybackup`, `machinebackup`, `nbd`, `snapshot` | Same, plus VSS and image smoke jobs | about 1,800 | medium |
| G7 | **PBS readers.** `pxar_reader`, `catalog_reader`, `didx_reader`, `didx_assembler` | Reads every golden archive the old code reads, byte-identical listings | about 1,000 | medium |
| G8 | **PBS client and writers.** `pbsapi.go`, `pxar.go`, `buzhash.go`, chunking | Produces byte-identical archives and chunk boundaries for the golden inputs; live backup and restore on the bed | about 1,800 | **high** |
| G9 | **Our own tests.** Carried-over tests replaced by tests written from the specs | No test file shared with upstream | 1,192 | low |
| G10 | **A repeatable distance check.** The measurement in `UPSTREAM-DISTANCE.md` turned into a script that prints the shared-line count by area and fails if it rises | Count reaches 0; then the check is removed and the credit question is revisited | n/a | none |

Order: G1, G2 and G3 can run during the stability period only if CJ allows (G1
and G2 touch no behavior). After the gate: G10, G9's fixtures, G4, G5, G6, G7,
G8 last, because the wire format is the one place a mistake corrupts backups.

## 2. How a file is replaced (the provenance rule)

1. **Spec first, from behavior.** The spec for a component is written from what it
   must do (the PBS format documentation, the V4 specs, the tests), not by
   rewording the old file.
2. **Golden fixtures before the swap.** While the old code still works, capture its
   outputs for fixed inputs as data files: archive bytes for a fixed tree, chunk
   boundaries for a fixed stream, catalog bytes, manifest JSON, the request
   sequence to a fake PBS. Data is not code; the new code must match the
   fixtures exactly where Proxmox fixes the format, and match behavior elsewhere.
3. **Write the new file without the old one open.** The implementer gets the spec
   and the fixtures. Where one session has already read the old file, the spec
   and the code should be written by different sessions. How strict this must be
   is a legal question, not mine; ask counsel.
4. **Record it.** `docs/PROVENANCE.md` gets one line per replaced file: date, spec,
   fixtures, and the dev release that shipped it. The distance check reads it.
5. **One component per pull request.** A pull request replaces one component and
   deletes the old file in the same change; nothing is kept "just in case".

## 3. The dev rolls (checked 2026-10-11)

What exists, read from `.github/workflows/build-and-release.yml`:

* Every push to `v4_dev` runs all gates (toolchain, check-deps, frontend, four
  compile views, security, gitleaks, unit tests, workspace tests, Windows smoke,
  reparse smoke, dependency audit) and, if they pass, **publishes a prerelease
  `v4.0.0-dev.N`**: CLI zips for three operating systems, the GUI `.exe`, the
  `.msi`, `build-info.json` and `SHA256SUMS`. The tag names the tested commit and
  a final step verifies that.
* Runs are one per branch and a newer push cancels an older one; tags are never
  cancelled. A manual re-run exists (`workflow_dispatch`).
* So "build small" already works: one merged pull request is one dev roll, and
  CJ installs it on a bed machine before the next one is merged.

What is missing or wrong for this program (proposed, not made; they are workflow
changes and wait for the gate):

| # | Revision | Why |
|---|---|---|
| W1 | A **distance** job (G10): prints shared lines by area, fails on a new import of the old module path, a tracked `.exe`, a file over the size limit it is told to enforce, or a rise in the shared count | Nothing checks any of this today |
| W2 | The dev-release body carries the **head commit's subject line** and the PR number | Today every roll looks the same; the bed tester cannot tell which step it is |
| W3 | **Golden-fixture tests run in the existing "Unit Tests" and "Workspace Tests" jobs**; fixtures live in `testdata/golden/` | They need no new job, only the files |
| W4 | The **release** job and the tag-release text still carry the vendor company name, copyright, website links and the attestation hint naming `rdemsystems/NimbusBackupClient` (lines 1026-1030, 1499, 1580-1631) | G1; this is user-visible on every release |
| W5 | There is **no live PBS round trip in CI**; it is run by CJ on the bed. A short checklist per goal (backup, restore, browse, encrypted and not) goes in the pull request body, and the dev roll is not called good until CJ has run it | Honest statement of what the gates do not prove |

## 4. What this adds to the specs still to be written

* `SPEC-BRANDING`: the fields, the check-in and cache behavior, the MSI-stamping
  question (an MSI cannot read the check-in). Server and agent halves.
* `SPEC-<component>` for each of G4 to G8, each with its fixtures list.
* `SPEC-PROVENANCE-CHECK`: the script and `PROVENANCE.md` format (G10).
* A stability definition, if CJ wants something firmer than section 0.

## 5. Open questions for CJ

1. Is the gate in section 0 right, or is full stability defined differently?
2. How strict must "no relation" be: different sessions for spec and code (section
   2, step 3), or is the fixture-and-spec approach enough? Counsel's answer
   decides this, not mine.
3. When the shared count reaches zero, the license no longer requires the credit
   for code that is gone. Keep a courtesy line or drop it? (Not legal advice.)
4. The `pbscommon` format constants come from Proxmox. Ask counsel whether
   anything about Proxmox's own license applies to them.
