# imagebrowse

Read-only, pure-Go **userspace filesystem readers** for browsing inside raw
disk images — no mount, no drivers, no admin rights. Built for NimbusBackup's
volume-backup browsing: every byte is read through an `io.ReaderAt`, which in
production is a lazy PBS chunk reader, so listing a multi-TB image downloads
only the blocks actually touched.

## Layers

```
ListPartitions (partitions.go)     GPT + MBR, filesystem identification
        │
OpenFilesystem (fs.go)             dispatch on detected filesystem
        │
 ┌──────┼──────────┐
ntfs.go fat.go  exfat.go           one Filesystem interface:
                                   List / Stat / ExtractFile / UsedBytes
```

Optional capability interfaces (type-assert on the returned `Filesystem`):

| Interface | Who | What |
|---|---|---|
| `TreeLister` | NTFS | `FullTree`: whole tree from ONE sequential $MFT read (the WizTree technique) — no per-directory disk reads |
| `Planner` | NTFS | `StoragePlan`: the $MFT's size + on-disk extents, so a caller can prefetch exactly those byte ranges (fragmentation-proof) |
| `StreamLister` | NTFS | Alternate data streams: enumerate + extract named $DATA streams |
| `SecurityReader` | NTFS | Self-relative security descriptor per path — modern SecurityId→$Secure/$SDS lookup with legacy inline-0x50 fallback |

FAT12/16/32 (`fat.go`, VFAT long names) and exFAT (`exfat.go`) are hand-rolled
against the published specs — the formats are small and frozen, and a backup
agent benefits from zero extra supply chain. NTFS rides on
[go-ntfs](https://github.com/Velocidex/go-ntfs).

**Deliberately refused:** ReFS (no mature pure-Go parser; guessing risks
corrupt files in a *restore* tool) and BitLocker (encrypted). Both are
detected and reported with an actionable message.

## Guarantees, enforced by tests

- Byte-exact extraction (SHA-256-verified) across all three filesystems, on
  **real images** made with `mkfs.ntfs` / `mkfs.vfat` / `mkfs.exfat` and
  populated through kernel drivers (gzipped in `testdata/`, ~50–180 KB each).
- The NTFS fast tree returns the **identical** entry set to the recursive
  walk, and hides **nothing**: `$WINDOWS.~BT`-style user dirs and
  hidden/system files are all listed; only the reserved metafile records
  (0–23) are excluded.
- ADS enumeration/extraction verified against streams written via ntfs-3g's
  `streams_interface=windows`.
- The security-descriptor chain ($SDS scan → SecurityId → SD bytes, plus the
  legacy fallback) is exercised end-to-end on the fixture.
- Corrupt-volume behaviour: cyclic FAT chains terminate, short cluster chains
  error rather than returning silently truncated files.

## Non-goals

Writing. Ever. This package restores *from* images; it must never be able to
damage one.
