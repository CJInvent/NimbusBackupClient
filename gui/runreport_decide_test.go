package main

import (
	"errors"
	"strings"
	"testing"
)

// V4-BETA-FIXES §4 (CJ, 2026-09-25): a backup completed from a snapshot with
// failed VSS writers is Warning, not Success, with the writers named.
func TestFailedWritersTurnASuccessIntoWarning(t *testing.T) {
	ws := vssWriterSummary([]string{"SqlServerWriter", "Registry Writer"})
	if ws != "VSS writer(s) failed: SqlServerWriter, Registry Writer; application consistency not guaranteed" {
		t.Fatalf("summary: %q", ws)
	}
	ok := &BackupStatus{Outcome: OutcomeVerifiedSuccess, Message: "Backup completed"}
	if k, s := decideRunReport(ok, ""); k != reportSuccess || s != "" {
		t.Fatalf("clean run: %v %q", k, s)
	}
	if k, s := decideRunReport(ok, ws); k != reportWarning || s != ws {
		t.Fatalf("writers failed: %v %q", k, s)
	}
	// The machine engine reports through its error return only.
	if k, s := decideFinalReport(nil, ws); k != reportWarning || s != ws {
		t.Fatalf("image run with failed writers: %v %q", k, s)
	}
	if k, _ := decideFinalReport(nil, ""); k != reportSuccess {
		t.Fatalf("clean image run: %v", k)
	}
	// Read errors AND writer failures: both are said, writers first.
	partial := &BackupStatus{Outcome: OutcomePartial, Message: "2 files skipped", SkippedReadError: []FileIssue{{Path: "a", Reason: "locked"}}}
	if k, s := decideRunReport(partial, ws); k != reportWarning || !strings.HasPrefix(s, ws+"; ") {
		t.Fatalf("partial + writers: %v %q", k, s)
	}
}

// The server prefixes "Snapshot creation failed: ", so the report carries the
// cause only -- not the marker, and not the marker twice as it once did
// (ledger F-43: "VSS snapshot creation failed: VSS snapshot creation failed: ...").
func TestAVSSFailureReportsTheCauseOnly(t *testing.T) {
	cause := "VSS_E_UNEXPECTED (0x80042302) during InitializeForBackup: A volume shadow copy service (VSS) component encountered an unexpected error."
	for _, msg := range []string{
		"Failed to backup PhysicalDrive0: " + vssCreateFailedMarker + ": " + cause,
		"[NB-2001] Backup failed - see backup log in C:\\ProgramData\\NimbusBackup :: Failed to backup PhysicalDrive0: " + vssCreateFailedMarker + ": " + cause,
		vssCreateFailedMarker + ": " + cause,
	} {
		if k, s := decideFinalReport(errors.New(msg), ""); k != reportVSSFailed || s != cause {
			t.Errorf("%q -> %v %q", msg, k, s)
		}
		if k, s := decideRunReport(&BackupStatus{Outcome: OutcomeFailed, Message: msg}, ""); k != reportVSSFailed || s != cause {
			t.Errorf("OnResult %q -> %v %q", msg, k, s)
		}
	}
	if k, _ := decideFinalReport(errors.New("PBS rejected the backup session: x"), "writers"); k != reportFailed {
		t.Error("an ordinary failure is Failed, whatever the writers did")
	}
}
