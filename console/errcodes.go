package main

// The error codes the console returns itself (docs/V4-CONSOLE.md §9, the
// errcodes.go split). Every other coded error the screens show is the
// service's own sentence, passed through.
//
// Contract, shared with the service's gui/errcodes.go: a user-facing error is
// "[NB-xxxx] <english base>", optionally followed by " :: <detail>". The
// front end recognizes the code and shows the active language's text
// (translations.js err_NBxxxx), keeping the detail. A code the service also
// declares must carry the same text; TestConsoleCodesMatchTheService pins it.
const (
	// The connection test failed because the server's certificate did not
	// verify and no fingerprint is pinned: the case the setup screen offers to
	// resolve by pinning one (trust on first use).
	errCertUntrusted = "[NB-1011] the server's certificate is not trusted and no fingerprint is pinned"

	// The service did not answer. The console has no fallback.
	errServiceUnavailable = "[NB-2011] the Nimbus service is not reachable - backups run in the service; check that it is started"

	// The restore screens' own argument checks, made before a job exists.
	errSnapshotList       = "[NB-3001] listing snapshots failed"
	errBackupIDRequired   = "[NB-3007] backup ID required"
	errSnapshotIDRequired = "[NB-3008] snapshot ID required"
	errDestPathRequired   = "[NB-3009] destination folder required"
)
