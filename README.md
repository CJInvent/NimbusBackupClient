# Nimbus Backup

Windows backup agent for **Proxmox Backup Server** (PBS), built for MSP
deployment: a native GUI + Windows service pair that backs up directories or
whole disks to PBS, restores at file granularity from either, and reports into
[NimbusControl](https://github.com/CJInvent/NimbusControl) for fleet
monitoring.

> Forked long ago from `tizbac/proxmoxbackupclient_go`; the codebase has since
> been substantially rewritten.

## What it does

**Backup**
- Directory backups (pxar) and whole-disk **volume backups** (raw image, VSS
  snapshot based), one-shot or scheduled.
- Optional split of large initial seeds into resumable parts.
- Upload bandwidth cap (`upload_limit_mbps`).
- Failure-alert emails; job history; multi-PBS routing (different paths to
  different PBS datastores).

**Restore & browse**
- Directory backups: browse the snapshot tree, restore in place or to an
  alternate location.
- **Volume backups: browse INSIDE the disk image without restoring it.**
  Partitions are parsed in userspace (GPT/MBR + NTFS, FAT12/16/32, exFAT —
  see `imagebrowse/`), the file table is fetched with an exact chunk plan
  (fragmentation-proof), and the tree is served one directory at a time —
  nothing hidden, sortable by name/date/size.
- **One unified restore workflow** for both backup types: pick files/folders,
  choose options, one button. Options grey out with a reason when the source
  can't provide them:
  | Option | Directory backup | Image (NTFS) | Image (FAT/exFAT) |
  |---|---|---|---|
  | Overwrite existing | ✅ | ✅ | ✅ |
  | Restore modification times | ✅ | ✅ | ✅ |
  | Restore NTFS permissions | ⛔ needs sidecar capture (planned) | ✅ **read from the image** (BETA) | ⛔ not stored by the format |
  | Restore alternate data streams | ⛔ needs sidecar capture (planned) | ✅ read from the image | ⛔ not stored by the format |
  | Package as ZIP | ✅ | ✅ | ✅ (metadata options grey while ZIP) |
- Download bandwidth cap (`download_limit_mbps`) — one shared token bucket
  across all concurrent chunk fetches.

**Fleet (NimbusControl)**
- Enrollment by one-time token; status, job results and alerting server-side.

## Architecture in one paragraph

Two processes from one binary: a **Windows service** (`-tags service`,
LocalSystem — owns VSS, schedules, uploads) and a **GUI** (Wails/WebView2 —
thin frontend; talks to the service over an authenticated local API). Restore
reads run over `pbscommon.FIDXReaderAt`, a lazy `io.ReaderAt` on PBS fixed
indexes with LRU chunk cache, concurrent prefetch, exact-range plan prefetch
and rate limiting — so browsing a multi-TB image moves megabytes. Full detail:
[ARCHITECTURE.md](ARCHITECTURE.md). Never reintroduce native file dialogs
(§7c) — they fault natively and kill the process.

## Modules

| Path | What it is |
|---|---|
| `gui/` | The application (GUI + service variants, Wails frontend under `gui/frontend/`) |
| `pbscommon/` | PBS protocol: sessions, chunking, DIDX/FIDX readers, prefetch, rate limit |
| `imagebrowse/` | Userspace partition + filesystem readers (NTFS/FAT/exFAT), ADS, NTFS security descriptors — see [imagebrowse/README.md](imagebrowse/README.md) |
| `controlplane/` | Agent side of NimbusControl enrollment/reporting |
| `docs/` | Deep dives: control plane, restore guide, image-browse scope |

## Building

CI (GitHub Actions) builds the MSI on every tag: `git tag vX.Y.Z && git push
origin master vX.Y.Z`. Locally:

```
cd gui/frontend && npm ci && npm run build   # webview assets
cd ..                                        # gui/
go build .                                   # GUI variant
go build -tags service .                     # service variant
```

Go ≥ 1.25. Tests: `go test ./...` in `pbscommon/` (race-detector prefetch
suites) and `imagebrowse/` (real mkfs-built fixture images in `testdata/`).

## Support notes

- ReFS and BitLocker partitions are detected and refused with a clear reason;
  full-image restore covers them. (No mature pure-Go ReFS parser exists, and
  guessing at on-disk structures in a restore tool risks corrupt files.)
- NTFS permission restore applies the DACL always; owner/group need elevation
  and downgrade to a logged warning. SACLs are not restored.
- Error codes: `NB-3xxx` in the GUI map to entries in the backup log at
  `C:\ProgramData\NimbusBackup`.
