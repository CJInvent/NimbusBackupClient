package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The routes the console still lacked (docs/V4-CONSOLE.md §4.1, phase C1):
// /pbs/servers, /pbs/test, /pbs/discover, /disks, /exchange, and /status's
// security_warnings. Each is driven over real HTTP through the real middleware
// and the client methods the console will call in C2, so a test here is the
// whole round trip rather than a handler called in isolation.

// consoleStub is the smoke suite's handler plus the two optional interfaces
// these routes type-assert for.
type consoleStub struct {
	*stubHandler

	mu         sync.Mutex
	servers    PBSServersResponse
	testResult map[string]PBSTestResponse
	testErr    error
	discovered string
	discErr    error
	disks      []PhysicalDisk
	disksErr   error
	exchange   ExchangeStatus
	logMode    ExchangeLogMode
	logQueried int
	warnings   []string
}

func newConsoleStub() *consoleStub {
	return &consoleStub{stubHandler: newStub(), testResult: map[string]PBSTestResponse{}}
}

// set changes the stub under its lock: the handler reads these fields on the
// server's goroutines, and -race does not count a socket as synchronization.
func (c *consoleStub) set(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f()
}

func (c *consoleStub) logQueries() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logQueried
}

func (c *consoleStub) PBSServersForAPI() PBSServersResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.servers
}

func (c *consoleStub) TestPBSServerForAPI(id string) (PBSTestResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.record("TestPBSServerForAPI:" + id)
	if c.testErr != nil {
		return PBSTestResponse{}, c.testErr
	}
	out, ok := c.testResult[id]
	if !ok {
		return PBSTestResponse{}, Refuse(http.StatusNotFound, fmt.Errorf("no PBS server %q", id))
	}
	return out, nil
}

func (c *consoleStub) DiscoverPBSFingerprintForAPI(url string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.record("DiscoverPBSFingerprintForAPI:" + url)
	return c.discovered, c.discErr
}

func (c *consoleStub) PhysicalDisksForAPI() ([]PhysicalDisk, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.disks, c.disksErr
}

func (c *consoleStub) ExchangeForAPI(withLogMode bool) ExchangeResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := ExchangeResponse{Status: c.exchange}
	if withLogMode {
		c.logQueried++
		lm := c.logMode
		out.LogMode = &lm
	}
	return out
}

func (c *consoleStub) GetSecurityWarnings() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.warnings
}

// bootConsoleServer is bootSmokeServer with the console stub, and a client
// pointed at it the way the console's is pointed at the service.
func bootConsoleServer(t *testing.T) (*httptest.Server, *consoleStub, *Client, *Server) {
	t.Helper()
	h := newConsoleStub()
	s := NewServer("127.0.0.1:0", h, testToken)
	ts := httptest.NewServer(s.authMiddleware(s.readOnlyMiddleware(s.mux)))
	t.Cleanup(ts.Close)
	c := &Client{baseURL: ts.URL, httpClient: &http.Client{Transport: headerTransport{
		key: tokenHeader, value: testToken, base: ts.Client().Transport,
	}}}
	return ts, h, c, s
}

// headerTransport sets the token header, standing in for tokenTransport (which
// reads the token from a file).
type headerTransport struct {
	key, value string
	base       http.RoundTripper
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set(h.key, h.value)
	return h.base.RoundTrip(r)
}

// newConsoleRoutes is every route C1 adds, with the method each one serves.
var newConsoleRoutes = []struct{ method, path string }{
	{http.MethodGet, "/pbs/servers"},
	{http.MethodPost, "/pbs/test"},
	{http.MethodPost, "/pbs/discover"},
	{http.MethodGet, "/disks"},
	{http.MethodGet, "/exchange"},
}

func TestConsoleRoutesRequireTheToken(t *testing.T) {
	ts, h, _, _ := bootConsoleServer(t)
	for _, r := range newConsoleRoutes {
		code, body := do(t, ts, r.method, r.path, map[string]any{"id": "pbs1", "url": "https://pbs.example:8007"})
		if code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s %s = %d, want 401 (%s)", r.method, r.path, code, body)
		}
	}
	if h.called("TestPBSServerForAPI:pbs1") || h.called("DiscoverPBSFingerprintForAPI:https://pbs.example:8007") {
		t.Error("an unauthenticated request reached the handler")
	}
}

func TestConsoleRoutesGateTheirMethod(t *testing.T) {
	ts, _, _, _ := bootConsoleServer(t)
	for _, r := range newConsoleRoutes {
		wrong := http.MethodPost
		if r.method == http.MethodPost {
			wrong = http.MethodGet
		}
		if code, _ := do(t, ts, wrong, r.path, map[string]any{}, withToken(testToken)); code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", wrong, r.path, code)
		}
	}
}

func TestConsoleRoutesAreRefusedWhileLocked(t *testing.T) {
	// None of them is on readonly.go's allowlist: a locked console is a status
	// panel, and the panel needs none of these. Refused before the handler, so
	// a locked machine does not even run a PBS test or an Exchange probe.
	ts, h, _, s := bootConsoleServer(t)
	s.SetLockedFunc(func() bool { return true })
	h.set(func() {
		h.testResult["pbs1"] = PBSTestResponse{OK: true}
	})
	for _, r := range newConsoleRoutes {
		code, _ := do(t, ts, r.method, r.path, map[string]any{"id": "pbs1", "url": "https://pbs.example"}, withToken(testToken))
		if code != http.StatusForbidden {
			t.Errorf("%s %s while locked = %d, want 403", r.method, r.path, code)
		}
	}
	if h.called("TestPBSServerForAPI:pbs1") {
		t.Error("a locked agent ran a PBS connection test")
	}
	// /status is the panel, and keeps its new field while locked.
	if code, body := do(t, ts, http.MethodGet, "/status", nil, withToken(testToken)); code != http.StatusOK ||
		!strings.Contains(body, `"security_warnings"`) {
		t.Errorf("GET /status while locked = %d %s", code, body)
	}
}

func TestConsoleRoutesAnswer501WithoutAService(t *testing.T) {
	// The plain smoke stub has neither optional interface, as a build without
	// the service would not. "Not implemented here" is the true answer.
	ts, _ := bootSmokeServer(t)
	for _, r := range newConsoleRoutes {
		code, _ := do(t, ts, r.method, r.path, map[string]any{"id": "pbs1", "url": "https://pbs.example"}, withToken(testToken))
		if code != http.StatusNotImplemented {
			t.Errorf("%s %s without a handler = %d, want 501", r.method, r.path, code)
		}
	}
}

// ---- /pbs/servers ------------------------------------------------------

func TestPBSServersRoundTrip(t *testing.T) {
	_, h, c, _ := bootConsoleServer(t)
	h.set(func() {
		h.servers = PBSServersResponse{
			Servers: []PBSServerInfo{
				{ID: "pbs1", Name: "Primary", BaseURL: "https://pbs1.example:8007", AuthID: "backup@pbs!ws01",
					Datastore: "store1", SecretSet: true},
				{ID: "dr", Name: "Offsite", BaseURL: "https://dr.example:8007", Datastore: "dr"},
			},
			DefaultID: "pbs1",
		}
	})
	got, err := c.ListPBSServers()
	if err != nil {
		t.Fatalf("ListPBSServers: %v", err)
	}
	if len(got.Servers) != 2 || got.DefaultID != "pbs1" || got.DatapathProxy {
		t.Fatalf("got %+v", got)
	}
	if !got.Servers[0].SecretSet || got.Servers[1].SecretSet {
		t.Errorf("secret_set did not survive the round trip: %+v", got.Servers)
	}
}

func TestPBSServersCarryNoSecretField(t *testing.T) {
	// The type is the sanitization: PBSServerInfo has nowhere to put a token.
	// Pinned on the wire, so a field added to the type later that serializes as
	// "secret" (or carries one under another name) fails here.
	ts, h, _, _ := bootConsoleServer(t)
	h.set(func() {
		h.servers = PBSServersResponse{Servers: []PBSServerInfo{{ID: "pbs1", AuthID: "backup@pbs!ws01", SecretSet: true}}}
	})
	code, body := do(t, ts, http.MethodGet, "/pbs/servers", nil, withToken(testToken))
	if code != http.StatusOK {
		t.Fatalf("GET /pbs/servers = %d %s", code, body)
	}
	if strings.Contains(body, `"secret"`) {
		t.Errorf("the server list carries a secret field: %s", body)
	}
	if !strings.Contains(body, `"secret_set":true`) {
		t.Errorf("the server list does not say a secret is stored: %s", body)
	}
}

func TestPBSServersIsEmptyAndSaysWhyInProxyMode(t *testing.T) {
	_, h, c, _ := bootConsoleServer(t)
	// A config that still lists a server from before the machine moved to the
	// data path must not be shown: the machine has no PBS settings now.
	h.set(func() {
		h.servers = PBSServersResponse{
			Servers:       []PBSServerInfo{{ID: "stale", BaseURL: "https://old.example"}},
			DatapathProxy: true,
		}
	})
	got, err := c.ListPBSServers()
	if err != nil {
		t.Fatal(err)
	}
	if !got.DatapathProxy || len(got.Servers) != 0 {
		t.Errorf("proxy mode answered %+v; want an empty list that says datapath_proxy", got)
	}
}

func TestPBSServersNeverNull(t *testing.T) {
	ts, _, _, _ := bootConsoleServer(t)
	_, body := do(t, ts, http.MethodGet, "/pbs/servers", nil, withToken(testToken))
	if !strings.Contains(body, `"servers":[]`) {
		t.Errorf("an empty configuration serialized as %s; want an empty list", body)
	}
}

// ---- /pbs/test ---------------------------------------------------------

func TestPBSTestOutcomes(t *testing.T) {
	ts, h, c, _ := bootConsoleServer(t)
	h.set(func() {
		h.testResult["good"] = PBSTestResponse{OK: true}
	})
	h.set(func() {
		h.testResult["selfsigned"] = PBSTestResponse{
			Error: "connection failed: tls: failed to verify certificate: x509: certificate signed by unknown authority", CertUntrusted: true,
		}
	})

	if out, err := c.TestPBSServer("good"); err != nil || !out.OK {
		t.Errorf("a passing test came back %+v, %v", out, err)
	}

	// A test that ran and failed is an ANSWER: 200 on the wire, an error with
	// the PBS message for the caller, and the structured cert flag.
	code, body := do(t, ts, http.MethodPost, "/pbs/test", map[string]any{"id": "selfsigned"}, withToken(testToken))
	if code != http.StatusOK {
		t.Errorf("a failed test = HTTP %d; it ran, so it is an answer, not an API error (%s)", code, body)
	}
	out, err := c.TestPBSServer("selfsigned")
	if err == nil || !strings.Contains(err.Error(), "unknown authority") {
		t.Errorf("the PBS failure did not reach the caller: %v", err)
	}
	if out == nil || !out.CertUntrusted {
		t.Errorf("cert_untrusted lost on the way: %+v", out)
	}

	// Unknown id: 404, and the client can tell.
	_, err = c.TestPBSServer("nope")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusNotFound {
		t.Errorf("an unknown server = %v, want a 404 StatusError", err)
	}

	// The handler's own refusal status is kept (proxy mode is 409).
	h.set(func() {
		h.testErr = Refuse(http.StatusConflict, errors.New("this machine's backups go through its control server's data path"))
	})
	_, err = c.TestPBSServer("good")
	if !errors.As(err, &se) || se.Code != http.StatusConflict || !strings.Contains(err.Error(), "data path") {
		t.Errorf("proxy-mode refusal = %v, want 409 with the service's sentence", err)
	}
}

func TestPBSTestValidatesBeforeTheHandler(t *testing.T) {
	ts, h, _, _ := bootConsoleServer(t)
	if code, _ := do(t, ts, http.MethodPost, "/pbs/test", map[string]any{}, withToken(testToken)); code != http.StatusBadRequest {
		t.Errorf("POST /pbs/test without id = %d, want 400", code)
	}
	if code, _ := do(t, ts, http.MethodPost, "/pbs/test", "{not json", withToken(testToken)); code != http.StatusBadRequest {
		t.Errorf("malformed body = %d, want 400", code)
	}
	if h.called("TestPBSServerForAPI:") {
		t.Error("an invalid request reached the handler")
	}
}

// ---- /pbs/discover -----------------------------------------------------

func TestPBSDiscoverRoundTrip(t *testing.T) {
	_, h, c, _ := bootConsoleServer(t)
	h.set(func() {
		h.discovered = "AA:BB:CC"
	})
	fp, err := c.DiscoverPBSFingerprint("https://pbs.example:8007")
	if err != nil || fp != "AA:BB:CC" {
		t.Errorf("discover = %q, %v", fp, err)
	}
	if !h.called("DiscoverPBSFingerprintForAPI:https://pbs.example:8007") {
		t.Error("the handler was not given the URL")
	}
}

func TestPBSDiscoverFailures(t *testing.T) {
	ts, h, c, _ := bootConsoleServer(t)
	if code, _ := do(t, ts, http.MethodPost, "/pbs/discover", map[string]any{}, withToken(testToken)); code != http.StatusBadRequest {
		t.Errorf("discover without url = %d, want 400", code)
	}

	// The handler's 400 for a malformed URL is kept.
	h.set(func() {
		h.discErr = Refuse(http.StatusBadRequest, errors.New("only HTTPS URLs allowed (got ftp)"))
	})
	_, err := c.DiscoverPBSFingerprint("ftp://pbs.example")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusBadRequest {
		t.Errorf("malformed URL = %v, want 400", err)
	}

	// Unreachable defaults to 502: the far end failed, not this API.
	h.set(func() {
		h.discErr = errors.New("failed to reach server: dial tcp: connection refused")
	})
	_, err = c.DiscoverPBSFingerprint("https://pbs.example")
	if !errors.As(err, &se) || se.Code != http.StatusBadGateway {
		t.Errorf("unreachable = %v, want 502", err)
	}

	// A handler that "succeeds" with nothing must not let the console offer
	// to pin an empty fingerprint.
	h.set(func() {
		h.discErr, h.discovered = nil, ""
	})
	if _, err = c.DiscoverPBSFingerprint("https://pbs.example"); err == nil {
		t.Error("an empty fingerprint was returned as a success")
	}
}

// ---- /disks and /exchange ----------------------------------------------

func TestDisksRoundTrip(t *testing.T) {
	_, h, c, _ := bootConsoleServer(t)
	h.set(func() {
		h.disks = []PhysicalDisk{{Identity: "serial:ABC", Target: `\\.\PhysicalDrive0`, DiskNumber: 0,
			SizeBytes: 512 << 30, SizeText: "512 GB", Letters: []string{"C:"}, Path: `\\.\PhysicalDrive0`}}
	})
	got, err := c.GetDisks()
	if err != nil || len(got) != 1 || got[0].Identity != "serial:ABC" || got[0].Letters[0] != "C:" {
		t.Errorf("GetDisks = %+v, %v", got, err)
	}

	h.set(func() {
		h.disks = nil
	})
	got, err = c.GetDisks()
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("no disks = %#v, %v; want an empty list", got, err)
	}

	h.set(func() {
		h.disksErr = errors.New("Failed to enumerate volumes: access denied")
	})
	if _, err = c.GetDisks(); err == nil || !strings.Contains(err.Error(), "enumerate volumes") {
		t.Errorf("an enumeration failure = %v, want the service's message", err)
	}
}

func TestExchangeRunsTheLogQueryOnlyWhenAsked(t *testing.T) {
	_, h, c, _ := bootConsoleServer(t)
	h.set(func() {
		h.exchange = ExchangeStatus{Installed: true, Version: "2019", HighlightSetting: true}
	})
	h.set(func() {
		h.logMode = ExchangeLogMode{Queried: true, LogsAccumulate: true, Detail: "circular logging off"}
	})

	st, err := c.GetExchange(false)
	if err != nil || !st.Status.Installed || st.Status.Version != "2019" {
		t.Fatalf("GetExchange(false) = %+v, %v", st, err)
	}
	if st.LogMode != nil || h.logQueries() != 0 {
		t.Errorf("the slow log-mode query ran without being asked for (%d runs)", h.logQueries())
	}

	st, err = c.GetExchange(true)
	if err != nil || st.LogMode == nil || !st.LogMode.LogsAccumulate {
		t.Errorf("GetExchange(true) = %+v, %v", st, err)
	}
	if n := h.logQueries(); n != 1 {
		t.Errorf("log-mode queries = %d, want 1", n)
	}
}

// ---- /status -----------------------------------------------------------

func TestStatusCarriesSecurityWarnings(t *testing.T) {
	_, h, c, _ := bootConsoleServer(t)
	h.set(func() {
		h.warnings = []string{"Windows Update last succeeded 90 days ago"}
	})
	st, err := c.GetStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.SecurityWarnings) != 1 || !strings.Contains(st.SecurityWarnings[0], "Windows Update") {
		t.Errorf("security_warnings = %#v", st.SecurityWarnings)
	}
}

func TestStatusSecurityWarningsAreNeverNull(t *testing.T) {
	// No warnings, and a handler that does not report posture at all: both are
	// an empty list on the wire, which a consumer can iterate without a guard.
	for name, boot := range map[string]func(*testing.T) *httptest.Server{
		"none reported": func(t *testing.T) *httptest.Server { ts, _, _, _ := bootConsoleServer(t); return ts },
		"no reporter":   func(t *testing.T) *httptest.Server { ts, _ := bootSmokeServer(t); return ts },
	} {
		ts := boot(t)
		_, body := do(t, ts, http.MethodGet, "/status", nil, withToken(testToken))
		if !strings.Contains(body, `"security_warnings":[]`) {
			t.Errorf("%s: /status = %s; want security_warnings as an empty list", name, body)
		}
	}
}

func TestDeprecatedBackupStatusRouteIsGone(t *testing.T) {
	// V4-PIPELINE §5 kept /backup/status/{id} "for one release"; nothing calls
	// it (V4-CONSOLE §4.1). Gone from the mux AND from the locked allowlist, so
	// it cannot come back as a reader nobody declared.
	ts, _ := bootSmokeServer(t)
	if code, _ := do(t, ts, http.MethodGet, "/backup/status/run-1-1", nil, withToken(testToken)); code != http.StatusNotFound {
		t.Errorf("GET /backup/status/{id} = %d, want 404", code)
	}
	if readOnlyAllowed(http.MethodGet, "/backup/status/run-1-1") {
		t.Error("the deleted route is still on the read-only allowlist")
	}
}
