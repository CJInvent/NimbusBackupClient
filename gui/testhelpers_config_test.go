package main

// Shared test helpers for anything that reads or writes the config directory.
//
// These live in an UNTAGGED file on purpose. They started out inside
// secrets_smoke_windows_test.go, which meant the DEK cache reset and the
// ProgramData isolation existed only on Windows — and getConfigDir() consults
// ProgramData on every platform, so a Linux test that forgot to isolate would
// happily write master.key and backup-key.json into the developer's real home
// directory and then read another test's leftovers.

import "testing"

// resetDEKCache clears the process-wide DEK cache so the next getDEK() re-reads
// master.key from the (freshly isolated) config dir.
func resetDEKCache() {
	dekMu.Lock()
	dekCached, dekProtector, dekErr = nil, "", nil
	dekMu.Unlock()
}

// isolateConfigDir points getConfigDir() at a temp directory for one test.
func isolateConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ProgramData", dir)
	resetDEKCache()
	t.Cleanup(resetDEKCache)
	return dir
}
