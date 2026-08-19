package main

import (
	"os"
	"strings"
	"testing"
)

// SOURCE-LEVEL PINS for the four wiring points phase F depends on.
//
// WHY THIS SHAPE, HONESTLY. Everything else about the backup key is pinned by
// behaviour: the decision is a pure function with a sweep over its whole input
// space, the store has its failure modes sabotage-tested, the wire contract has
// a fake server. The four facts below are about a CALL SITE inside
// runBackupPipeline and two Windows-only engines — code that needs an App, a
// live PBS and (for one of them) a physical disk to execute. There is no
// harness here that reaches them, and this codebase has already learned what
// happens without one: a unit-tested helper nobody calls passes exactly as well
// as one everybody calls, and reverting UploadChunk to encrypt-only failed
// nothing at all because every test exercised the helper directly.
//
// So these read the source. That is weaker than executing it and is not
// pretending otherwise — a pin, in the same spirit as the `nm` assertion that
// the GUI binary links no backup engine. They catch the specific regression
// that costs the most and is the least visible: the gate quietly stopping being
// called, or the escrow blob drifting to after the manifest, neither of which
// fails anything until a customer needs a restore.
//
// They should be replaced by an end-to-end run against a real PBS when phase E's
// outstanding caveat is closed.

func sourceOf(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

// mustPrecede asserts a appears before b, and that both appear at all — an
// ordering check over two absent strings passes vacuously.
func mustPrecede(t *testing.T, src, file, a, b, why string) {
	t.Helper()
	ia, ib := strings.Index(src, a), strings.Index(src, b)
	if ia < 0 {
		t.Fatalf("%s: %q is gone. %s", file, a, why)
	}
	if ib < 0 {
		t.Fatalf("%s: %q is gone — the ordering assertion below would pass vacuously", file, b)
	}
	if ia > ib {
		t.Errorf("%s: %q now comes AFTER %q. %s", file, a, b, why)
	}
}

// The gate runs, and it runs before the engine. A pipeline that assembles
// options without consulting it backs up in the clear for an org that
// encrypts, and reports success.
func TestPipelineCallsTheGateBeforeExecuting(t *testing.T) {
	src := sourceOf(t, "backup_pipeline.go")

	if !strings.Contains(src, "a.resolveBackupKeyForRun(runUUID)") {
		t.Fatal("backup_pipeline.go no longer calls the backup-key gate; " +
			"every encrypted org would silently back up in the clear")
	}
	if !strings.Contains(src, "opts.BackupKey, opts.EscrowBlob = key, escrow") {
		t.Error("the gate's result is no longer handed to the engine")
	}
	mustPrecede(t, src, "backup_pipeline.go",
		"a.resolveBackupKeyForRun(runUUID)", "RunBackupInline(opts)",
		"refusing after the engine has uploaded data refuses nothing")
	mustPrecede(t, src, "backup_pipeline.go",
		"a.resolveBackupKeyForRun(runUUID)", "RunMachineBackup(opts)",
		"refusing after the engine has uploaded data refuses nothing")

	// A refusal must be REPORTED, not just returned. A machine that stops
	// backing up has to be visible in the portal — silence and success look
	// identical from a dashboard, which is the whole reason the gate exists.
	mustPrecede(t, src, "backup_pipeline.go",
		"attachControlPlaneHooks(&opts)", "a.resolveBackupKeyForRun(runUUID)",
		"a refusal before the reporters are attached is an invisible one")
	if !strings.Contains(src, "cpFinish(keyErr)") || !strings.Contains(src, "runFinish(keyErr)") {
		t.Error("a gate refusal no longer finalizes the run; it would sit in 'preparing' forever")
	}
}

// Both engines must apply the key, and both must write the escrow blob BEFORE
// the manifest. The manifest records the files it has seen, so a blob uploaded
// after it is absent from what /finish validates — the bytes land in the
// snapshot and no restore tool ever looks for them.
func TestEnginesApplyTheKeyAndEscrowBeforeTheManifest(t *testing.T) {
	for _, file := range []string{"backup_inline.go", "machine_backup_windows.go"} {
		src := sourceOf(t, file)

		if !strings.Contains(src, "client.SetCryptKey(opts.BackupKey)") {
			t.Errorf("%s: the engine no longer encrypts with the key the gate resolved", file)
		}
		if !strings.Contains(src, "client.SetEscrowBlob(opts.EscrowBlob)") {
			t.Errorf("%s: the engine no longer records the escrow blob", file)
		}
		mustPrecede(t, src, file,
			"client.UploadEscrowBlobIfEncrypted()", "client.UploadManifest()",
			"a blob written after the manifest is absent from the manifest that /finish validates")
	}
}
