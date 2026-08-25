# Restoring an encrypted snapshot (spec phase G)

Read with `docs/V4-STATUS.md` (where the work stands), `docs/V4-PIPELINE.md`
(how a backup runs) and `docs/RESTORE_GUIDE.md` (the restore engine itself,
which predates encryption and describes the parts that did not change).

---

## The gap this closes

Phase E made every new snapshot encrypted. Phase F delivered the key that
encrypts them, stored it, and put a gate in front of the backup pipeline.
Neither touched restore.

The measurable form of that: before this work, `PBSClient.SetCryptKey` had
exactly two call sites, `gui/backup_inline.go` and
`gui/machine_backup_windows.go` — **both backup engines**. No restore, browse,
search, image or NBD path set a key, and nothing anywhere read
`rsa-encrypted.key.blob` back. The client had no manifest reader at all.

So the product could write a snapshot it could not read. Everything looked
correct until someone needed a restore, which is the worst moment to find out
and the least likely moment to be watching a test suite.

Nothing had failed, either. That is the part worth keeping: every unit test
passed, CI was green, and the missing half was invisible because no assertion
anywhere asked "and can we read it back". The source-level pins in
`gui/restorekey_wiring_test.go` exist so the same absence cannot recur quietly.

---

## The ordering problem

An encrypted snapshot is unreadable without its key, and the key is named by a
fingerprint that lives inside the snapshot. If that fingerprint were in an
encrypted file, a restore would have to hold the key in order to learn which
key to hold.

Upstream breaks the cycle, and we match it:

- `index.json.blob` (the manifest) is **never encrypted**. Its integrity comes
  from a signature, not from secrecy.
- The key's identity lives in the manifest's `unprotected` block — **outside
  the signature** — because a reader must work out which key to fetch *before*
  it can verify anything.

`pbscommon.blobExemptFromEncryption` is the write side of that rule.
`pbscommon/manifest_read.go` is the read side, and did not exist until now.

---

## How a restore finds its key

Every reader entry point calls `attachRestoreKey` immediately after
`Connect`, and there are exactly three:

| entry point | file | covers |
|---|---|---|
| `withSnapshotReader` | `gui/restore_inline.go` | file restore, content listing, search, metadata |
| `listSnapshotViaCatalog` | `gui/restore_inline.go` | the fast catalog listing |
| `openVolumeReader` | `gui/imagebrowse_core.go` | image browse, download, restore — including the ones NimbusControl drives remotely |

All three are `service`-only since the rewire below: they are the engine, and
the console no longer compiles it.

`attachRestoreKey` does four things, in this order:

1. Reads `index.json.blob` through the reader session and parses it.
2. Takes `unprotected.key-fingerprint`. **Empty is a real answer**: the
   snapshot is unencrypted, no key is set, and the restore proceeds. Snapshots
   written before phase E take this path.
3. Resolves that fingerprint to key material and calls `SetCryptKey`.
4. **Verifies the manifest signature** with the now-loaded key, and refuses the
   restore if it does not match.

Step 4 is the only integrity check a restore has over the snapshot *as a
whole*. Every chunk authenticates itself, so a chunk substituted from another
snapshot under the same key would verify happily; only the manifest ties the
set of files together. A file list with an entry removed, a size rewritten, a
backup-time moved — none of that is detectable anywhere else.

It refuses rather than warns. The value of the check is entirely in what it
stops, and a warning on a screen nobody is watching during an unattended
server-driven restore stops nothing.

### Why the lookup is by fingerprint

A machine holds one key today, so "use the key we have" would work today and
be the wrong shape tomorrow. The moment a key is rotated, a snapshot older than
the rotation needs a key this machine no longer holds — and code that never
asked *which* key it needed has nowhere to put that question.

So `gui/restorekey.go` asks for a key **by name**, from an ordered list of
sources:

```go
var keySources = []keySource{
    {name: "this machine's key store", get: storedKeyForFingerprint},
    {name: "the management server",    get: activeKeyForFingerprint},
}
```

Local before remote, deliberately: a machine that already holds the right key
must not need the management server to restore with it. That is the same
outage tolerance durable storage was chosen for in phase F, and reversing it
here would undo that decision on the one path where it matters most.

Adding the server's historical-key endpoint, or an operator-supplied recovery
bundle, is **a new entry in that list and no change to any caller**. The first
of those has since been built, and it cost exactly that: one source swapped,
no caller touched.

### The server source asks by name now

The original remote source fetched whatever key the server currently ISSUES
and compared it to what the snapshot wanted. That worked for the newest
snapshot and silently failed for every older one — and it was the wrong
request besides: it asked for the key to encrypt with in order to answer a
question about decrypting.

`POST /api/agent/v1/backup-key/for-fingerprint` answers the right question and
**can return a RETIRED key**, which is the whole point: every other server read
path filters `status = 'active'`, correctly, because serving a retired key for
a BACKUP would let a machine keep writing under a key an operator rotated away
from. A restore of a snapshot older than the rotation needs exactly that key.

A `404` is `ok=false`, not an error — the snapshot may belong to another tenant
or predate this server, and the search continues. Everything else IS an error,
because an unreachable server must not read as a missing key: an operator told
"no source could supply the key" during an outage goes hunting for something
that is sitting in the vault.

The fingerprint derivation now exists in two languages, so it is pinned in two
repos against one shared vector (`pbscommon/crypt_test.go` and NimbusControl's
`tests/key_delivery.php`). Each side is self-consistent, which is the hazard: a
shared mistake passes every test either side writes about itself and shows up
only as a restore that cannot find a key the machine is holding. Sabotaging the
server's derivation confirmed it — every fetch in the suite still worked, and
only the two vector assertions noticed.

### A wrong key is refused before it is used

Each candidate's material is checked against the fingerprint it actually
derives to. A source that offers material for a different key is an error, not
a "try the next one" — using it would mean decrypting under the wrong key, and
the AEAD failure that follows arrives hundreds of chunks later looking exactly
like corrupt storage.

A source that simply *does not have* this key says so and the search
continues. That is the rotation case and it is not a fault.

An **unreadable** source is a third thing again: its error is recorded and
survives into the final refusal even if a later source succeeds. The case that
matters is a GUI refused permission on the service-owned key file — an
operator reading "no source could supply the key" with nothing else would go
hunting for a missing key that is sitting on the disk in front of them.

---

## The `restore` release mode

A machine with no durable key storage holds nothing at rest by design, so it
has to **ask** for the key to restore. That request releases key material and
is audited like any other release — but no backup run follows it, and none
ever will.

The tempting implementation is to exclude restore releases from
`BackupKeys::unmatchedReleases()`, since they are unmatched by construction.
That would be a hole exactly the size of the audit: the mode is
**client-asserted**, so an attacker with a stolen agent token would claim
`restore` and vanish from the only report looking for them.

So restore releases are **still flagged**. What the mode buys is a legible
report — an operator can tell "a restore happened here" from "something took
the key and never backed anything up", instead of the two arriving as the same
row and the first teaching them to ignore the second.

Server side: migration `022_key_release_restore_mode.sql`, and the mode shares
the ephemeral rate ceiling. A restore is bursty in exactly the wrong way for
the durable limit — an operator recovering a machine opens several snapshots
in a row while hunting for the right one, and 3/hour would stop them halfway
through a recovery.

### The claim now carries evidence — BUILT

Both halves of the lasting fix are in.

**The server corroborates at report time.** For each `restore` release,
`BackupKeys::unmatchedReleases()` looks for a portal browse or extract issued
to that agent within an hour, with `requested_by` naming a real user, and
returns the evidence alongside the verdict.

**This agent reports its own restores.** `POST /api/agent/v1/restores`
(`controlplane/restorereport.go`, wired at the two file-restore ops in
`gui/restore_service.go`) files a `restore_runs` row when a restore starts and
again when it ends.

The two are graded apart and deliberately not collapsed:

| claim | what backs it | who could have written it |
|---|---|---|
| `asserted` | the `mode` field alone | this agent's token |
| `reported` | a `restore_runs` row near the release | this agent's token |
| `corroborated` | a portal action by a named user | **the server** |

A stolen agent token can write a `restore_runs` row. It cannot make the server
record that a human clicked something — that is the only line in the scale it
cannot cross, and merging `reported` into `corroborated` would erase it.

**What reporting must never do is fail a restore.** Recovery is the moment this
product exists for; if the control server is unreachable the restore runs and
the report is lost. `restoreReporter.finish` returns nothing at all, so there
is no error for a future caller to propagate, and a source-level test pins that
signature for exactly that reason.

`asserted` is not an accusation. A restore driven from this machine's own
console with a durably stored key can legitimately leave nothing else behind.

### A stronger shape, not taken yet

The three call sites are wired by discipline and pinned by source-level tests.
The failure that created phase G was exactly that discipline failing — three
reader entry points, none of them wired, nothing noticing.

The stronger shape is to move the guard into `pbscommon`'s reader
constructors: give the package a host-supplied key resolver (the same pattern
`AuditLogFn` and `DebugLogFn` already use) and have `NewDIDXReaderAt` /
`NewFIDXReaderAt` refuse to open an archive whose manifest names a key when no
crypt config is set. That makes an unkeyed reader on an encrypted snapshot
*impossible* rather than merely unconventional, gives any future reader site
the behaviour for free, and replaces the source-level pins with executable
ones.

It was not done here because it is a redesign of code that is already built,
tested and sabotage-verified, and because it trades a visible call site for an
implicit one. It is the right follow-up if a fourth reader entry point ever
appears.

---

## The rewire: restore moved into the service

Phase G left a question open — a GUI on Windows cannot read the key file,
because phase F gave `backup-key.json` an ACL of SYSTEM and Administrators
only. Three ways out were written down here: relax the ACL, serve the key to
the console over the local API, or **delegate the whole restore to the
service**.

CJ chose the third, and named why the question existed at all: earlier work was
left open. The architecture rule predates this phase — *the service owns all
the active pieces of the client software; the GUI is a client that talks only
to the service; every API call is gated on whether the caller is permitted by
the server to make it*. Backup was rewired that way in phase D. Restore was
not, and phase G's key problem was the first thing to trip over it.

So this is not a workaround for an ACL. The ACL was right; the console was in
the wrong place.

### What moved

Everything that reads a datastore. The console keeps its bindings — same
names, same arguments, same events, so the front end did not change — and each
one now says the same thing to the service over the local API:

| console binding | op | service |
|---|---|---|
| `ListSnapshots` | `snapshots` | `ListSnapshotsInline` |
| `ListSnapshotContents` | `contents` | `ListSnapshotContentsInline` |
| `GetSnapshotMeta` | `meta` | `ReadSnapshotMetaInline` |
| `RestoreSnapshot` | `restore` | `RestoreSnapshotInline` |
| `DownloadSelection` | `download` | `downloadSelection` |
| `SearchFiles` / `CancelSearch` | `search` / `cancel-search` | `SearchFilesInline` |
| `ListVolumePartitions` | `image-partitions` | `ListVolumePartitions` |
| `ListVolumeFiles` | `image-contents` | `ListVolumeFiles` |
| `ListVolumeDirectory` | `image-directory` | `ListVolumeDirectory` |
| `DownloadFilesFromVolume` | `image-download` | `DownloadFilesFromVolume` |
| `RestoreFilesFromVolume` / `CancelVolumeFileRestore` | `image-restore` / `cancel-image` | `RestoreFilesFromVolume` |

Three routes carry all of it — `/restore/query`, `/restore/job`,
`/restore/control`, plus `GET /restore/job/<id>` to watch one — because a route
per operation would put the gate in a dozen places, and a gate in a dozen
places is how three entry points came to have no gate at all.

### The gate

`gui/api/restore.go` holds ONE table naming every operation the console may ask
for and the permission each requires. The check runs before dispatch, on the
far side of an authenticated socket, against a predicate the service installs
from `ControlPolicy().FileRestore`.

Three properties are deliberate:

- **An undeclared op is refused.** Adding a handler case grants nothing; the
  declaration is where the permission is written down.
- **No predicate means no restore.** The api package refuses when none is
  installed, so an edit that drops the wiring in `service.go` breaks restore
  loudly rather than quietly ungating it.
- **The check repeats on collection.** A job permitted at 09:00 and still
  running at 09:05 stops being collectable the moment the org revokes restore.

Restore is *not* in the read-only allowlist: a locked console is a status page,
and browsing a backup is not a status.

### What this fixes beyond the key

- **The key never enters the console process.** The ACL stays as phase F wrote
  it, and the open question is closed rather than traded away.
- **PBS credentials never enter it either.** The console names a server by its
  configured id; base URL, auth id and secret are resolved in the service, by
  the process that owns `config.json`.
- **The policy check is no longer inside the process it restrains.** That is
  what made the August 2026 gap possible to have.
- **Space enforcement is done by the writer.** The console keeps its warning
  dialog; the block lives in `download_service.go`, running as the account that
  writes the bytes.

### How it is enforced

CI asserts the console **compiles** no engine, by asking the toolchain for each
build's file list and checking every engine file is in the service's set and
none of the console's. That check cannot be vacuous: it fails if a file is
missing from the service side too, so a rename or a deletion breaks it rather
than satisfying it.

The `go tool nm` probe is kept as a second opinion and is documented as the
weaker one. The linker drops what nothing reaches, so an engine that is
compiled in but currently uncalled can leave no symbol behind — "no symbol" is
evidence, "not compiled" is proof.

### What is worse than before, honestly

- **A restore is polled, not streamed.** The console asks every 500ms and
  translates each state into the event the front end already listened for. It
  is a loopback request on the same machine, and it matches how backup runs are
  already observed, but it is a poll.
- **Two cancels exist conceptually** — the job's context and the engine's own
  cancellation. Only the engine's is used, because it stops at a boundary it
  chooses (a snapshot for search, a file for a volume-file restore) and so leaves
  nothing half-written. The context is carried unused and says so.
- **One image operation at a time.** The engine already had a single cancel
  slot, so this was always true; it is now stated and refused explicitly
  instead of silently clobbering.

## What is not done in phase G

- ~~**Historical-key restore.**~~ **Done** — see "The server source asks by
  name now" above. The agent still stores one key; what changed is that it can
  ask the server for another one by name, and the server will serve a retired
  one for a restore while continuing to refuse to issue one for a backup.
- **Recovery-bundle restore.** `Vault::exportMasterKey` +
  `escrowedKeysFor` produce a bundle that recovers a key with no server in the
  path, verified by `tests/key_recovery.php` — but nothing *in the product*
  consumes one. Today it is a human with stock `proxmox-backup-client`.
- **Dedup measurement against an encrypted datastore.** The scanner is
  designed for it (typed `FormatError::Encrypted`, blobs never opened,
  `crypt_mode` carried into `pbs_snapshot_files`) and has never been run
  against one. Note also that chunk digests are `sha256(plaintext ‖ id_key)`,
  so **key scope is the dedup boundary**: per-agent keys mean zero cross-machine
  dedup and a rotation re-uploads everything. Nothing measures that yet.
- **An end-to-end restore of an encrypted snapshot against a live PBS.** This
  is phase E's outstanding caveat and phase F's, and it is the thing that would
  replace the source-level pins in `gui/restorekey_wiring_test.go` with an
  execution.

---

## Testing

`pbscommon/manifest_read_test.go` — the load-bearing assertion is that
`VerifyManifestSignature` accepts **upstream's own published vector**, the same
constant `manifest_sign_test.go` pins for the write side. A verifier checked
only against our own signer proves the two agree with each other and nothing
about whether either agrees with PBS (dev rule 25). A restore that rejects a
snapshot stock `proxmox-backup-client` wrote is a bug this suite has to see.

`gui/restorekey_test.go` — the selection logic, with injected sources so no
DPAPI protector or control plane is needed.

`gui/restorekey_wiring_test.go` — source-level pins on the three reader entry
points, in the same spirit and with the same honest limitation as
`gui/backupkey_wiring_test.go`. `service`-tagged since the rewire, like the
code it pins.

`gui/api/restore_test.go` — the gate, swept over the declared op table: every
op refused when `file_restore` is off (403, and the engine never reached),
every op reaching the engine when it is on, an unconfigured server refusing,
an undeclared op refused before dispatch, and the routes refused under
lockdown. It replaces `gui/restore_policy_test.go`, which swept the same
property one layer up while the gate still lived in the console.

`gui/restore_ops_test.go` — the three places an op name is written down (the
console's constants, the gate's table, the service's dispatch) must agree.
Drift between them fails on a customer's machine and fails quietly: a console
asking for an undeclared op gets a 400 that reads like a broken restore.

Five sabotages were run and all five were caught by the intended test:
accepting whatever a source offers; letting an encrypted blob through the plain
decoder; making signature verification a no-op; forgetting to strip
`unprotected` before checking the signature; and dropping the verification call
from `attachRestoreKey`.

One real bug was found by a test while writing them: `DecodePlainBlob` checked
the CRC *before* reading the magic. An encrypted blob has a 44-byte header and
its CRC covers the ciphertext, so a plain-layout check on one compares two
unrelated numbers and reports a checksum failure — sending an operator to look
for damaged storage when the answer is "this is an encrypted blob". The magic
defines the layout the CRC belongs to, so it is read first.
