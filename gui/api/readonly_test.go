package api

import (
	"net/http"
	"testing"
)

// The read-only lockdown boundary.
//
// This is the control, not the presentation: the GUI hiding its buttons is
// cosmetic, and what these tests pin is that the SERVICE refuses the call. The
// adversary they are written against is a modified front end holding a valid
// local API token — which is exactly the thing an org that sets `gui_read_only`
// is worried about.

func lockedTestServer(t *testing.T, locked bool) (*Server, *http.Client, string) {
	t.Helper()
	s, ts := newRunsTestServer(t)
	s.SetLockedFunc(func() bool { return locked })
	return s, ts.Client(), ts.URL
}

func req(t *testing.T, c *http.Client, method, url string, token bool) int {
	t.Helper()
	r, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token {
		r.Header.Set(tokenHeader, testToken)
	}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	code := resp.StatusCode
	_ = resp.Body.Close()
	return code
}

func TestLockedAgentRefusesEveryMutatingRoute(t *testing.T) {
	_, c, base := lockedTestServer(t, true)

	// Every route the mux serves that is not a reader. If a route is added
	// to setupRoutes and not here, the allowlist still refuses it — that is
	// the point of the allowlist — but this list is what proves the refusal
	// actually happens rather than being assumed.
	mutating := []struct{ method, path string }{
		{http.MethodPost, "/backup"},
		{http.MethodPost, "/backup/cancel"},
		{http.MethodPost, "/jobs/create"},
		{http.MethodPost, "/jobs/update"},
		{http.MethodDelete, "/jobs/delete/abc"},
		{http.MethodPost, "/pbs/fingerprint"},
		{http.MethodPost, "/pbs/save"},
		{http.MethodDelete, "/pbs/delete/abc"},
		{http.MethodPost, "/pbs/default"},
		{http.MethodPost, "/config/save"},
		{http.MethodPost, "/controlplane/save"},
	}
	for _, m := range mutating {
		if code := req(t, c, m.method, base+m.path, true); code != http.StatusForbidden {
			t.Errorf("%s %s = %d while locked, want 403", m.method, m.path, code)
		}
	}
}

func TestLockedAgentStillServesTheStatusPanel(t *testing.T) {
	s, c, base := lockedTestServer(t, true)
	id := s.Runs().Begin(TriggerSchedule, "job-7", "Nightly-C", "WS-01", "machine")

	// Everything the panel needs, and nothing else. A lockdown that also
	// blinded the technician would push every question to the MSP helpdesk.
	for _, path := range []string{
		"/status", "/runs/active", "/runs/recent", "/runs/" + id,
	} {
		if code := req(t, c, http.MethodGet, base+path, true); code != http.StatusOK {
			t.Errorf("GET %s = %d while locked, want 200 — the panel must still render", path, code)
		}
	}
}

func TestLockdownIsAnAllowlistNotADenylist(t *testing.T) {
	_, c, base := lockedTestServer(t, true)

	// A route nobody has thought about yet must be REFUSED, not permitted.
	// Enumerating the mutating routes instead would leave every future
	// endpoint open until someone remembered to list it.
	for _, path := range []string{"/some/future/route", "/jobs", "/backup"} {
		if code := req(t, c, http.MethodGet, base+path, true); code != http.StatusForbidden {
			t.Errorf("GET %s = %d while locked, want 403 — unknown routes must default to refused", path, code)
		}
	}
}

func TestLockdownPinsTheMethodNotJustThePath(t *testing.T) {
	_, c, base := lockedTestServer(t, true)

	// Every allowed route is a reader today. If one grows a POST branch
	// later, that must not quietly widen what a locked agent will do.
	for _, path := range []string{"/status", "/runs/active", "/runs/recent"} {
		if code := req(t, c, http.MethodPost, base+path, true); code != http.StatusForbidden {
			t.Errorf("POST %s = %d while locked, want 403", path, code)
		}
	}
}

func TestUnlockedAgentIsUnaffected(t *testing.T) {
	_, c, base := lockedTestServer(t, false)

	// The lockdown must be inert when the policy does not set it — including
	// on routes that would be refused if it did.
	if code := req(t, c, http.MethodGet, base+"/status", true); code != http.StatusOK {
		t.Errorf("GET /status unlocked = %d, want 200", code)
	}
	// A mutating route reaches its handler; what it answers is the handler's
	// business, but it must not be 403 from the lockdown.
	if code := req(t, c, http.MethodPost, base+"/jobs/create", true); code == http.StatusForbidden {
		t.Error("POST /jobs/create was refused by the lockdown while unlocked")
	}
}

func TestLockdownDefaultsToUnlockedWhenNeverWired(t *testing.T) {
	// A nil predicate is not a locked agent. Defaulting the other way would
	// brick the console of every unmanaged install, and of any managed one
	// whose service starts before its first check-in.
	s, ts := newRunsTestServer(t)
	if s.IsLocked() {
		t.Fatal("a server with no lock predicate reports itself locked")
	}
	if code := req(t, ts.Client(), http.MethodPost, ts.URL+"/jobs/create", true); code == http.StatusForbidden {
		t.Error("an unwired server refused a mutating call")
	}
}

func TestAuthIsCheckedBeforeLockdown(t *testing.T) {
	// An unauthenticated caller must get 401 and learn nothing. 403 would
	// tell an unauthenticated prober that this machine is under management
	// and locked, which is a fact about the customer, not about the request.
	_, c, base := lockedTestServer(t, true)
	if code := req(t, c, http.MethodPost, base+"/config/save", false); code != http.StatusUnauthorized {
		t.Errorf("unauthenticated POST while locked = %d, want 401 not 403", code)
	}
}

func TestLockStateIsReReadPerRequest(t *testing.T) {
	// The policy arrives on a check-in. An org that locks a machine expects
	// that on the next call, not on the next service restart.
	s, ts := newRunsTestServer(t)
	locked := false
	s.SetLockedFunc(func() bool { return locked })

	if code := req(t, ts.Client(), http.MethodPost, ts.URL+"/pbs/save", true); code == http.StatusForbidden {
		t.Fatal("refused before the policy locked anything")
	}
	locked = true
	if code := req(t, ts.Client(), http.MethodPost, ts.URL+"/pbs/save", true); code != http.StatusForbidden {
		t.Errorf("still permitted after the policy locked the agent = %d, want 403", code)
	}
}

func TestReadOnlyAllowedIsPurelyAboutMethodAndPath(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/status", true},
		{http.MethodHead, "/status", true},
		{http.MethodPost, "/status", false},
		{http.MethodGet, "/runs/active", true},
		{http.MethodGet, "/runs/recent", true},
		{http.MethodGet, "/runs/run-1-2", true},
		{http.MethodGet, "/backup/status/job-1", true},
		{http.MethodGet, "/controlplane/status", true},
		{http.MethodGet, "/controlplane/save", false},
		{http.MethodGet, "/backup", false},
		{http.MethodGet, "/jobs", false},
		{http.MethodDelete, "/runs/run-1-2", false},
	}
	for _, c := range cases {
		if got := readOnlyAllowed(c.method, c.path); got != c.want {
			t.Errorf("readOnlyAllowed(%s, %s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}
