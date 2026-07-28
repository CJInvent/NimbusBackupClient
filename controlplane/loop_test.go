package controlplane

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// TestCommandLifecycleIsLogged is the regression guard for a real incident:
// three run_backup commands sat at 'sent' for 15+ hours with nothing
// anywhere — client or server — showing whether the client had ever seen
// them. CheckinNow() used to log NOTHING on the command path except
// failures. This asserts every stage now leaves a trace: receipt, dispatch
// outcome, and post outcome, in that order, for both a command that
// dispatches OK and one whose handler reports failure.
func TestCommandLifecycleIsLogged(t *testing.T) {
	srv, _ := fakeServer(t)
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	resp, err := c.Enroll(EnrollRequest{Token: "good-token", Hostname: "h"})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	c.AgentID, c.Secret = resp.AgentID, resp.Secret

	a := &Agent{
		Client: c,
		HandleCommand: func(cmd Command) CommandResult {
			return CommandResult{OK: true, Result: map[string]interface{}{"note": "dispatched"}}
		},
	}

	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0) // no timestamp — makes substring assertions exact
	defer func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) }()

	a.CheckinNow()

	out := buf.String()
	checks := []string{
		"check-in delivered 1 command(s): 1:run_backup",
		"command 1 (run_backup) dispatched OK",
		"command 1 result posted (ok=true)",
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("missing log line %q\n--- full output ---\n%s", want, out)
		}
	}

	// Order matters: receipt, then dispatch, then post — each stage must
	// appear before the next so a partial log (e.g. process died mid-cycle)
	// still tells you exactly how far the command got.
	iReceipt := strings.Index(out, "delivered 1 command")
	iDispatch := strings.Index(out, "dispatched OK")
	iPost := strings.Index(out, "result posted")
	if !(iReceipt >= 0 && iReceipt < iDispatch && iDispatch < iPost) {
		t.Errorf("log lines out of order:\n%s", out)
	}
}

// TestCommandDispatchFailureIsLogged: a handler that reports failure (e.g.
// "unknown job") must log the failure AND still attempt to post the result
// — the two are independent, and a lost post must not be confused with a
// failed dispatch when reading the log after the fact.
func TestCommandDispatchFailureIsLogged(t *testing.T) {
	srv, _ := fakeServer(t)
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	resp, err := c.Enroll(EnrollRequest{Token: "good-token", Hostname: "h"})
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	c.AgentID, c.Secret = resp.AgentID, resp.Secret

	a := &Agent{
		Client: c,
		HandleCommand: func(cmd Command) CommandResult {
			return CommandResult{OK: false, Result: map[string]interface{}{"error": "unknown job: "}}
		},
	}

	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) }()

	a.CheckinNow()

	out := buf.String()
	if !strings.Contains(out, "command 1 (run_backup) dispatch FAILED") {
		t.Errorf("missing dispatch-failure log line:\n%s", out)
	}
	if !strings.Contains(out, "command 1 result posted (ok=false)") {
		t.Errorf("missing post-outcome log line for a failed dispatch:\n%s", out)
	}
}
