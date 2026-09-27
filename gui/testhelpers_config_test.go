package main

// Shared test helpers for anything that reads or writes the config directory.
//
// These live in an UNTAGGED file on purpose. They started out inside
// secrets_smoke_windows_test.go, which meant the DEK cache reset and the
// ProgramData isolation existed only on Windows — and getConfigDir() consults
// ProgramData on every platform, so a Linux test that forgot to isolate would
// happily write master.key and backup-key.json into the developer's real home
// directory and then read another test's leftovers.

import (
	"os"
	"testing"
)

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

// captureServiceLog points the service logger at a temp file for one test and
// returns a reader for what was written, so a test can assert on the lines a
// change must (or must not) produce -- the "logs once" rules are only rules if
// something counts.
func captureServiceLog(t *testing.T) func() string {
	t.Helper()
	path := t.TempDir() + "/service-test.log"
	lg, err := NewRotatingLogger(path, MaxLogSize, MaxLogFiles)
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	prev := serviceLogger
	serviceLogger = lg
	t.Cleanup(func() { serviceLogger = prev })
	return func() string {
		b, _ := os.ReadFile(path)
		return string(b)
	}
}
