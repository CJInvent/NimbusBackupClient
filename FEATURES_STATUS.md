# Nimbus Backup — Feature Status

**Current version:** v0.2.128
**Status:** GUI production-ready · Windows service stable
**Developer/architecture reference:** [`ARCHITECTURE.md`](ARCHITECTURE.md)

Legend: ✅ implemented & exercised · 🧪 implemented, needs on-hardware validation · 🔭 planned

---

## GUI & core (Wails v2 + React)

| Feature | Status | Notes |
|---|---|---|
| Modern GUI, PBS config, connection test | ✅ | Real auth before backup |
| Multi-language interface | ✅ | **fr / en / es**, full key parity |
| Localized backend errors | ✅ | `[NB-xxxx]` codes, translated in GUI, English in logs |
| Real-time progress: %, speed, ETA | ✅ | Works in service mode (live stats bridge) |
| Multi-PBS servers + default selection | ✅ | See `MULTI_PBS_GUIDE.md` |
| TOFU certificate fingerprint pinning | ✅ | Delegated to service |

## Backup — directory mode

| Feature | Status | Notes |
|---|---|---|
| Multi-folder backup, PXAR + DIDX dedup | ✅ | |
| Background size scan for ETA | ✅ | |
| Junction/locked-file skip + report | ✅ | |
| Custom exclusions (wildcards) | ✅ | |
| Auto-split for large first backups | ✅ | Resumable partial bins |

## Backup — machine mode (full disk)

| Feature | Status | Notes |
|---|---|---|
| Bootable full-volume FIDX image | ✅ | VSS snapshot per volume, 4 MB fixed chunks |
| Two-stage disk sizing | ✅ | Length-info then geometry; handles VSS volume devices |
| Reader-abort before index close | ✅ | Prevents committing a truncated image |
| Concurrency guard (per-destination) | ✅ | Avoids PBS group-lock failures |
| Multi-volume single snapshot set | 🔭 | Split-volume DBs torn across volumes today |

## Restore

| Feature | Status | Notes |
|---|---|---|
| Snapshot browse + selective restore | ✅ | Tree, cache, search |
| Metadata sidecar, in-place restore | ✅ | Cross-host guard |
| Machine-image restore via nbd | 🧪 | End-to-end boot-verify not yet exercised |

## Service, scheduling, control plane

| Feature | Status | Notes |
|---|---|---|
| Windows service (LocalSystem) | ✅ | Scheduler, privileged VSS |
| Service is single writer of config | ✅ | GUI delegates all config writes |
| Authenticated local API (18765) | ✅ | Token + constant-time compare |
| Token file ACL (well-known SIDs) | 🧪 | icacls; domain-independent |
| Scheduled jobs (daily, startup) | ✅ | CRUD via API |
| Upload bandwidth limit | ✅ | Token bucket; GUI + config flag |
| Failure-alert emails with log tails | 🧪 | SMTP STARTTLS/TLS; needs a mail server to confirm |

## Security

| Feature | Status | Notes |
|---|---|---|
| Secrets encrypted at rest (AES-256-GCM) | 🧪 | `encv1:` envelope |
| DPAPI protector (machine scope) | 🧪 | Universal Windows fallback |
| TPM protector (ncrypt PCP, RSA-2048) | 🧪 | Auto-upgrade from DPAPI; round-trip verified |
| CPU speculation / Windows Update posture | 🧪 | GUI banner + startup log |

## Exchange (application-aware)

| Feature | Status | Notes |
|---|---|---|
| Detection (2007–2019) | 🧪 | Registry hive; GUI highlights if detected-but-off |
| Post-backup health tasks | 🧪 | EMS, on success only |
| Circular-logging mode query | 🧪 | Lazy EMS query |
| Post-backup log truncation | 🧪 | diskshadow writer-participating; opt-in |

🧪 items are compile/lint-verified in CI but require real Windows (and, for Exchange, a real Exchange host) to validate runtime behavior — CI cannot execute VSS/DPAPI/TPM/EMS.

See [`ARCHITECTURE.md`](ARCHITECTURE.md) §12 for the full backlog and the control-server design surface.
