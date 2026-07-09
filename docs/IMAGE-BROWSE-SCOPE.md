# Scope: browsing files inside image (machine/volume) backups

## The two backup types, precisely

| | Directory backup | Machine / volume backup |
|---|---|---|
| PBS archive | `backup.pxar.didx` (+ `catalog.pcat1.didx`) | one `*.img.fidx` **per disk** (+ `qemu-server.conf` blob) |
| PBS index type | **dynamic** (DIDX) — variable chunks, file-aware | **fixed** (FIDX) — 4 MB fixed chunks of a raw block device |
| Structure PBS knows | full file tree, via the catalog | **none** — it is an opaque disk image (MBR/GPT + partitions + filesystems live *inside* the bytes) |
| Browsable today? | **Yes** — `ListSnapshotContentsInline` walks the catalog | **No** — there is no file structure at the PBS layer to walk |

So "browse files in an image backup" is fundamentally: get **random-access
bytes** of the disk image, then **parse the partition table + filesystem**
ourselves to synthesize a file tree. PBS cannot do this for us.

## What the project ALREADY has (the hard 80%)

`nbd/fidxserver.go` — `FIDXServer` implements `io.ReaderAt` over a `.img.fidx`:
given a byte offset+length, it computes which 4 MB chunks are needed, fetches
only those from PBS, LRU-caches them (16 × 4 MB), and returns the bytes. This
is exactly the random-access primitive we need — and it fetches *only* the
ranges asked for, so browsing a directory listing pulls a handful of chunks,
not the whole multi-GB image.

`nbd/main.go` — wires that `ReaderAt` to a Linux `/dev/nbdX` device
(`modprobe nbd`, read-only) so the kernel mounts it. **This is the part that
is Linux-only and needs deep OS integration** — and it is exactly what we do
NOT want on the Windows agent.

## The Windows problem, and the chosen approach

PVE mounts the image via the kernel (loop/NBD + the OS filesystem drivers).
On Windows that would mean a WinFsp/Dokan FUSE mount or attaching the image
as a virtual disk — deep OS hooks, a driver dependency, admin rights, and a
surface for the exact "users going crazy on restores" footgun the policy
system exists to contain.

**Chosen approach (per requirement "a sandboxed mount that doesn't have to
interact with Windows too deep — only the software needs access"):**
**userspace filesystem parsing, in-process, no kernel mount.**

Keep the existing `FIDXServer` `io.ReaderAt`. Layer a pure-Go filesystem
reader on top of it:

```
PBS chunks ──► FIDXServer (io.ReaderAt, exists)
                   │
                   ▼
            partition table parser (GPT/MBR)  ── new, pure Go
                   │
                   ▼
            filesystem reader (NTFS first)     ── new, pure Go library
                   │
                   ▼
            virtual file tree  →  same SnapshotEntry shape the GUI/portal
                                   already render for directory backups
```

Nothing touches the Windows filesystem, no driver, no mount, no admin. The
software reads bytes and interprets them itself — precisely the sandboxed
model asked for. It also works **identically on the PHP control server side**
if we ever reimplement the reader there, but see "Control server" below for
the cheaper path.

### Libraries (pure Go, no cgo, no OS mount)

- Partition tables: `github.com/diskfs/go-diskfs` (MBR + GPT, pure Go) — or a
  ~150-line hand-rolled GPT parser (GPT is simple; we control the scope).
- **NTFS** (the case that matters — Windows machines): `github.com/Velocidex/go-ntfs`
  (used by Velociraptor; pure Go; read-only; reads `$MFT` for the file tree
  and file contents via an `io.ReaderAt`). This is the linchpin — it takes an
  `io.ReaderAt` of the partition, which is exactly what `FIDXServer` provides
  once offset by the partition start.
- FAT/exFAT (EFI system partitions, USB media): `go-diskfs` covers FAT.
- ext4 (Linux guests, later): `github.com/dsoprea/go-ext4` if needed.

Scope v1 to **GPT/MBR + NTFS + FAT**, read-only, listing + single-file and
folder(zip) extraction. That covers essentially every Windows machine backup.

### Effort estimate

- Partition parsing + wiring `FIDXServer` → per-partition `io.ReaderAt`: small.
- NTFS listing (walk `$MFT`, build tree) → `SnapshotEntry`: medium.
- NTFS file extraction (resolve data runs, stream out): medium, mostly the
  library's job.
- GUI: teach the Browse tab that a snapshot has *disks → partitions → files*
  (three levels above the existing file tree) and reuse the existing tree UI
  below that. Medium.
- Risk: encrypted archives (key never leaves client — already handled), and
  BitLocker-encrypted NTFS volumes (out of scope v1; detect and show a clear
  "volume is BitLocker-encrypted" node rather than failing).

## Control server (NimbusControl portal) browsing

The portal already browses **directory** backups via PBS's own
`catalog` + `pxar-file-download` endpoints (built in v0.5) — PBS does the work
server-side. **PBS has no equivalent for image backups** (no file API over a
fixed-index disk image), so the portal has the same fundamental gap.

Two options, to decide later (NOT in this push):
1. **Reimplement the FIDX reader + partition/NTFS parsing in PHP** — large,
   duplicates the Go work, and NTFS parsing in PHP is unpleasant.
2. **Delegate to the agent**: the portal asks the agent (which already has the
   Go reader, the PBS creds, and the policy gate) to list/extract a path from
   an image backup, streaming the result back through the existing command
   channel. Far less code, keeps one implementation, and respects the
   `file_restore` policy automatically. **Recommended.**

Either way, image browsing in the portal is a follow-up; this push delivers
the *client-side* capability and the type distinction in both UIs.

## What ships in THIS push (agreed "all in one")

1. Theme accents reworked to swatches over Auto/Light/Dark; Proxmox orange
   default. (client + portal)
2. Control-server card moved to the Servers tab. (client)
3. Same theming system ported into the NimbusControl portal.
4. **Type distinction groundwork**: the Browse tab and the portal visibly
   distinguish *file* (directory) vs *volume* (machine/image) backups, and
   image backups show their disks/partitions with an honest "file browsing for
   image backups is coming" affordance where the deep parser isn't wired yet —
   OR, if the NTFS reader integration proves quick, the full tree. I will build
   the parser as far as it goes cleanly and gate the rest behind a clear state
   rather than ship something half-working.
