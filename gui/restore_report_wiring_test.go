package main

// Every restore is reported (V4-SPEC §9.7 step 2).
//
// A SOURCE PIN, and its limits are stated rather than glossed: the dispatch it
// checks lives in a `service`-tagged file this test cannot call into, so what
// follows catches deletion, rename and a new restore path added without
// reporting. It does not catch a reporter that runs and does the wrong thing --
// that is controlplane/restorereport_test.go's job, which is why the outcome
// policy was moved into that package instead of being inlined here.
//
// WHY PIN IT AT ALL. An unreported restore is invisible in exactly one
// direction: the restore works, the user is happy, and the server's release
// audit is left with a key release it cannot match. The claim reads 'asserted'
// forever and nothing anywhere reports a failure. There is no runtime symptom
// to notice, so the check has to be structural.

import (
	"strings"
	"testing"
)

func TestEveryRestoreOpReportsItself(t *testing.T) {
	src := sourceOf(t, "restore_service.go")

	// The two ops that WRITE FILES onto this machine out of a snapshot. A
	// browse, a listing or a search reads without extracting and is not a
	// restore; a download is deliberately not in this list either, and if that
	// changes it should change here in front of this comment.
	for _, op := range []string{"opRestore", "opVolumeFileRestore"} {
		idx := strings.Index(src, "case "+op+":")
		if idx < 0 {
			t.Fatalf("restore_service.go has no case for %s; this test is pinning nothing", op)
		}
		// The case body runs until the next `case` at the same level.
		rest := src[idx+len("case "+op+":"):]
		if next := strings.Index(rest, "\n\tcase "); next >= 0 {
			rest = rest[:next]
		}
		if !strings.Contains(rest, "reportedRestore(") {
			t.Errorf("%s restores files without reporting the restore. The release audit is then "+
				"left with a key release it cannot match, and NOTHING fails visibly: the restore "+
				"works, the claim reads 'asserted' forever (V4-SPEC 9.7)", op)
		}
	}
}

// The report must not be able to change what the caller sees. A restore that
// failed because telemetry was down is the worst possible failure for this
// product -- recovery is the moment it exists for.
func TestReportingCannotFailARestore(t *testing.T) {
	src := sourceOf(t, "restore_report_service.go")

	if !strings.Contains(src, "func reportedRestore") {
		t.Fatal("reportedRestore is gone; restore_service.go's calls cannot be doing what this file promises")
	}
	// finish() returns nothing at all. A signature that could return an error
	// is a signature somebody will eventually propagate.
	if !strings.Contains(src, "func (r *restoreReporter) finish(restoreErr error, items int, bytes int64) {") {
		t.Error("finish's signature changed. It returns NOTHING on purpose: an error it could " +
			"return is an error a caller will eventually surface, during a recovery, over telemetry")
	}
	// The restore's own error is what comes back, unmodified.
	if !strings.Contains(src, "\terr := fn()\n\trep.finish(err, items, 0)\n\treturn err\n") {
		t.Error("reportedRestore no longer returns the restore's own error untouched")
	}
}
