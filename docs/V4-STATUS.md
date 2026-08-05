# v4 Status — NimbusBackupClient

### Known gap, found 2026-08-05 while building the client locally

`gui/go.mod` does not require `github.com/getlantern/systray` or
`github.com/kardianos/service`, both of which `gui/` imports directly, and
`gui/` has no `go.sum` at all. The build only works because every CI job runs
`go mod tidy` first and resolves the graph from the network at build time.
That means the dependency set of a shipped, code-signed MSI is not pinned by
anything in the repository, and `go.sum` does not gate what goes into it.
Not fixed here — it wants its own commit, and `go mod tidy` output should be
reviewed rather than taken.

**Last updated:** 2026-08-05. Branch: `v4_dev`.

The v4 program spans two repositories and its documentation lives in
**NimbusControl**, because most of it describes a server/client contract that
only makes sense read as one thing:

| Document | What it is |
|---|---|
| `NimbusControl/docs/V4-STATUS.md` | **Start here.** Program-wide status, what is merged, what is not started, open questions |
| `NimbusControl/docs/V4-SPEC.md` | The corrected v4 requirements. §9 holds the verified PBS cryptography — do not re-derive it |
| `NimbusControl/docs/V4-CLIENT-CONFIG.md` | Design for the next block of client work: managed jobs, PVE scheduling, GUI lockdown |
| `NimbusControl/docs/V4-AUDIT.md` | Phase B audit findings, including the two client subsystems deleted |
| `NimbusControl/docs/AGENT-API.md` | The wire contract. It is authoritative: server-side compliance parses it and fails the build if the implementation disagrees |

One design record lives **here** rather than in NimbusControl, because it
describes this repository's internal architecture and nothing crosses the
wire:

| Document | What it is |
|---|---|
| `docs/V4-PIPELINE.md` | One backup pipeline, in the service. Root-causes the "GUI cannot see a scheduled backup" report, and lists what gets deleted |

---

## What changed in this repository so far

- `v4_dev` branched from `master` at v0.3.2. `master` remains trunk.
- **Go pinned to exactly 1.26.5.** Not `1.26` — `setup-go` resolves a loose
  pin loosely. A `toolchain` job asserts `go env GOVERSION` *and* that
  `go.work` plus all twelve `go.mod` files declare the same version.
  `controlplane/go.mod` had been sitting on 1.22 while everything else said
  1.25, and nothing complained: a newer toolchain satisfies an older `go`
  directive silently.
- **Dev releases** from `v4_dev` as `v4.0.0-dev.<run_number>` prereleases,
  reusing the artifacts the gate jobs already built so what ships is
  byte-identical to what was tested. Numbers start near 98 because
  `github.run_number` is per-workflow; deliberate, not reset.
- **Deleted, both orphaned** (audit CLIENT-1 / CLIENT-2):
  - `gui/backup.go` — a second backup engine that shelled out and recovered
    progress by running regexes over the child's stdout. Superseded by the
    in-process engines, which emit structured milestone events.
  - `gui/jobs.go` — a second job model (`Job`/`JobManager`) persisting to
    `~/.proxmox-backup-guardian/jobs.json`, a path inherited from the upstream
    fork and meaningless for a LocalSystem service. The live path is
    `ScheduledJob` in `scheduler.go`.
- `.github/workflows/release.yml` deleted: tag trigger commented out,
  duplicated `build-and-release.yml`, pinned Go 1.22.
- `gui/wails.json` productVersion 0.2.152 → 4.0.0. It feeds both
  `main.appVersion` and the WiX ProductVersion, so every dev MSI had been
  stamping itself a full minor behind the repo's own tags.

## What has NOT been built

Nothing client-side from the v4 feature set. In dependency order:

1. **Managed configuration.** The server has no representation of a backup job
   at all — directories, exclusions, drive letters, VSS, compression, backup
   type and schedule live only in `scheduled_jobs.json`, written by the GUI.
   Managed jobs become read-only on the client; local jobs stay, are reported
   through the existing `agents.inventory` blob (display only, never read back
   as config), and are clearable by an `agent_commands` verb. Collision rule:
   managed wins, then rename.
2. **PVE calendar-event scheduling**, replacing
   `ScheduledJob.ScheduleTime string // HH:MM`, which is daily-only.
3. **GUI lockdown** — read-only, single status page. Two traps, both written
   up in `V4-CLIENT-CONFIG.md` §5:
   - `ModeStandalone` is **overloaded**: it means both "no service found,
     execute directly" *and* "I **am** the service". The real discriminator is
     `isServiceProcess`, which is why every live check reads
     `!a.isServiceProcess && a.mode == api.ModeService`. A gate keyed on mode
     alone either blocks the service from running managed backups or fails
     open in the GUI.
   - direct execution becomes a registry toggle, default off
     (`HKLM\SOFTWARE\NimbusBackup`, following `gui/breakglass_windows.go`), so
     the GUI cannot silently become a backup engine when the service stops.
   - `restrictToOwners` is applied only to the API token file today;
     `config.json` and `scheduled_jobs.json` still inherit ProgramData's
     permissive ACL.
4. **Encryption** (spec phase E). The client has **none**:
   `pbscommon/pbsapi.go` returns `"encrypted chunks not supported"` and chunk
   digests are plain `sha256.Sum256`. This is a wire-layer rewrite, not a
   feature flag, and it is the largest item in the program.

   When it happens, collapse the four `dynamic/fixed × compressed/uncompressed`
   wrappers over `UploadChunk` into one options-taking call rather than adding
   an encryption axis and turning four into eight (audit CLIENT-3). The two
   uncompressed ones are already dead; they were left in place deliberately
   because deleting half a symmetric set is tidying the next phase undoes.

## Building and testing

Go 1.26.5 and Docker are **not** available in the development sandbox used so
far, so everything in this repository has been CI-verified only. If you have a
local Go toolchain, use it — it is faster than a push, and two red builds in
the server repo were caused by exactly that gap.
