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

See [ARCHITECTURE](../../ARCHITECTURE.md) for the service ownership rules,
compile views and required tests. `server_smoke_test.go` and
`storage_identity_test.go` exercise the real local HTTP boundary.
