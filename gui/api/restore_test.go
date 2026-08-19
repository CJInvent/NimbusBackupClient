package api

// THE RESTORE GATE, SWEPT.
//
// This file replaces gui/restore_policy_test.go, which swept the same property
// one layer up: every restore entry point must honour `file_restore`. It moved
// here because the gate did. While restore executed in the console, the check
// had to be in the console — and a check inside the process a policy exists to
// restrain is a suggestion, which is why three entry points were found in
// August 2026 with no check at all.
//
// The sweep survives the move, and it is still a SWEEP rather than a case
// list: it iterates the declared op table, so an op added without a permission
// is a failing test rather than a hole nobody looks for. The wire contract is
// unchanged (controlplane/types.go):
//
//	FileRestore=false: the GUI must hide/disable its restore browser and the
//	local API must refuse restore operations on this machine.
//
// The tests drive the REAL server stack over real HTTP — same mux, same auth
// middleware, same read-only middleware — because the gate is only worth
// anything at the layer a caller actually reaches.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// restoreStub is a BackupHandler that ALSO implements RestoreHandler, and
// records whether the engine was reached. "Was the engine reached" is the
// assertion that matters: a 403 proves the gate answered, and a recorded call
// proves it did not answer when it should not have.
type restoreStub struct {
	stubHandler

	mu       sync.Mutex
	queried  []string
	jobbed   []string
	controls []string

	jobResult any
	jobErr    error
	jobHold   chan struct{} // when non-nil, a job blocks until closed
}

func newRestoreStub() *restoreStub {
	return &restoreStub{stubHandler: *newStub()}
}

func (h *restoreStub) RestoreQuery(op string, params json.RawMessage) (any, error) {
	h.mu.Lock()
	h.queried = append(h.queried, op)
	h.mu.Unlock()
	// Echo the params back so a test can prove the body survived the trip.
	return map[string]any{"op": op, "params": string(params)}, nil
}

func (h *restoreStub) RestoreJob(_ context.Context, op string, _ json.RawMessage, progress RestoreProgress) (any, error) {
	h.mu.Lock()
	h.jobbed = append(h.jobbed, op)
	hold := h.jobHold
	h.mu.Unlock()

	progress(42, "working", 10, 100, 1e6, 7)
	if hold != nil {
		<-hold
	}
	return h.jobResult, h.jobErr
}

func (h *restoreStub) RestoreControl(op string, _ json.RawMessage) error {
	h.mu.Lock()
	h.controls = append(h.controls, op)
	h.mu.Unlock()
	return nil
}

func (h *restoreStub) reached() (queries, jobs, controls int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.queried), len(h.jobbed), len(h.controls)
}

// bootRestoreServer starts the production stack with an engine behind it and
// the given permission predicate.
func bootRestoreServer(t *testing.T, allow func(string) bool) (*httptest.Server, *restoreStub) {
	t.Helper()
	h := newRestoreStub()
	s := NewServer("127.0.0.1:0", h, testToken)
	if allow != nil {
		s.SetRestoreAllowedFunc(allow)
	}
	ts := httptest.NewServer(s.authMiddleware(s.readOnlyMiddleware(s.mux)))
	t.Cleanup(ts.Close)
	return ts, h
}

// routeFor maps an op kind to the route that carries it.
func routeFor(kind RestoreOpKind) string {
	switch kind {
	case OpQuery:
		return "/restore/query"
	case OpJob:
		return "/restore/job"
	default:
		return "/restore/control"
	}
}

// EVERY declared op is refused when `file_restore` is off, and refused with
// 403 specifically.
//
// Pinning the status AND the engine's silence is what makes this a real
// assertion rather than "something went wrong": a stub that returned an error
// for its own reasons would satisfy a looser check while the gate was gone.
func TestEveryRestoreOpIsRefusedWhenFileRestoreIsOff(t *testing.T) {
	ts, h := bootRestoreServer(t, func(string) bool { return false })

	for _, op := range RestoreOps() {
		t.Run(op.Name, func(t *testing.T) {
			code, body := do(t, ts, http.MethodPost, routeFor(op.Kind),
				map[string]any{"op": op.Name, "params": map[string]any{}}, withToken(testToken))
			if code != http.StatusForbidden {
				t.Fatalf("%s returned %d, want 403 — the policy gate did not fire\nbody: %s", op.Name, code, body)
			}
			if !strings.Contains(body, ErrRestoreForbiddenText) {
				t.Fatalf("%s was refused without saying why: %s", op.Name, body)
			}
		})
	}
	if q, j, c := h.reached(); q+j+c != 0 {
		t.Fatalf("the engine was reached %d/%d/%d times (query/job/control) while file_restore was OFF", q, j, c)
	}
}

// The converse, and it is not decoration: a gate that refused unconditionally
// would pass the test above completely.
func TestEveryRestoreOpReachesTheEngineWhenPermitted(t *testing.T) {
	ts, h := bootRestoreServer(t, func(string) bool { return true })

	for _, op := range RestoreOps() {
		t.Run(op.Name, func(t *testing.T) {
			code, body := do(t, ts, http.MethodPost, routeFor(op.Kind),
				map[string]any{"op": op.Name, "params": map[string]any{}}, withToken(testToken))
			if code != http.StatusOK {
				t.Fatalf("%s returned %d while file_restore was ON\nbody: %s", op.Name, code, body)
			}
		})
	}

	q, j, c := h.reached()
	var wantQ, wantJ, wantC int
	for _, op := range RestoreOps() {
		switch op.Kind {
		case OpQuery:
			wantQ++
		case OpJob:
			wantJ++
		case OpControl:
			wantC++
		}
	}
	// Jobs are asynchronous, so give the goroutines a moment before counting.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if q, j, c = h.reached(); j >= wantJ {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if q != wantQ || j != wantJ || c != wantC {
		t.Fatalf("engine reached %d/%d/%d, want %d/%d/%d (query/job/control)", q, j, c, wantQ, wantJ, wantC)
	}
}

// NO PREDICATE MEANS NO RESTORE. The service installs one at startup; an edit
// that drops that line must break restore loudly rather than leave it ungated,
// so "never configured" is refused exactly like "not permitted".
func TestRestoreIsRefusedWhenNoPolicyPredicateIsInstalled(t *testing.T) {
	ts, h := bootRestoreServer(t, nil)

	code, body := do(t, ts, http.MethodPost, "/restore/query",
		map[string]any{"op": "snapshots"}, withToken(testToken))
	if code != http.StatusForbidden {
		t.Fatalf("an unconfigured server permitted a restore: %d %s", code, body)
	}
	if q, _, _ := h.reached(); q != 0 {
		t.Fatal("the engine was reached with no policy predicate installed")
	}
}

// An op nobody declared is refused before dispatch. This is what makes the
// table the enumeration it claims to be: a handler that grew a case without a
// declaration is unreachable.
func TestAnUndeclaredOpIsRefused(t *testing.T) {
	ts, h := bootRestoreServer(t, func(string) bool { return true })

	code, body := do(t, ts, http.MethodPost, "/restore/query",
		map[string]any{"op": "delete-everything"}, withToken(testToken))
	if code != http.StatusBadRequest {
		t.Fatalf("an undeclared op returned %d, want 400: %s", code, body)
	}
	if q, _, _ := h.reached(); q != 0 {
		t.Fatal("an undeclared op reached the engine")
	}
}

// A declared op called down the wrong route is refused too. Without this, a
// long-running job could be invoked on the query route and hold an HTTP
// request open for an hour, and a control could be started as a job.
func TestAnOpCalledDownTheWrongRouteIsRefused(t *testing.T) {
	ts, _ := bootRestoreServer(t, func(string) bool { return true })

	code, _ := do(t, ts, http.MethodPost, "/restore/query",
		map[string]any{"op": "restore"}, withToken(testToken)) // a job, not a query
	if code != http.StatusBadRequest {
		t.Fatalf("a job invoked as a query returned %d, want 400", code)
	}
}

// RESTORE IS NOT READ-ONLY WORK. A locked console is a status page
// (gui/api/readonly.go); it must not be able to browse or extract a backup,
// and the allowlist is what says so. This pins that the restore routes were
// not added to it.
func TestRestoreRoutesAreRefusedUnderLockdown(t *testing.T) {
	h := newRestoreStub()
	s := NewServer("127.0.0.1:0", h, testToken)
	s.SetRestoreAllowedFunc(func(string) bool { return true })
	s.SetLockedFunc(func() bool { return true })
	ts := httptest.NewServer(s.authMiddleware(s.readOnlyMiddleware(s.mux)))
	t.Cleanup(ts.Close)

	for _, path := range []string{"/restore/query", "/restore/job", "/restore/control", "/restore/job/restore-1"} {
		method := http.MethodPost
		if strings.HasPrefix(path, "/restore/job/") {
			method = http.MethodGet
		}
		code, body := do(t, ts, method, path,
			map[string]any{"op": "snapshots"}, withToken(testToken))
		if code != http.StatusForbidden {
			t.Fatalf("%s returned %d under lockdown, want 403: %s", path, code, body)
		}
	}
	if q, j, c := h.reached(); q+j+c != 0 {
		t.Fatalf("a locked console reached the restore engine (%d/%d/%d)", q, j, c)
	}
}

// A job runs, reports progress, and hands back its result.
func TestARestoreJobReportsProgressAndItsResult(t *testing.T) {
	ts, h := bootRestoreServer(t, func(string) bool { return true })
	h.jobResult = map[string]any{"hits": 3}

	code, body := do(t, ts, http.MethodPost, "/restore/job",
		map[string]any{"op": "search"}, withToken(testToken))
	if code != http.StatusOK {
		t.Fatalf("starting a search returned %d: %s", code, body)
	}
	var start restoreJobStartResponse
	if err := json.Unmarshal([]byte(body), &start); err != nil {
		t.Fatalf("job id: %v (%s)", err, body)
	}

	st := pollRestoreJob(t, ts, start.JobID)
	if !st.Success {
		t.Fatalf("the job failed: %s", st.Error)
	}
	if !strings.Contains(string(st.Result), `"hits":3`) {
		t.Fatalf("the job's result did not come back: %s", st.Result)
	}
}

// A job that fails reports the engine's own words. The console shows them
// verbatim, so flattening them here would cost a user the one line that says
// whether this was a missing key, an unreachable datastore, or a full disk.
func TestAFailedRestoreJobCarriesTheEnginesOwnError(t *testing.T) {
	ts, h := bootRestoreServer(t, func(string) bool { return true })
	h.jobErr = fmt.Errorf("no source could supply the key this snapshot was encrypted with")

	code, body := do(t, ts, http.MethodPost, "/restore/job",
		map[string]any{"op": "restore"}, withToken(testToken))
	if code != http.StatusOK {
		t.Fatalf("starting a restore returned %d: %s", code, body)
	}
	var start restoreJobStartResponse
	_ = json.Unmarshal([]byte(body), &start)

	st := pollRestoreJob(t, ts, start.JobID)
	if st.Success {
		t.Fatal("a failing job reported success")
	}
	if !strings.Contains(st.Error, "no source could supply the key") {
		t.Fatalf("the engine's error did not survive: %q", st.Error)
	}
}

// REVOKING MID-RUN STOPS THE ANSWER. A job permitted when it started can be
// running when an org turns restore off, and collecting its result because the
// check happened once would make the revocation cosmetic.
func TestARunningJobStopsBeingCollectableWhenPolicyIsRevoked(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)

	h := newRestoreStub()
	h.jobHold = make(chan struct{})
	s := NewServer("127.0.0.1:0", h, testToken)
	s.SetRestoreAllowedFunc(func(string) bool { return allowed.Load() })
	ts := httptest.NewServer(s.authMiddleware(s.readOnlyMiddleware(s.mux)))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(h.jobHold) })

	code, body := do(t, ts, http.MethodPost, "/restore/job",
		map[string]any{"op": "search"}, withToken(testToken))
	if code != http.StatusOK {
		t.Fatalf("starting a search returned %d: %s", code, body)
	}
	var start restoreJobStartResponse
	_ = json.Unmarshal([]byte(body), &start)

	allowed.Store(false)
	code, _ = do(t, ts, http.MethodGet, "/restore/job/"+start.JobID, nil, withToken(testToken))
	if code != http.StatusForbidden {
		t.Fatalf("a revoked console could still collect a running job: %d", code)
	}
}

// A server with no engine behind it says so rather than pretending. The GUI
// process builds an api.Server of its own and must never look like one that
// can restore.
func TestAServerWithNoEngineSaysSo(t *testing.T) {
	s := NewServer("127.0.0.1:0", newStub(), testToken) // plain BackupHandler
	s.SetRestoreAllowedFunc(func(string) bool { return true })
	ts := httptest.NewServer(s.authMiddleware(s.readOnlyMiddleware(s.mux)))
	t.Cleanup(ts.Close)

	code, _ := do(t, ts, http.MethodPost, "/restore/query",
		map[string]any{"op": "snapshots"}, withToken(testToken))
	if code != http.StatusNotImplemented {
		t.Fatalf("a server with no restore engine returned %d, want 501", code)
	}
}

func pollRestoreJob(t *testing.T, ts *httptest.Server, id string) RestoreJobState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		code, body := do(t, ts, http.MethodGet, "/restore/job/"+id, nil, withToken(testToken))
		if code != http.StatusOK {
			t.Fatalf("polling %s returned %d: %s", id, code, body)
		}
		var st RestoreJobState
		if err := json.Unmarshal([]byte(body), &st); err != nil {
			t.Fatalf("job state: %v (%s)", err, body)
		}
		if st.Complete {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s never completed", id)
	return RestoreJobState{}
}
