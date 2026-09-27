# `vssadmin list writers` output

Each `*.txt` is one listing; `*.want` beside it names the writers that must
parse as FAILED, one per line (empty file = none), and the first line of
`*.want` may be `writers=N`, the total number of writers in the listing.
`snapshot/vss_writers_test.go` runs every pair.

| File | Provenance |
|---|---|
| `en-reconstructed-2026-09-14-2300.txt` | **RECONSTRUCTED, not raw.** The service log of 2026-09-14 23:00 on the test VM recorded five lines `VSS writer '<name>': state [10] Failed, last error: Timed out`, which the old English parser built from exactly the label lines `Writer name: '<name>'`, `State: [10] Failed` and `Last error: Timed out`. Those label lines are reproduced here; the `Writer Id` lines and the banner the old parser discarded are not. Replace with a raw capture (`docs/reverify-2026-09-27/C0-Capture.ps1 -Part Vss` in NimbusControl) and keep this only if the raw capture has no failed writer |

**Wanted, not yet captured:** a raw English listing, and one from a Windows
display language other than English (French or Spanish). The non-English one
is what proves ledger F-36's language independence; until it is here that
claim rests on the parser reading only structure (a quoted name, a bracketed
state number, the line after it), which the English fixture exercises.
