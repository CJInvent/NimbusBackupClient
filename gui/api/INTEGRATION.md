# GUI and service integration

The service owns backup execution, configuration writes and storage identity
approvals. The GUI uses the authenticated local API; an unavailable service
returns an error and never triggers in-process backup execution.

`App.StartBackup(backupType, backupDirs, diskTargets, excludeList, backupID,
useVSS, compression)` delegates to `api.Client.StartBackup`. The service
validates and executes through `runBackupPipeline`. Image targets are `boot`
or `device:v1:<sha256>` in `disk_targets`; disk numbers and letters cannot
select backup sources. Scheduler persistence uses the `diskTargets` JSON
property and the same service pipeline.

`GET /storage/identity` exposes current evidence, approved bindings and any
latched error. `POST /storage/identity` accepts a revisioned approval only for
standalone agents, subject to authentication and the existing read-only gate.
Joined agents reject local approvals and receive them from NimbusControl
check-in. The GUI calls `GetStorageIdentityStatus` and `ApproveStorageIdentity`
through this service boundary. Neither endpoint returns credentials.

Each device carries `identity_strength` (F-39/F-41, NimbusControl
`docs/V4-BETA-FIXES.md` §5): `strong` when the open device reports an NAA or
EUI-64 designator or a serial that is not a slot name, `weak` when every
designator is a slot name such as QEMU's `drive-scsi1` (a Proxmox disk with no
`serial=`), absent when there is no identity. The strength is read from the
same page-0x83 bytes as the id (`gui/storage_identity_parse.go`), and the id
itself did not change. Approvals record it: a weak binding is matched by disk
GUID, size and partitions, so the disk may move to another slot, and a clone
cannot be told from it (the documented limit; the panel says so). A strength
that changes after approval needs a new approval. A binding approved before
the field existed matches by id as before. `model` is the disk's friendly name,
display only.

See [ARCHITECTURE](../../ARCHITECTURE.md) for the service ownership rules,
compile views and required tests. `server_smoke_test.go` and
`storage_identity_test.go` exercise the real local HTTP boundary.
