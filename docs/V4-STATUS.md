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

**Last updated:** 2026-08-09. Branch: `v4_dev`.

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

## Where the client work stands

Items 1–3 below were the "not built" list in the previous revision. **All
three are built.** Encryption is in progress. Each has its own design record;
this is the index.

### 1. Managed configuration — **DONE**
The server owns `backup_jobs` and delivers the set on every check-in. The
client caches it in `managed_jobs.json` and fires it on schedule.

Three files, and the split is load-bearing:

| file | owner |
|---|---|
| `scheduled_jobs.json` | the user's own jobs |
| `managed_jobs.json` | the server's set — **replaced wholesale** every check-in |
| `managed_job_state.json` | fire history — the agent's |

Fire history cannot live with the managed set: that file is replaced every
check-in, so a daily job would re-fire on the first tick after each one.

Two rules, both about NOT starting backups: a job seen for the first time is
**seeded, not fired** (otherwise authoring a job at 14:00 with an 02:00
schedule backs up the whole org immediately), and a missed window **collapses
to one run** (catching up on backups whose moment has passed is pointless).

### 2. PVE calendar-event scheduling — **DONE**
`controlplane/calendar.go`, a deliberately literal port of the PHP parser. The
two are held to agreement by a **shared fixture file**,
`controlplane/testdata/calendar-fixtures.json`, byte-identical to the server's
copy with its SHA-256 asserted in both repositories.

It caught a real divergence on its first run: Go's `time.Date` rolls a
DST-nonexistent wall time **backward**, PHP rolls it **forward**. Fixed in
`normalizeForward`.

### 3. GUI lockdown — **DONE**, and it became the whole client rewire
Record: **`docs/V4-PIPELINE.md`**, including the corrections made along the
way. Outcomes:

- one backup pipeline, in the service, called by both builds' entry points
- **the GUI links no backup engine at all** — asserted in CI with `go tool nm`
  against an unstripped probe binary, because `wails build` strips symbols and
  a grep over an empty symbol table would pass while proving nothing
- `ModeStandalone`'s overload resolved by splitting it into `ModeInProcess`
  and `ModeServiceUnavailable`
- direct execution became a **service-side** toggle, not a GUI one: the
  service is what the control plane talks to, so org policy can render the
  toggle inert while the server is reachable — a GUI flag never could be
- polling is the only observation path; `StatusPanel.jsx` renders instead of
  the tab UI when locked

**Still open from this item:** `restrictToOwners` is applied only to the API
token file; `config.json` and `scheduled_jobs.json` still inherit
ProgramData's permissive ACL.

### 5. Crypto audit trail — **DONE**
`pbscommon.AuditLogFn`, separate from `DebugLogFn` and wired in
`gui/pbslog_glue.go` to `writeBackupLog`, which is **not level-gated** and
prefers the per-run logger. The separation is the point: whether a customer's
data was encrypted and under which key is asked at restore time, during an
incident, or by an auditor — months after whoever set `LogLevel` has forgotten
doing it, and a diagnostics channel an admin can quieten is the wrong place
for it.

One line per session (key enabled, by **fingerprint**), per index close
(crypt-mode, covering every chunk beneath it), per blob, and for the manifest.
**Never per chunk** — a 931 GB machine is ~240,000 of them. A test asserts the
raw key and the derived id_key never appear in what the hook receives.

### 4. Encryption (spec phase E) — **IN PROGRESS**
`pbscommon/crypt.go` implements the PBS chunk format, and `pbsapi.go` now
encrypts what it writes and decrypts what it reads.

Facts that cost real effort to establish and must not be re-derived from
memory — every constant was read from upstream source, and `crypt_config.rs`
lives under **`pbs-tools/`**, not `pbs-datastore/`:

- the IV is **16 bytes**. Go's `cipher.NewGCM` gives 12, and a 12-byte IV
  round-trips against itself perfectly while producing a blob PBS cannot read
- the digest is `sha256(plaintext ‖ id_key)` — the key is **appended**, and
  prepending breaks dedup silently
- `pbkdf2_hmac(enc_key, "_id_key", 10, sha256)`: the key is the *password* and
  `"_id_key"` is the *salt*, which reads backwards. Ten iterations is domain
  separation, not stretching — "hardening" it changes every digest
- encryption **short-circuits compression**, because PBS has a separate magic
  for the encrypted-and-compressed form and writing zstd under the plain
  encrypted magic yields a blob PBS accepts and cannot read

**Manifest signing is done** (`EncodeManifest`), and with it two things that
were quietly wrong:

- index entries declared `crypt-mode: "none"` while referencing encrypted
  chunks. `FileInfo::chunk_crypt_mode()` reads `none` as *expect plain
  chunks*, so a verify job would have reported corruption against intact data
- `EncodeEncryptedBlob` wrote a **zero CRC**. That came from reading
  upstream's `crc: [0; 4]` header literal without reading four lines further,
  where `blob.set_crc(blob.compute_crc())` overwrites it unconditionally for
  every variant. Chunks reach the same function via `DataChunkBuilder`, so
  upstream never emits a zero CRC and `DataBlob::decode` calls `verify_crc`.
  Now real — and it covers the **ciphertext only**, starting at
  `header_size(magic)` = 44, so the IV and tag are outside it. A CRC taken
  from byte 12 (which is right for the unencrypted paths) verifies against
  itself and against no Proxmox server.

More facts that must not be re-derived from memory: `compute_auth_tag` is an
**HMAC-SHA256 keyed with id_key**, NOT the append-the-key digest chunks use;
the signature covers canonical JSON with **both** `signature` and
`unprotected` removed; and **the manifest is signed, never encrypted** —
`proxmox-backup-client` uploads it with `encrypt: false`, because a datastore
must be able to list a snapshot's files without a key.

The signing tests are pinned to upstream's own vector
(`pbs-datastore/src/manifest.rs::test_manifest_signature`) rather than to a
round trip, per dev rule 25.

**Blobs are now encrypted too** — everything except the manifest. They were
going up in the clear from a client that was otherwise encrypting everything,
so a snapshot advertised as encrypted still exposed the ACL side-car, the
status side-car and a VM's full `qemu-server.conf`. Checked against the
consumer before changing it: `NimbusControl/scanner` opens **only**
`index.json.blob`, accounts every other blob by its manifest-declared size,
and already returns a typed `FormatError::Encrypted` for the encrypted magics.

**The encrypted-and-compressed form is implemented, and PHASE E IS
COMPLETE.** Encrypted chunks used to skip compression entirely, because zstd
output under `ENCRYPTED_BLOB_MAGIC_1_0` is a blob PBS accepts and cannot read.
The answer was to switch the magic, not to skip the compression:
`EncodeEncryptedBlobCompressed` keeps the compressed payload only when it is
shorter than the plaintext — upstream's rule — and emits
`ENCR_COMPR_BLOB_MAGIC_1_0` when it does.

**Compress, then encrypt.** Ciphertext is incompressible by construction, so
the reverse order still produces valid blobs while silently costing every
encrypted customer their whole compression ratio. A test asserts on SIZE,
since that is the only observable that separates the two orderings. On decode,
decompression happens strictly after the AEAD tag verifies, so an unauthenticated
zstd frame is never expanded.

Until now every encrypted snapshot was stored uncompressed. Existing ones stay
that way; the digest is over plaintext, so they dedup against new ones
regardless.

The `UploadChunk` wrapper collapse (audit CLIENT-3) is still outstanding and
is now more attractive: `putChunk` has been extracted, so the shared tail
already exists.

### 5. Key delivery and the verification gate (spec phase F) — **STARTED**

`controlplane/backupkey.go` holds the wire types and **the gate decision as a
pure function**, in the same style as `breakglass.go` and `unmanaged.go`, so
the logic that stops a backup is testable without a server, a registry or a
Windows machine. `Client.FetchBackupKey` and `Client.ReportKeyStatus` are
wired.

**The gate exists because "not encrypted" and "could not encrypt" are
different answers.** Backing up in the clear because a key was unavailable
silently downgrades a customer who believes their data is encrypted, and on
every dashboard it looks identical to a customer who chose not to encrypt —
nobody finds out until someone reads a snapshot they assumed was protected. So
a nil `backup_key` (encryption off) proceeds unencrypted, while an advertised
key we cannot obtain or verify REFUSES to start.

That refusal points the opposite way to `restrict_unmanaged_backups`, which
defaults permissive precisely because a restrictive default silently stops
backups. The difference is observability: a refusal reports a status, carries a
detail string and shows up as a failed run, whereas a silent downgrade is not
observable at all.

`TestNeverDowngradesSilently` sweeps the entire input space rather than the
cases anyone thought of, asserting that no combination yields "back up
unencrypted" while a key is advertised — and the converse, that encryption is
never claimed without the matching key.

**STILL MISSING — nothing works end to end yet:**

- **Storage.** The key must go through the EXISTING `gui/secrets.go` DEK and
  protector chain (§6 — extend it, never add a second secret store). But
  `encryptSecret` FALLS BACK TO STORING PLAINTEXT when the DEK is unavailable,
  and `decryptSecret` returns `""` on failure. Those semantics are right for a
  PBS token, which is re-enterable, and WRONG for a backup key: silently
  writing it in the clear defeats the point of encrypting at all, and an empty
  string returned to a caller that reads it as "no key" is exactly the collapse
  the gate exists to prevent. Same DEK, same protectors, DIFFERENT failure
  behaviour — fail loudly, never degrade.
- Calling the gate from the backup path, and reporting via `/key-status`.
- Writing `escrow_blob` to PBS as `rsa-encrypted.key.blob`, which is what makes
  the server-side org recovery bundle redundant rather than load-bearing.

## Building and testing

**Everything except the MSI build now runs in the development sandbox**, and
should be used — it is faster than a push, and the gap caused several red
builds before it was closed.

- **Go 1.26.5** (the CI pin) comes from the `actions/go-versions` GitHub
  release assets, not `go.dev/dl` — that redirects to a host outside the
  allowlist. Fetch the asset through the API with
  `-H "Accept: application/octet-stream"`.
- Building `gui/` needs `replace` directives in a **copy** of `go.work`
  pointing `golang.org/x/*` and `gopkg.in/*` at their GitHub mirrors; never
  commit those. `git.sr.ht/~jackmordaunt/go-toast` has no mirror, but nothing
  imports it, so a two-line stub module satisfies the graph.
- Verify **all four views**: `GOOS={linux,windows} go vet [-tags service]
  ./gui/...`. A symbol can exist in three and be missing from the fourth.
- `gui/api` and `controlplane` are dependency-free modules: `GOWORK=off
  GOPROXY=off go test -race .` works directly in the real tree.
- **golangci-lint runs twice**, matching CI: the default pass, then
  `--build-tags=service --disable=unused`. `unused` is disabled in the second
  because it reports symbols with no visible caller, which only has a
  meaningful answer where every caller is visible.
- The frontend builds with `npm install && npm run build` (`npm ci` fails on a
  lock mismatch), and `gui/frontend/dist/` **is tracked** — rebuild and commit
  it.
- **gitleaks** runs locally from its GitHub release tarball. Run it before
  pushing anything containing a credential-shaped string; `.gitleaksignore`
  explains why fixtures are generated at run time rather than written as
  literals.
