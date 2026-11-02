package api

// S3 — local API smoke (ARCHITECTURE.md Part III, Phase 1).
//
// Boots the REAL server (real mux, real authMiddleware) on a real loopback
// listener and drives it over real HTTP. Calling the handlers directly would
// bypass the auth middleware and the body cap — i.e. exactly the layer a GUI
// or a compromised local process actually hits, so it is the layer under test.
// The connections carry testCaller (testserver_test.go); the pipe that gives
// them a real Windows identity is tested on Windows (pipe_windows_test.go).
//
// Covers: the identity gate, the body cap, every route in setupRoutes()
// including method gates, the service->GUI progress relay round-trip (dev
// rule 9), and handler error propagation.

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

// stubHandler implements BackupHandler plus the optional interface the
// server type-asserts for progress (SetProgressCallbacks). It records calls so
// the test can assert the server actually delegated, and can be armed to fail
// so error propagation is exercised.
type stubHandler struct {
	mu     sync.Mutex
	calls  []string
	failOn map[string]bool

	// backup coordination
	started    chan string
	release    chan struct{}
	onProgress func(string, float64, string)
	onStats    func(string, uint64, uint64, uint64, uint64)
	onComplete func(string, bool, string)
	cbReady    chan struct{}
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

func (h *stubHandler) StartBackup(backupType string, backupDirs, diskTargets, excludeList []string, backupID string, useVSS bool, compression string) error {
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

func (h *stubHandler) ListScheduledJobs() ([]ScheduledJob, error) {
	h.record("ListScheduledJobs")
	if err := h.fail("ListScheduledJobs"); err != nil {
		return nil, err
	}
	return []ScheduledJob{{
		ID: "job-1", Name: "Nightly", BackupType: "directory", ScheduleTime: "02:00",
		BackupDirs: []string{`C:\Users`}, ExcludeList: []string{"*.tmp"}, BackupID: "WS-01",
		UseVSS: true, Compression: "fastest", LastRun: "2026-07-01T02:00:00Z", Enabled: true,
	}}, nil
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
// Handler() is what Serve serves; the connection's identity is testCaller.
func bootSmokeServer(t *testing.T) (*httptest.Server, *stubHandler) {
	t.Helper()
	h := newStub()
	ts := serveTest(NewServer(h))
	t.Cleanup(ts.Close)
	return ts, h
}

type reqOpt func(*http.Request)

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
	// A connection that carries no identity gets nothing: what Serve gives
	// every connection is the pipe's Windows identity, and a server reached
	// any other way (here, plain loopback TCP) has none to give.
	h := newStub()
	bare := serveTestWithoutCaller(NewServer(h))
	t.Cleanup(bare.Close)
	code, body := do(t, bare, http.MethodGet, "/status", nil)
	if code != http.StatusUnauthorized {
		t.Errorf("GET /status with no caller = %d, want 401 (body %s)", code, body)
	}
	if strings.Contains(body, "smoke-host") {
		t.Errorf("unauthorized response leaked config: %s", body)
	}
	if h.called("GetConfigWithHostname") {
		t.Error("a request with no caller reached the handler")
	}

	// The same server with an identity on its connections serves it, so the
	// refusal above is the missing identity and nothing else.
	ts, h2 := bootSmokeServer(t)
	if code, _ := do(t, ts, http.MethodGet, "/status", nil); code != http.StatusOK {
		t.Errorf("GET /status with a caller = %d, want 200", code)
	}
	if !h2.called("GetConfigWithHostname") {
		t.Error("an identified request never reached the handler")
	}

	// The token and the browser defenses are gone, not merely unused: a
	// header that used to matter changes nothing either way.
	if code, _ := do(t, ts, http.MethodGet, "/status", nil, withHeader("Origin", "https://example.invalid")); code != http.StatusOK {
		t.Errorf("an Origin header on an identified request = %d, want 200 (the pipe has no browser to defend against)", code)
	}
	if code, _ := do(t, bare, http.MethodGet, "/status", nil, withHeader("X-Nimbus-Token", "anything")); code != http.StatusUnauthorized {
		t.Errorf("the retired token header on a request with no caller = %d, want 401", code)
	}
}

func TestSmokeAuthGateCoversEveryRoute(t *testing.T) {
	bare := serveTestWithoutCaller(NewServer(newStub()))
	t.Cleanup(bare.Close)

	// Every route in setupRoutes(). A caller with no identity must never get
	// past the middleware, whatever the method or path shape.
	routes := []struct{ method, path string }{
		{http.MethodGet, "/status"},
		{http.MethodGet, "/caller"},
		{http.MethodPost, "/backup"},
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
	routes = append(routes, newConsoleRoutes...)
	for _, r := range routes {
		code, body := do(t, bare, r.method, r.path, map[string]interface{}{})
		if code != http.StatusUnauthorized {
			t.Errorf("unidentified %s %s = %d, want 401 (body %s)", r.method, r.path, code, body)
		}
	}
}

func TestSmokeBodyCap(t *testing.T) {
	ts, _ := bootSmokeServer(t)

	// maxRequestBody is 1 MiB; a larger body must be refused rather than
	// buffered. The decoder surfaces the truncation as a 400.
	big := `{"blob":"` + strings.Repeat("A", 2<<20) + `"}`
	code, _ := do(t, ts, http.MethodPost, "/config/save", big)
	if code != http.StatusBadRequest {
		t.Errorf("oversized body = %d, want 400", code)
	}

	// A body just under the cap still works.
	small := map[string]interface{}{"blob": strings.Repeat("A", 1024)}
	code, _ = do(t, ts, http.MethodPost, "/config/save", small)
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
		code, _ := do(t, ts, c.method, c.path, map[string]interface{}{})
		if code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", c.method, c.path, code)
		}
	}
}

func TestSmokeStatusAndJobsCRUD(t *testing.T) {
	ts, h := bootSmokeServer(t)

	// /status returns the sanitized config the GUI and control server read.
	code, body := do(t, ts, http.MethodGet, "/status", nil)
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

	// /jobs returns the jobs whole: the console's list and edit form read
	// every field, so a summary would empty the form.
	code, body = do(t, ts, http.MethodGet, "/jobs", nil)
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
	j := jobs.Jobs[0]
	if j.LastRun != "2026-07-01T02:00:00Z" || j.ScheduleTime != "02:00" || !j.UseVSS || j.BackupID != "WS-01" ||
		len(j.BackupDirs) != 1 || len(j.ExcludeList) != 1 || j.Compression != "fastest" || !j.Enabled {
		t.Errorf("a field did not come through: %+v", j)
	}
	for _, key := range []string{`"scheduleTime"`, `"runAtStartup"`, `"backupDirs"`, `"diskTargets"`, `"excludeList"`, `"useVSS"`} {
		if !strings.Contains(body, key) {
			t.Errorf("GET /jobs lacks %s, a key the console reads: %s", key, body)
		}
	}

	// create / update / delete all delegate to the handler.
	if code, _ = do(t, ts, http.MethodPost, "/jobs/create", map[string]interface{}{"name": "New"}); code != http.StatusOK {
		t.Errorf("POST /jobs/create = %d", code)
	}
	if code, _ = do(t, ts, http.MethodPost, "/jobs/update", map[string]interface{}{"id": "job-1"}); code != http.StatusOK {
		t.Errorf("POST /jobs/update = %d", code)
	}
	if code, _ = do(t, ts, http.MethodPut, "/jobs/update", map[string]interface{}{"id": "job-1"}); code != http.StatusOK {
		t.Errorf("PUT /jobs/update = %d", code)
	}
	if code, _ = do(t, ts, http.MethodDelete, "/jobs/delete/job-1", nil); code != http.StatusOK {
		t.Errorf("DELETE /jobs/delete/job-1 = %d", code)
	}
	for _, want := range []string{"SaveScheduledJobFromMap", "UpdateScheduledJobFromMap", "DeleteScheduledJobFromMap:job-1"} {
		if !h.called(want) {
			t.Errorf("handler never received %s", want)
		}
	}

	// A missing job id is a 400, not a delete-everything.
	if code, _ = do(t, ts, http.MethodDelete, "/jobs/delete/", nil); code != http.StatusBadRequest {
		t.Errorf("DELETE with empty job id = %d, want 400", code)
	}
}

func TestSmokePBSAndConfigRoutes(t *testing.T) {
	ts, h := bootSmokeServer(t)

	if code, _ := do(t, ts, http.MethodPost, "/pbs/save", map[string]interface{}{"id": "pbs-1", "host": "pbs.example"}); code != http.StatusOK {
		t.Errorf("POST /pbs/save = %d", code)
	}
	if code, _ := do(t, ts, http.MethodPost, "/pbs/default", map[string]interface{}{"id": "pbs-1"}); code != http.StatusOK {
		t.Errorf("POST /pbs/default = %d", code)
	}
	if code, _ := do(t, ts, http.MethodPost, "/pbs/fingerprint", map[string]interface{}{"id": "pbs-1", "fingerprint": "AA:BB"}); code != http.StatusOK {
		t.Errorf("POST /pbs/fingerprint = %d", code)
	}
	if code, _ := do(t, ts, http.MethodDelete, "/pbs/delete/pbs-1", nil); code != http.StatusOK {
		t.Errorf("DELETE /pbs/delete/pbs-1 = %d", code)
	}
	if code, _ := do(t, ts, http.MethodPost, "/config/save", map[string]interface{}{"usevss": true}); code != http.StatusOK {
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
	if code, _ := do(t, ts, http.MethodPost, "/pbs/default", map[string]interface{}{}); code != http.StatusBadRequest {
		t.Errorf("POST /pbs/default without id = %d, want 400", code)
	}
	if code, _ := do(t, ts, http.MethodPost, "/pbs/fingerprint", map[string]interface{}{"id": "pbs-1"}); code != http.StatusBadRequest {
		t.Errorf("POST /pbs/fingerprint without fingerprint = %d, want 400", code)
	}
	if code, _ := do(t, ts, http.MethodDelete, "/pbs/delete/", nil); code != http.StatusBadRequest {
		t.Errorf("DELETE /pbs/delete/ without id = %d, want 400", code)
	}

	// Malformed JSON is a 400, never a panic.
	if code, _ := do(t, ts, http.MethodPost, "/pbs/save", "{not json"); code != http.StatusBadRequest {
		t.Errorf("malformed JSON = %d, want 400", code)
	}
}

func TestSmokeControlPlaneRoutes(t *testing.T) {
	ts, h := bootSmokeServer(t)

	code, body := do(t, ts, http.MethodGet, "/controlplane/status", nil)
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

	if code, _ = do(t, ts, http.MethodPost, "/controlplane/save", map[string]interface{}{"control_server_url": "https://control.example"}); code != http.StatusOK {
		t.Errorf("POST /controlplane/save = %d", code)
	}
	if !h.called("SaveControlPlaneFromMap") {
		t.Error("handler never received SaveControlPlaneFromMap")
	}

	// A rejected settings write surfaces as 400 (the service validates).
	h.arm("SaveControlPlaneFromMap")
	if code, _ = do(t, ts, http.MethodPost, "/controlplane/save", map[string]interface{}{"control_server_url": "nonsense"}); code != http.StatusBadRequest {
		t.Errorf("rejected controlplane save = %d, want 400", code)
	}
}

// --- the progress relay ---------------------------------------------------

func TestSmokeBackupProgressRoundTrip(t *testing.T) {
	ts, h := bootSmokeServer(t)

	if code, _ := do(t, ts, http.MethodPost, "/backup", map[string]interface{}{"backup_type": "directory"}); code != http.StatusBadRequest {
		t.Errorf("POST /backup without backup_id = %d, want 400", code)
	}

	code, body := do(t, ts, http.MethodPost, "/backup", map[string]interface{}{
		"backup_type": "directory",
		"backup_id":   "smoke-backup",
		"backup_dirs": []string{"C:\\data"},
		"use_vss":     true,
	})
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

	// An unknown run id must 404 rather than reporting a phantom run.
	if code, _ = do(t, ts, http.MethodGet, "/runs/no-such-run", nil); code != http.StatusNotFound {
		t.Errorf("unknown run = %d, want 404", code)
	}

	// Drive the engine callbacks and confirm the polled JSON reflects them:
	// this is the service -> GUI relay the whole progress UI depends on.
	h.mu.Lock()
	onProgress, onStats, onComplete := h.onProgress, h.onStats, h.onComplete
	h.mu.Unlock()

	onProgress(started.JobID, 42.5, "halfway")
	onStats(started.JobID, 1024, 4096, 7, 3)

	prog := pollRun(t, ts, started.JobID)
	if !prog.Running() || prog.State == RunDone {
		t.Errorf("run should still be running: %+v", prog)
	}
	if prog.Percent != 42.5 || prog.Message != "halfway" {
		t.Errorf("progress not relayed: %+v", prog)
	}
	if prog.BytesDone != 1024 || prog.BytesTotal != 4096 || prog.NewChunks != 7 || prog.ReusedChunks != 3 {
		t.Errorf("stats not relayed: %+v", prog)
	}
	if prog.StartedAt.IsZero() {
		t.Error("started_at missing")
	}

	onComplete(started.JobID, true, "Backup completed successfully")
	close(h.release)

	prog = pollRun(t, ts, started.JobID)
	if prog.State != RunDone || !prog.Success || prog.Running() {
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
	})
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
		prog := pollRun(t, ts, started.JobID)
		if prog.State == RunDone {
			if prog.Success {
				t.Errorf("failed backup reported success: %+v", prog)
			}
			if prog.Error == "" {
				t.Errorf("failed backup carried no error text: %+v", prog)
			}
			if prog.Running() {
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
		code, body := do(t, ts, c.method, c.path, c.body)
		if code != http.StatusInternalServerError {
			t.Errorf("%s failure -> %s %s = %d, want 500 (body %s)", c.armed, c.method, c.path, code, body)
		}
	}
}

// pollRun reads one run the way the console does: /runs/{id}, which replaced
// the deleted /backup/status/{id}.
func pollRun(t *testing.T, ts *httptest.Server, runID string) Run {
	t.Helper()
	code, body := do(t, ts, http.MethodGet, "/runs/"+runID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /runs/%s = %d (%s)", runID, code, body)
	}
	var run Run
	if err := json.Unmarshal([]byte(body), &run); err != nil {
		t.Fatalf("decode run: %v (%s)", err, body)
	}
	return run
}
