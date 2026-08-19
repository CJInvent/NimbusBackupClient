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

### 5. Key delivery and the verification gate (spec phase F) — **CLIENT HALF DONE**

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

**TWO MODES, chosen by what the machine can support — not by preference.**

The plaintext-fallback dilemma is gone. It used to be a choice between writing
the key that decrypts every backup next to the config it protects, and not
encrypting at all. There is a third option:

| storage | behaviour | trade |
|---|---|---|
| DPAPI / TPM | store the key; fetch only on a mismatch | survives a control-plane outage |
| plaintext fallback only | never write it; fetch per run, RAM only | backups need the control plane reachable |

**Why not ephemeral everywhere.** `gui/managed_jobs.go` persists the job set
specifically so the scheduler keeps working through an outage. Fetching per run
makes every encrypted backup depend on the server, which would silently reverse
that commitment — and it is the same failure already rejected when
`restrict_unmanaged_backups` was given a permissive default: a restrictive
default that *stops backups*.

`KeyStorage` replaces the old `(storedKeyID, error)` pair, because "can I write
a file" was never the question — it is whether a key written there is protected
by something a stolen disk cannot defeat, which is how `secrets.go` already
ranks its protectors. Storage-*unreadable* stays distinct from storage-*weak*:
unreadable means we cannot tell what is sitting there, so we can neither trust
it nor safely skip persisting.

Ephemeral reports `ok`, not `unavailable` — it is not a fault, and colouring a
fleet view red for machines working as designed trains operators to ignore it.

**Go is pinned to 1.26.6, not 1.26.5.** The Dependency Audit job caught two
standard-library vulnerabilities — `GO-2026-6090` (crypto/tls) and
`GO-2026-5972` (encoding/asn1) — both reported reachable from this code:
`compressLogFile` → `io.Copy` → `tls.Conn.Read`, and `signal.Notify` →
`asn1.Unmarshal`. Both are fixed in go1.26.6.

Worth recording how this presented, because it looked like a code failure and
was not: CI went red on a commit that added no dependencies, and re-running the
previously-green `378f222` **unchanged** reproduced the same failure. A live
vulnerability database makes a passing build a statement about a moment, not
about a commit. That is correct and should not be "fixed" — a CVE published
yesterday *should* fail today's build.

`govulncheck` itself is now pinned (`GOVULNCHECK_VERSION`), like every other
tool in that workflow. The database stays live on purpose; the tool does not,
because a tool release can change output or exit codes independently of any
finding, and then red means "something changed" rather than "something is
wrong".

**WHAT WAS BUILT TO FINISH THE CLIENT HALF (2026-08-18)**

Four pieces, in dependency order — the list this document carried as "still
missing" the day before.

**1. Durable key storage — `gui/backupkey_store.go`.** The same DEK and the
same protector chain as `gui/secrets.go`, per §6: extend that store, never
stand up a second one. What is deliberately NOT shared is the failure
behaviour, and that is the whole reason the file exists rather than a call to
`encryptSecret`. `encryptSecret` falls back to *storing plaintext* when the DEK
is unavailable; `decryptSecret` returns `""` on any failure so the caller cannot
tell "nothing stored" from "there is something there and we could not open it".
Both are right for a re-enterable PBS token and catastrophic for a backup key —
the first writes the value that decrypts every snapshot in the clear beside the
config, and the second makes "fetch" and "REFUSE" look identical. Every path
here fails loudly, and a machine whose only protector is `plaintext` is refused
a durable store outright rather than degraded into one.

The escrow blob is stored **alongside** the key. It arrives only in the fetch
response, and a durable agent fetches roughly twice in a machine's lifetime —
so an agent that kept only the key would have nothing to write as
`rsa-encrypted.key.blob` on any of the thousands of snapshots in between, and
the recovery path would be missing from almost every backup it protects.

The file's ACL is **narrower than the local-API token's**: LocalSystem and
local Administrators, without `S-1-5-4` INTERACTIVE. The GUI links no backup
engine, so a console user has no reason to be able to read the key that
decrypts every snapshot.

**2. The ephemeral holder — `gui/backupkey_ephemeral.go`.** There is no holder
object, and that is the design. A package-level "current ephemeral key" with a
drop-it-when-the-run-ends hook is a key that outlives its run every time the
hook does not fire — a panic, a cancelled context, an engine returning down a
path nobody updated. The lifetime is the pipeline's local variable instead: the
key exists as an argument to one backup and becomes unreachable when that call
returns, with nothing to remember to clean up. It also does **not** perform a
theatrical wipe, because in Go on Windows there is nothing honest to wipe — see
the file's own note on the GC, the pagefile, crash dumps and hibernation.

**3. The gate, driven — `gui/backupkey_gate.go`, called from
`runBackupPipeline`.** One entry point, one caller. It runs AFTER the reporters
are attached and BEFORE the engine: a refusal has to be a reported FAILED run,
because a machine that quietly stops backing up looks exactly like one that is
fine, and refusing after the engine has uploaded data refuses nothing. The run
uuid is threaded through so a key fetch names the run it is for — the release
audit matches releases against the runs that followed, and omitting the uuid is
the evasion the server deliberately flags.

**THE STATE THAT IS NOT ON THE WIRE — the subtlest thing in this phase.** The
obvious implementation reads the last check-in advertisement and treats "no
advertisement" as "no encryption". That is wrong in a way that takes years to
surface: an agent that has never reached the server, or a service that
restarted during an outage, has no advertisement either — and backing up in the
clear because nobody has told us otherwise is precisely the silent downgrade
the three-state design exists to prevent. So:

- `Agent.CurrentBackupKey()` returns the advertisement **and a `known` flag**.
  `(nil, true)` is "this org does not encrypt"; `(nil, false)` is "we have not
  been told". They are opposite instructions and must never collapse into one
  nil pointer.
- The org's answer is **persisted** (`encryption: on|off` in
  `backup-key.json`), written on every check-in that delivers `backup_key:
  null` — which is why `OnBackupKey` fires on the null case at all.
- A machine that genuinely cannot tell — enrolled, never once reached the
  server, nothing recorded — **refuses**, with a message saying it will work
  once the server has been reached once. This is narrow and self-healing, and
  it is the one place in this phase where a restrictive default stops a backup.
  **Flagged for CJ as a judgement call**, not presented as settled: the
  alternative is to back up in the clear on a machine whose org may mandate
  encryption.
- The advertisement deliberately does **not** expire with `PolicyMaxAge`.
  Durable mode's entire promise is that an encrypted machine keeps backing up
  through a control-plane outage; ageing it out would quietly convert every
  such machine into one that stops.

**4. `UploadEscrowBlob`, wired — before `UploadManifest` in both engines.**
Via `PBSClient.SetEscrowBlob` / `UploadEscrowBlobIfEncrypted`, so the escrow
bytes travel with the key they escrow rather than as another parameter threaded
down through `backupDirectory`/`backupReal`/`uploadWorker`, which is how the
two come to disagree. Unlike the ACL and status sidecars beside it, a failure
here **fails the backup**: an encrypted snapshot with no escrow blob is
recoverable only through our control plane, the single dependency the escrow
design exists to remove.

**HOW THE WIRING IS PINNED, honestly.** The decision, the store, the holder and
the wire contract are all pinned by behaviour — a sweep over the decision's
whole input space, sabotage-tested failure modes on the store, a fake server for
the wire. The four *call sites* are not: they live inside `runBackupPipeline`
and two Windows-only engines, which need an App, a live PBS and a physical disk
to execute, and there is no harness here that reaches them. So
`gui/backupkey_wiring_test.go` reads the source and asserts the gate is called
before the engine, that its result reaches `BackupOptions`, that a refusal is
finalized through both reporters, and that the escrow blob precedes the
manifest in both engines. That is weaker than executing it and says so — a pin
in the same spirit as the `nm` assertion that the GUI links no backup engine.
It exists because this codebase has already been bitten by exactly the gap it
covers: reverting `UploadChunk` to encrypt-only failed nothing, because every
test exercised the helper directly. **It should be replaced by an end-to-end run
against a real PBS**, which is also what phase E's outstanding caveat asks for.

**STILL MISSING:**

- **An end-to-end run against a live PBS with encryption on.** Nothing in
  phases E or F has done this. It is phase E's stated caveat and it is now the
  next task: every piece exists, none of them have met each other outside a
  test.

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
