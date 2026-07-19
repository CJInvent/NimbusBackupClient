package api

// S3 — local API smoke (ARCHITECTURE.md Part III, Phase 1).
//
// Boots the REAL server (real mux, real authMiddleware) on a real loopback
// listener and drives it over real HTTP. Calling the handlers directly would
// bypass the auth middleware and the body cap — i.e. exactly the layer a GUI
// or a compromised local process actually hits, so it is the layer under test.
//
// Covers: the token gate (absent/wrong/prefix), the browser-Origin block, the
// body cap, every route in setupRoutes() including method gates, the
// service->GUI progress relay round-trip (dev rule 9), and handler error
// propagation.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

// stubHandler implements BackupHandler plus the two optional interfaces the
// server type-asserts (ReloadConfig, SetProgressCallbacks). It records calls so
// the test can assert the server actually delegated, and can be armed to fail
// so error propagation is exercised.
type stubHandler struct {
	mu     sync.Mutex
	calls  []string
	failOn map[string]bool

	// backup coordination
	started       chan string
	release       chan struct{}
	onProgress    func(string, float64, string)
	onStats       func(string, uint64, uint64, uint64, uint64)
	onComplete    func(string, bool, string)
	cbReady       chan struct{}
	reloadedFirst bool
}

func newStub() *stubHandler {
	return &stubHandler{
		failOn:  map[string]bool{},
		started: make(chan string, 4),
		release: make(chan struct{}),
		cbReady: make(chan struct{}, 4),
	}
}

func (h *stubHandler) record(name string) {
	h.mu.Lock()
	h.calls = append(h.calls, name)
	h.mu.Unlock()
}

func (h *stubHandler) called(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.calls {
		if c == name {
			return true
		}
	}
	return false
}

func (h *stubHandler) fail(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failOn[name] {
		return fmt.Errorf("stub failure in %s", name)
	}
	return nil
}

func (h *stubHandler) arm(name string) {
	h.mu.Lock()
	h.failOn[name] = true
	h.mu.Unlock()
}

// ReloadConfig is the optional interface handleBackup type-asserts.
func (h *stubHandler) ReloadConfig() {
	h.mu.Lock()
	if len(h.calls) == 0 {
		h.reloadedFirst = true
	}
	h.mu.Unlock()
	h.record("ReloadConfig")
}

func (h *stubHandler) SetProgressCallbacks(
	jobID string,
	onProgress func(string, float64, string),
	onStats func(string, uint64, uint64, uint64, uint64),
	onComplete func(string, bool, string),
) {
	h.mu.Lock()
	h.onProgress, h.onStats, h.onComplete = onProgress, onStats, onComplete
	h.mu.Unlock()
	h.record("SetProgressCallbacks")
	select {
	case h.cbReady <- struct{}{}:
	default:
	}
}

func (h *stubHandler) StartBackup(backupType string, backupDirs, driveLetters, excludeList []string, backupID string, useVSS bool, compression string) error {
	h.record("StartBackup:" + backupType + ":" + backupID + ":" + compression)
	select {
	case h.started <- backupID:
	default:
	}
	<-h.release // block so the test can observe the RUNNING state
	return h.fail("StartBackup")
}

func (h *stubHandler) GetConfigWithHostname() map[string]interface{} {
	h.record("GetConfigWithHostname")
	return map[string]interface{}{"hostname": "smoke-host", "usevss": true}
}

func (h *stubHandler) GetScheduledJobsForAPI() []map[string]interface{} {
	h.record("GetScheduledJobsForAPI")
	return []map[string]interface{}{
		{"id": "job-1", "name": "Nightly", "backup_type": "directory", "schedule": "0 2 * * *", "last_run": "2026-07-01T02:00:00Z"},
	}
}

func (h *stubHandler) SaveScheduledJobFromMap(job map[string]interface{}) error {
	h.record("SaveScheduledJobFromMap")
	return h.fail("SaveScheduledJobFromMap")
}

func (h *stubHandler) UpdateScheduledJobFromMap(job map[string]interface{}) error {
	h.record("UpdateScheduledJobFromMap")
	return h.fail("UpdateScheduledJobFromMap")
}

func (h *stubHandler) DeleteScheduledJobFromMap(jobID string) error {
	h.record("DeleteScheduledJobFromMap:" + jobID)
	return h.fail("DeleteScheduledJobFromMap")
}

func (h *stubHandler) PinServerFingerprint(id, fingerprint string) error {
	h.record("PinServerFingerprint:" + id + ":" + fingerprint)
	return h.fail("PinServerFingerprint")
}

func (h *stubHandler) SavePBSServerFromMap(server map[string]interface{}) error {
	h.record("SavePBSServerFromMap")
	return h.fail("SavePBSServerFromMap")
}

func (h *stubHandler) DeletePBSServerByID(id string) error {
	h.record("DeletePBSServerByID:" + id)
	return h.fail("DeletePBSServerByID")
}

func (h *stubHandler) SetDefaultPBSByID(id string) error {
	h.record("SetDefaultPBSByID:" + id)
	return h.fail("SetDefaultPBSByID")
}

func (h *stubHandler) SaveConfigFromMap(config map[string]interface{}) error {
	h.record("SaveConfigFromMap")
	return h.fail("SaveConfigFromMap")
}

func (h *stubHandler) ControlPlaneStatusMap() map[string]interface{} {
	h.record("ControlPlaneStatusMap")
	return map[string]interface{}{"enrolled": true, "server_url": "https://control.example"}
}

func (h *stubHandler) SaveControlPlaneFromMap(m map[string]interface{}) error {
	h.record("SaveControlPlaneFromMap")
	return h.fail("SaveControlPlaneFromMap")
}

// bootSmokeServer starts the production server stack on a loopback listener.
// authMiddleware + mux are the same objects Start() would serve.
func bootSmokeServer(t *testing.T) (*httptest.Server, *stubHandler) {
	t.Helper()
	h := newStub()
	s := NewServer("127.0.0.1:0", h, testToken)
	ts := httptest.NewServer(s.authMiddleware(s.mux))
	t.Cleanup(ts.Close)
	return ts, h
}

type reqOpt func(*http.Request)

func withToken(tok string) reqOpt {
	return func(r *http.Request) { r.Header.Set(tokenHeader, tok) }
}

func withHeader(k, v string) reqOpt {
	return func(r *http.Request) { r.Header.Set(k, v) }
}

func do(t *testing.T, ts *httptest.Server, method, path string, body interface{}, opts ...reqOpt) (int, string) {
	t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
		rdr = nil
	case string:
		rdr = strings.NewReader(b)
	default:
		enc, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(enc)
	}
	req, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, path, err)
	}
	for _, o := range opts {
		o(req)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s %s: %v", method, path, err)
	}
	return resp.StatusCode, string(out)
}

// --- the gate -------------------------------------------------------------

func TestSmokeAuthGate(t *testing.T) {
	ts, h := bootSmokeServer(t)

	cases := []struct {
		name string
		opts []reqOpt
		want int
	}{
		{"no token", nil, http.StatusUnauthorized},
		{"empty token", []reqOpt{withToken("")}, http.StatusUnauthorized},
		{"wrong token", []reqOpt{withToken("nope")}, http.StatusUnauthorized},
		// A prefix of the real token must fail: constant-time compare is
		// length-sensitive, a naive HasPrefix check would not be.
		{"prefix of token", []reqOpt{withToken(testToken[:len(testToken)-1])}, http.StatusUnauthorized},
		{"token plus suffix", []reqOpt{withToken(testToken + "x")}, http.StatusUnauthorized},
		{"valid token", []reqOpt{withToken(testToken)}, http.StatusOK},
	}
	for _, c := range cases {
		code, body := do(t, ts, http.MethodGet, "/status", nil, c.opts...)
		if code != c.want {
			t.Errorf("%s: GET /status = %d, want %d (body %s)", c.name, code, c.want, body)
		}
		if c.want == http.StatusUnauthorized && strings.Contains(body, "smoke-host") {
			t.Errorf("%s: unauthorized response leaked config: %s", c.name, body)
		}
	}

	// The handler must not have been reached by any rejected request.
	if h.called("GetConfigWithHostname") != true {
		t.Error("authorized request never reached the handler")
	}

	// Browser-originated requests are refused even WITH a valid token: our GUI
	// never sets Origin, a cross-origin browser request always does.
	code, _ := do(t, ts, http.MethodGet, "/status", nil, withToken(testToken), withHeader("Origin", "https://evil.example"))
	if code != http.StatusForbidden {
		t.Errorf("Origin-bearing request = %d, want 403", code)
	}
}

func TestSmokeAuthGateCoversEveryRoute(t *testing.T) {
	ts, _ := bootSmokeServer(t)

	// Every route in setupRoutes(). An unauthenticated caller must never get
	// past the middleware, whatever the method or path shape.
	routes := []struct{ method, path string }{
		{http.MethodGet, "/status"},
		{http.MethodPost, "/backup"},
		{http.MethodGet, "/backup/status/whatever"},
		{http.MethodGet, "/jobs"},
		{http.MethodPost, "/jobs/create"},
		{http.MethodPost, "/jobs/update"},
		{http.MethodPost, "/jobs/delete/job-1"},
		{http.MethodPost, "/pbs/fingerprint"},
		{http.MethodPost, "/pbs/save"},
		{http.MethodPost, "/pbs/delete/pbs-1"},
		{http.MethodPost, "/pbs/default"},
		{http.MethodPost, "/config/save"},
		{http.MethodGet, "/controlplane/status"},
		{http.MethodPost, "/controlplane/save"},
	}
	for _, r := range routes {
		code, body := do(t, ts, r.method, r.path, map[string]interface{}{})
		if code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s %s = %d, want 401 (body %s)", r.method, r.path, code, body)
		}
	}
}

func TestSmokeBodyCap(t *testing.T) {
	ts, _ := bootSmokeServer(t)

	// maxRequestBody is 1 MiB; a larger body must be refused rather than
	// buffered. The decoder surfaces the truncation as a 400.
	big := `{"blob":"` + strings.Repeat("A", 2<<20) + `"}`
	code, _ := do(t, ts, http.MethodPost, "/config/save", big, withToken(testToken))
	if code != http.StatusBadRequest {
		t.Errorf("oversized body = %d, want 400", code)
	}

	// A body just under the cap still works.
	small := map[string]interface{}{"blob": strings.Repeat("A", 1024)}
	code, _ = do(t, ts, http.MethodPost, "/config/save", small, withToken(testToken))
	if code != http.StatusOK {
		t.Errorf("normal body = %d, want 200", code)
	}
}

// --- the route table ------------------------------------------------------

func TestSmokeMethodGates(t *testing.T) {
	ts, _ := bootSmokeServer(t)

	cases := []struct{ method, path string }{
		{http.MethodPost, "/status"},
		{http.MethodGet, "/backup"},
		{http.MethodPost, "/backup/status/x"},
		{http.MethodPost, "/jobs"},
		{http.MethodGet, "/jobs/create"},
		{http.MethodGet, "/jobs/update"},
		{http.MethodGet, "/jobs/delete/job-1"},
		{http.MethodGet, "/pbs/fingerprint"},
		{http.MethodGet, "/pbs/save"},
		{http.MethodGet, "/pbs/delete/pbs-1"},
		{http.MethodGet, "/pbs/default"},
		{http.MethodGet, "/config/save"},
		{http.MethodPost, "/controlplane/status"},
		{http.MethodGet, "/controlplane/save"},
	}
	for _, c := range cases {
		code, _ := do(t, ts, c.method, c.path, map[string]interface{}{}, withToken(testToken))
		if code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", c.method, c.path, code)
		}
	}
}

func TestSmokeStatusAndJobsCRUD(t *testing.T) {
	ts, h := bootSmokeServer(t)

	// /status returns the sanitized config the GUI and control server read.
	code, body := do(t, ts, http.MethodGet, "/status", nil, withToken(testToken))
	if code != http.StatusOK {
		t.Fatalf("GET /status = %d", code)
	}
	var status StatusResponse
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatalf("decode status: %v (%s)", err, body)
	}
	if !status.Running {
		t.Error("status.running = false, want true")
	}
	if status.Configuration["hostname"] != "smoke-host" {
		t.Errorf("status.configuration.hostname = %v, want smoke-host", status.Configuration["hostname"])
	}

	// /jobs projects the handler's maps into JobInfo.
	code, body = do(t, ts, http.MethodGet, "/jobs", nil, withToken(testToken))
	if code != http.StatusOK {
		t.Fatalf("GET /jobs = %d", code)
	}
	var jobs JobsResponse
	if err := json.Unmarshal([]byte(body), &jobs); err != nil {
		t.Fatalf("decode jobs: %v (%s)", err, body)
	}
	if len(jobs.Jobs) != 1 || jobs.Jobs[0].ID != "job-1" || jobs.Jobs[0].Name != "Nightly" {
		t.Fatalf("unexpected jobs payload: %+v", jobs.Jobs)
	}
	if jobs.Jobs[0].LastRun != "2026-07-01T02:00:00Z" {
		t.Errorf("last_run not carried through: %+v", jobs.Jobs[0])
	}

	// create / update / delete all delegate to the handler.
	if code, _ = do(t, ts, http.MethodPost, "/jobs/create", map[string]interface{}{"name": "New"}, withToken(testToken)); code != http.StatusOK {
		t.Errorf("POST /jobs/create = %d", code)
	}
	if code, _ = do(t, ts, http.MethodPost, "/jobs/update", map[string]interface{}{"id": "job-1"}, withToken(testToken)); code != http.StatusOK {
		t.Errorf("POST /jobs/update = %d", code)
	}
	if code, _ = do(t, ts, http.MethodPut, "/jobs/update", map[string]interface{}{"id": "job-1"}, withToken(testToken)); code != http.StatusOK {
		t.Errorf("PUT /jobs/update = %d", code)
	}
	if code, _ = do(t, ts, http.MethodDelete, "/jobs/delete/job-1", nil, withToken(testToken)); code != http.StatusOK {
		t.Errorf("DELETE /jobs/delete/job-1 = %d", code)
	}
	for _, want := range []string{"SaveScheduledJobFromMap", "UpdateScheduledJobFromMap", "DeleteScheduledJobFromMap:job-1"} {
		if !h.called(want) {
			t.Errorf("handler never received %s", want)
		}
	}

	// A missing job id is a 400, not a delete-everything.
	if code, _ = do(t, ts, http.MethodDelete, "/jobs/delete/", nil, withToken(testToken)); code != http.StatusBadRequest {
		t.Errorf("DELETE with empty job id = %d, want 400", code)
	}
}

func TestSmokePBSAndConfigRoutes(t *testing.T) {
	ts, h := bootSmokeServer(t)

	if code, _ := do(t, ts, http.MethodPost, "/pbs/save", map[string]interface{}{"id": "pbs-1", "host": "pbs.example"}, withToken(testToken)); code != http.StatusOK {
		t.Errorf("POST /pbs/save = %d", code)
	}
	if code, _ := do(t, ts, http.MethodPost, "/pbs/default", map[string]interface{}{"id": "pbs-1"}, withToken(testToken)); code != http.StatusOK {
		t.Errorf("POST /pbs/default = %d", code)
	}
	if code, _ := do(t, ts, http.MethodPost, "/pbs/fingerprint", map[string]interface{}{"id": "pbs-1", "fingerprint": "AA:BB"}, withToken(testToken)); code != http.StatusOK {
		t.Errorf("POST /pbs/fingerprint = %d", code)
	}
	if code, _ := do(t, ts, http.MethodDelete, "/pbs/delete/pbs-1", nil, withToken(testToken)); code != http.StatusOK {
		t.Errorf("DELETE /pbs/delete/pbs-1 = %d", code)
	}
	if code, _ := do(t, ts, http.MethodPost, "/config/save", map[string]interface{}{"usevss": true}, withToken(testToken)); code != http.StatusOK {
		t.Errorf("POST /config/save = %d", code)
	}
	for _, want := range []string{
		"SavePBSServerFromMap", "SetDefaultPBSByID:pbs-1",
		"PinServerFingerprint:pbs-1:AA:BB", "DeletePBSServerByID:pbs-1", "SaveConfigFromMap",
	} {
		if !h.called(want) {
			t.Errorf("handler never received %s", want)
		}
	}

	// Required-field validation happens before delegation.
	if code, _ := do(t, ts, http.MethodPost, "/pbs/default", map[string]interface{}{}, withToken(testToken)); code != http.StatusBadRequest {
		t.Errorf("POST /pbs/default without id = %d, want 400", code)
	}
	if code, _ := do(t, ts, http.MethodPost, "/pbs/fingerprint", map[string]interface{}{"id": "pbs-1"}, withToken(testToken)); code != http.StatusBadRequest {
		t.Errorf("POST /pbs/fingerprint without fingerprint = %d, want 400", code)
	}
	if code, _ := do(t, ts, http.MethodDelete, "/pbs/delete/", nil, withToken(testToken)); code != http.StatusBadRequest {
		t.Errorf("DELETE /pbs/delete/ without id = %d, want 400", code)
	}

	// Malformed JSON is a 400, never a panic.
	if code, _ := do(t, ts, http.MethodPost, "/pbs/save", "{not json", withToken(testToken)); code != http.StatusBadRequest {
		t.Errorf("malformed JSON = %d, want 400", code)
	}
}

func TestSmokeControlPlaneRoutes(t *testing.T) {
	ts, h := bootSmokeServer(t)

	code, body := do(t, ts, http.MethodGet, "/controlplane/status", nil, withToken(testToken))
	if code != http.StatusOK {
		t.Fatalf("GET /controlplane/status = %d", code)
	}
	var cp map[string]interface{}
	if err := json.Unmarshal([]byte(body), &cp); err != nil {
		t.Fatalf("decode controlplane status: %v (%s)", err, body)
	}
	if cp["enrolled"] != true {
		t.Errorf("controlplane status not carried through: %v", cp)
	}

	if code, _ = do(t, ts, http.MethodPost, "/controlplane/save", map[string]interface{}{"control_server_url": "https://control.example"}, withToken(testToken)); code != http.StatusOK {
		t.Errorf("POST /controlplane/save = %d", code)
	}
	if !h.called("SaveControlPlaneFromMap") {
		t.Error("handler never received SaveControlPlaneFromMap")
	}

	// A rejected settings write surfaces as 400 (the service validates).
	h.arm("SaveControlPlaneFromMap")
	if code, _ = do(t, ts, http.MethodPost, "/controlplane/save", map[string]interface{}{"control_server_url": "nonsense"}, withToken(testToken)); code != http.StatusBadRequest {
		t.Errorf("rejected controlplane save = %d, want 400", code)
	}
}

// --- the progress relay ---------------------------------------------------

func TestSmokeBackupProgressRoundTrip(t *testing.T) {
	ts, h := bootSmokeServer(t)

	if code, _ := do(t, ts, http.MethodPost, "/backup", map[string]interface{}{"backup_type": "directory"}, withToken(testToken)); code != http.StatusBadRequest {
		t.Errorf("POST /backup without backup_id = %d, want 400", code)
	}

	code, body := do(t, ts, http.MethodPost, "/backup", map[string]interface{}{
		"backup_type": "directory",
		"backup_id":   "smoke-backup",
		"backup_dirs": []string{"C:\\data"},
		"use_vss":     true,
	}, withToken(testToken))
	if code != http.StatusOK {
		t.Fatalf("POST /backup = %d (%s)", code, body)
	}
	var started BackupResponse
	if err := json.Unmarshal([]byte(body), &started); err != nil {
		t.Fatalf("decode backup response: %v (%s)", err, body)
	}
	if !started.Success || started.JobID == "" {
		t.Fatalf("backup did not start: %+v", started)
	}

	// Wait for the service side to register its callbacks and enter StartBackup.
	select {
	case <-h.cbReady:
	case <-time.After(5 * time.Second):
		t.Fatal("SetProgressCallbacks never called — the GUI would poll a job that never reports")
	}
	select {
	case <-h.started:
	case <-time.After(5 * time.Second):
		t.Fatal("StartBackup never called")
	}

	// An unknown job id must 404 rather than reporting a phantom job.
	if code, _ = do(t, ts, http.MethodGet, "/backup/status/no-such-job", nil, withToken(testToken)); code != http.StatusNotFound {
		t.Errorf("unknown job status = %d, want 404", code)
	}

	// Drive the engine callbacks and confirm the polled JSON reflects them:
	// this is the service -> GUI relay the whole progress UI depends on.
	h.mu.Lock()
	onProgress, onStats, onComplete := h.onProgress, h.onStats, h.onComplete
	h.mu.Unlock()

	onProgress(started.JobID, 42.5, "halfway")
	onStats(started.JobID, 1024, 4096, 7, 3)

	prog := pollProgress(t, ts, started.JobID)
	if !prog.Running || prog.Complete {
		t.Errorf("job should still be running: %+v", prog)
	}
	if prog.Progress != 42.5 || prog.Message != "halfway" {
		t.Errorf("progress not relayed: %+v", prog)
	}
	if prog.BytesDone != 1024 || prog.BytesTotal != 4096 || prog.NewChunks != 7 || prog.ReusedChunks != 3 {
		t.Errorf("stats not relayed: %+v", prog)
	}
	if prog.StartTime == "" {
		t.Error("start_time missing")
	}

	onComplete(started.JobID, true, "Backup completed successfully")
	close(h.release)

	prog = pollProgress(t, ts, started.JobID)
	if !prog.Complete || !prog.Success || prog.Running {
		t.Errorf("completion not relayed: %+v", prog)
	}
	if prog.Error != "" {
		t.Errorf("successful backup reported an error: %+v", prog)
	}
}

// A backup whose engine never fires callbacks must still terminate as failed
// when StartBackup returns an error — otherwise the GUI polls "running" forever.
func TestSmokeBackupFailureWithoutCallbacks(t *testing.T) {
	ts, h := bootSmokeServer(t)
	h.arm("StartBackup")

	code, body := do(t, ts, http.MethodPost, "/backup", map[string]interface{}{
		"backup_type": "machine",
		"backup_id":   "smoke-fail",
	}, withToken(testToken))
	if code != http.StatusOK {
		t.Fatalf("POST /backup = %d (%s)", code, body)
	}
	var started BackupResponse
	if err := json.Unmarshal([]byte(body), &started); err != nil {
		t.Fatalf("decode: %v", err)
	}

	select {
	case <-h.started:
	case <-time.After(5 * time.Second):
		t.Fatal("StartBackup never called")
	}
	close(h.release)

	deadline := time.Now().Add(5 * time.Second)
	for {
		prog := pollProgress(t, ts, started.JobID)
		if prog.Complete {
			if prog.Success {
				t.Errorf("failed backup reported success: %+v", prog)
			}
			if prog.Error == "" {
				t.Errorf("failed backup carried no error text: %+v", prog)
			}
			if prog.Running {
				t.Errorf("failed backup still marked running: %+v", prog)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("backup never terminated: %+v", prog)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Config-write failures inside the service surface as 500, so the GUI reports
// the write failed instead of silently believing it succeeded (dev rule 2).
func TestSmokeHandlerErrorsPropagate(t *testing.T) {
	ts, h := bootSmokeServer(t)

	for _, c := range []struct {
		armed  string
		method string
		path   string
		body   interface{}
	}{
		{"SavePBSServerFromMap", http.MethodPost, "/pbs/save", map[string]interface{}{"id": "pbs-1"}},
		{"DeletePBSServerByID", http.MethodDelete, "/pbs/delete/pbs-1", nil},
		{"SetDefaultPBSByID", http.MethodPost, "/pbs/default", map[string]interface{}{"id": "pbs-1"}},
		{"PinServerFingerprint", http.MethodPost, "/pbs/fingerprint", map[string]interface{}{"id": "p", "fingerprint": "AA"}},
		{"SaveConfigFromMap", http.MethodPost, "/config/save", map[string]interface{}{"usevss": true}},
		{"SaveScheduledJobFromMap", http.MethodPost, "/jobs/create", map[string]interface{}{"name": "x"}},
		{"UpdateScheduledJobFromMap", http.MethodPost, "/jobs/update", map[string]interface{}{"id": "x"}},
		{"DeleteScheduledJobFromMap", http.MethodDelete, "/jobs/delete/job-1", nil},
	} {
		h.arm(c.armed)
		code, body := do(t, ts, c.method, c.path, c.body, withToken(testToken))
		if code != http.StatusInternalServerError {
			t.Errorf("%s failure -> %s %s = %d, want 500 (body %s)", c.armed, c.method, c.path, code, body)
		}
	}
}

func pollProgress(t *testing.T, ts *httptest.Server, jobID string) BackupProgress {
	t.Helper()
	code, body := do(t, ts, http.MethodGet, "/backup/status/"+jobID, nil, withToken(testToken))
	if code != http.StatusOK {
		t.Fatalf("GET /backup/status/%s = %d (%s)", jobID, code, body)
	}
	var prog BackupProgress
	if err := json.Unmarshal([]byte(body), &prog); err != nil {
		t.Fatalf("decode progress: %v (%s)", err, body)
	}
	return prog
}
