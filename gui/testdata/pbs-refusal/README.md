# PBS refusal captures (dev rule 25)

Real lines from the test VM (DESKTOP-ND3EQI2), not written for the test:

| File | Source |
|---|---|
| `t54-directory-previous-401.txt` | `backup-20260924-102735-beta-reactivation-proof.log.gz`, T5.4 (retire then reactivate, 2026-09-24): the directory engine's first request after PBS had revoked the token. The line starts with the engine's own prefix `No previous backup found (first backup?): ` |
| `t54-directory-session-lost.txt` | the same run, the error that followed and bought the 25-minute wait |
| `offline-directory-previous.txt` | `backup-20260924-092756-beta-offline-queue-1.log.gz`: the same first request with the network down |
| `reenroll-image-401.txt` | server `agent_log_entries` row 74 (agent 11, run ab49237b..., 2026-09-26): the image engine's refusal after re-enrollment, as pushed |

The session cookie in the 401 lines was already redacted by the client's log
redaction when captured. `gui/pbs_credential_test.go` reads these files.
