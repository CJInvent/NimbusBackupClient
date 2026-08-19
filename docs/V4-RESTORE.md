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
| `openImageReader` | `gui/imagebrowse_core.go` | image browse, download, restore — including the ones NimbusControl drives remotely |

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
bundle, is **a new entry in that list and no change to any caller**.

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

**The lasting fix** is to match a restore release against a restore *this
server itself requested* — `Restore\ImageBrowseController` already records
agent browse and extract commands — which turns `restore` into a claim with
evidence behind it rather than a label. That is a follow-up, written down so
nobody reads the label as a control it is not yet.

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

## Open — needs a decision

**A GUI on Windows may not be able to read the key file.** Phase F gave
`backup-key.json` a deliberately narrow ACL (`gui/keyacl_windows.go`): SYSTEM
and Administrators only, *no* `S-1-5-4` INTERACTIVE, on the reasoning that "the
GUI links no backup engine, so a console user has no business reading it".

That reasoning was incomplete. The GUI links no *backup* engine — CI asserts
it — but it does link the **restore** engine, and restore now needs the key.
A non-elevated interactive user therefore gets a permissions error rather than
a restore. The DEK itself is not the obstacle: DPAPI is machine-scope
specifically so the service and a user-context GUI share it.

Three ways out, and this is a product decision rather than a code one:

1. **Relax the ACL** to include INTERACTIVE. Simple; undoes a deliberate
   hardening.
2. **Serve the key to the GUI over the local API.** The GUI↔service seam
   already exists, is token-authenticated and has a lockdown allowlist. The key
   still ends up in the GUI process, so the ACL becomes "token-holders only"
   rather than "not interactive users".
3. **Delegate the whole restore to the service** via a new local-API route, so
   the key never leaves the service at all. Architecturally the most
   consistent with "one pipeline in the service" — and the largest piece of
   work, because restore is long-running, cancellable and has a browse UI.

Until this is settled, the failure is at least *legible*: the key store's own
permission error is carried verbatim into the refusal.

---

## What is not done in phase G

- **Historical-key restore.** Every server read path filters
  `status = 'active' LIMIT 1`, and the agent stores one key. The data to do
  better already exists (`backup_keys.key_id`, `master_key_id`, `status`,
  `retired_at`) and the client's source list is shaped to take it.
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
`gui/backupkey_wiring_test.go`.

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
