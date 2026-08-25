package controlplane

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// Restore reporting (V4-SPEC §9.7 step 2).
//
// The property that matters most in this file is NOT that a report is sent.
// It is that the wire shape the server validates is the shape this package
// produces, because a report the server refuses leaves the release audit with
// nothing to match and nobody is told: the restore succeeded, the row is
// missing, and the only symptom is a claim that reads 'asserted' forever.

// The server refuses anything else, and refuses it AFTER the restore has
// already happened -- so a generator that drifts costs an audit record with no
// visible failure anywhere.
var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestARestoreIDIsTheShapeTheServerAccepts(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id, err := NewRestoreUUID()
		if err != nil {
			t.Fatalf("NewRestoreUUID: %v", err)
		}
		if !uuidRE.MatchString(id) {
			t.Fatalf("%q is not the lowercase hyphenated form the server's regex requires", id)
		}
		if id[14] != '4' {
			t.Errorf("%q is not version 4; the version nibble is %q", id, id[14])
		}
		if !strings.ContainsRune("89ab", rune(id[19])) {
			t.Errorf("%q has variant nibble %q, want one of 8/9/a/b", id, id[19])
		}
		if seen[id] {
			t.Fatalf("NewRestoreUUID returned %q twice in 200 draws", id)
		}
		seen[id] = true
	}
}

// THE OUTCOME POLICY. Each case is a different wrong answer that would look
// fine in a portal until someone relied on it.
func TestHowARestoreEndingIsReported(t *testing.T) {
	if st, sum := RestoreOutcome(nil, false); st != RestoreSuccess || sum != "" {
		t.Errorf("a restore that returned no error reported %q/%q", st, sum)
	}

	// A cancellation returns a non-nil error too, so an implementation that
	// checked the error first would file every cancelled restore as failed --
	// a red row for something the operator chose to do.
	cancelled := errors.New("volume file restore cancelled")
	if st, sum := RestoreOutcome(cancelled, true); st != RestoreCancelled {
		t.Errorf("a cancellation reported %q, want %q", st, RestoreCancelled)
	} else if sum != "" {
		t.Errorf("a cancellation carried an error summary %q; it did not fail", sum)
	}

	boom := errors.New("destination is read-only")
	st, sum := RestoreOutcome(boom, false)
	if st != RestoreFailed {
		t.Errorf("a failure reported %q, want %q", st, RestoreFailed)
	}
	if sum != boom.Error() {
		t.Errorf("summary = %q, want the error text %q", sum, boom.Error())
	}
}

// Capped HERE, not left to the server's column truncation: there is no reason
// to push a megabyte of error text across the wire to be cut at the far end,
// and a summary that survives this is one the portal shows whole.
func TestALongFailureIsCappedBeforeItIsSent(t *testing.T) {
	long := errors.New(strings.Repeat("x", 9000))
	_, sum := RestoreOutcome(long, false)
	if len(sum) != restoreSummaryMax {
		t.Errorf("summary length = %d, want %d", len(sum), restoreSummaryMax)
	}
}

// THE WIRE. Field names are what the server validates on; a rename here is
// silent at compile time and fatal at run time.
func TestTheReportGoesToTheEndpointInTheShapeTheServerReads(t *testing.T) {
	var gotPath, gotAuth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"applied":true}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, AgentID: 7, Secret: "s3cret"}
	err := c.ReportRestore(RestoreReport{
		RestoreUUID: "11111111-2222-4333-8444-555555555555",
		Kind:        RestoreVolume,
		Source:      RestoreConsole,
		Status:      RestoreRunning,
		StartedAt:   "2026-08-25T04:00:00Z",
		Snapshot:    "host/desktop-ifre2/2026-08-20T04:00:00Z",
	})
	if err != nil {
		t.Fatalf("ReportRestore: %v", err)
	}
	if gotPath != "/api/agent/v1/restores" {
		t.Errorf("posted to %q", gotPath)
	}
	if gotAuth == "" {
		t.Error("the report went out unauthenticated; the server would refuse it")
	}
	for _, f := range []string{"restore_uuid", "kind", "source", "status", "started_at", "snapshot"} {
		if _, ok := body[f]; !ok {
			t.Errorf("the body has no %q field; the server validates on that name", f)
		}
	}
	// An unfinished restore must not claim a finish time. The server would
	// store it and the portal would show a running restore that already ended.
	if _, ok := body["finished_at"]; ok {
		t.Error("a running report carried finished_at")
	}
	if body["kind"] != "volume" {
		t.Errorf("kind serialised as %v, want \"volume\"", body["kind"])
	}
}

// There is deliberately NO restore kind meaning "a whole partition". The
// client restores files and folders; it cannot write an image over a live disk
// and must not be able to. Pinned so that adding one is a decision somebody
// makes on purpose, in front of this comment.
func TestOnlyFileRestoreKindsExist(t *testing.T) {
	for _, k := range []RestoreKind{RestoreArchive, RestoreVolume} {
		s := strings.ToLower(string(k))
		for _, forbidden := range []string{"partition", "image", "disk", "volume-restore"} {
			if s == forbidden {
				t.Errorf("restore kind %q names restoring a whole %s", k, forbidden)
			}
		}
	}
	if string(RestoreVolume) != "volume" || string(RestoreArchive) != "archive" {
		t.Errorf("the kinds drifted from the server's CHECK constraint: %q, %q",
			RestoreArchive, RestoreVolume)
	}
}
