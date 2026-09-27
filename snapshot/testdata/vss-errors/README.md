# VSS failure text, captured (dev rule 25)

Real messages the client produced on the test VM (DESKTOP-ND3EQI2), read back
from the control server's database on 2026-09-27. Not written for the test.

| File | Source |
|---|---|
| `run113-initializeforbackup-unexpected.txt` | `agent_log_entries` row 24 (agent 1, run 113, 2026-09-23 04:00 UTC): the scheduled image run that failed at `InitializeForBackup` with `VSS_E_UNEXPECTED` (ledger F-34) |
| `run1790421299-4-isvolumesupported-object-not-found.txt` | `agent_log_entries` row 68 (agent 1, run-1790421299-4, 2026-09-26 11:15 UTC): the R1 race run, `VSS_E_OBJECT_NOT_FOUND` at `IsVolumeSupported` after the test disk was detached |
| `server-event-403.txt` | `backup_run_events` row 403 (run 179): what the portal showed for that failure, the prefix and the TAIL of the hex (ledger F-43) |

The parenthesised hex in the first two is not an HRESULT: go-vss formats its
HRESULT (a Stringer) with `%#x`, which hex-encodes the description string.
`snapshot/vsserr.go` decodes it back to the name and code.
