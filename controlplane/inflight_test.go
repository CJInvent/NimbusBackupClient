package controlplane

// V4-BETA-FIXES §13.3 (F-75): a run's reports survive a restart and an
// outage, a run the process never saw finish is closed as interrupted, and a
// service stop closes its own runs. Driven against a recording fake of
// POST /api/agent/v1/runs; the server's side of the contract (forward-only,
// first terminal wins) is NimbusControl's RunIngest and its tests.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type runSink struct {
	mu      sync.Mutex
	status  int // response for every report; 0 = 200
	reports []RunReport
}

func (s *runSink) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agent/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		var rep RunReport
		_ = json.NewDecoder(r.Body).Decode(&rep)
		s.mu.Lock()
		code := s.status
		if code == 0 || code == http.StatusOK {
			s.reports = append(s.reports, rep)
		}
		s.mu.Unlock()
		if code != 0 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"refused"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (s *runSink) setStatus(code int) { s.mu.Lock(); s.status = code; s.mu.Unlock() }

func (s *runSink) got() []RunReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RunReport(nil), s.reports...)
}

func (s *runSink) terminals(uuid string) []RunReport {
	var out []RunReport
	for _, r := range s.got() {
		if r.RunUUID == uuid && r.Status != StatusPreparing && r.Status != StatusRunning {
			out = append(out, r)
		}
	}
	return out
}

// fastRetries: what happens after the retries give up, without waiting 40s.
func fastRetries(t *testing.T) {
	t.Helper()
	postDelaysMu.Lock()
	orig := postDelays
	postDelays = []time.Duration{0, time.Millisecond}
	postDelaysMu.Unlock()
	t.Cleanup(func() {
		postDelaysMu.Lock()
		postDelays = orig
		postDelaysMu.Unlock()
	})
}

// freshProcess forgets the process's store for path, as a restart does.
func freshProcess(path string) {
	inflightMu.Lock()
	delete(inflightStores, path)
	inflightMu.Unlock()
}

func storePath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "runs-inflight.json")
	t.Cleanup(func() { freshProcess(p) })
	return p
}

func newAgentClient(url, path string) *Client {
	c := &Client{BaseURL: url, AgentID: 7, Secret: "s3cret"}
	c.SetInflight(OpenInflightRuns(path))
	return c
}

// waitIdle is logging_test.go's.

func records(t *testing.T, path string) map[string]*inflightRecord {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]*inflightRecord{}
	}
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*inflightRecord{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("records file: %v", err)
	}
	return m
}

func TestRunIsRecordedUntilItsTerminalIsDelivered(t *testing.T) {
	sink := &runSink{}
	path := storePath(t)
	c := newAgentClient(sink.server(t).URL, path)

	r := c.NewRun("Nightly", "directory")
	r.Preparing()
	if rec, ok := records(t, path)[r.RunUUID()]; !ok || rec.Terminal != nil {
		t.Fatalf("the first report must record the run before it is sent; records=%v", records(t, path))
	}
	r.Running()
	r.Success("host", "PC-01", 1700000000, RunTotals{BytesTotal: 10}, "")
	waitIdle(t, r)
	if n := len(records(t, path)); n != 0 {
		t.Errorf("a delivered terminal must remove the record; %d left", n)
	}
}

func TestUndeliveredTerminalSurvivesARestartAndIsResentAsItWas(t *testing.T) {
	fastRetries(t)
	sink := &runSink{}
	srv := sink.server(t)
	path := storePath(t)
	c := newAgentClient(srv.URL, path)

	r := c.NewRun("Nightly", "directory")
	r.Preparing()
	waitIdle(t, r)
	sink.setStatus(http.StatusServiceUnavailable) // the outage starts
	r.Success("host", "PC-01", 1700000000, RunTotals{BytesTotal: 10}, "tail")
	waitIdle(t, r)
	if rec := records(t, path)[r.RunUUID()]; rec == nil || rec.Terminal == nil || rec.Terminal.Status != StatusSuccess {
		t.Fatalf("an undelivered terminal must stay stored; got %+v", rec)
	}

	// Restart; the server is back.
	freshProcess(path)
	sink.setStatus(0)
	c2 := newAgentClient(srv.URL, path)
	if n := c2.DeliverPendingRuns(); n != 1 {
		t.Fatalf("delivered %d, want 1", n)
	}
	got := sink.terminals(r.RunUUID())
	if len(got) != 1 || got[0].Status != StatusSuccess || got[0].PBSBackupID != "PC-01" || got[0].PBSBackupTime == nil {
		t.Fatalf("the success must arrive late exactly as it was; got %+v", got)
	}
	if len(records(t, path)) != 0 {
		t.Error("record kept after delivery")
	}
}

func TestRunNeverSeenToFinishIsClosedAsInterrupted(t *testing.T) {
	sink := &runSink{}
	srv := sink.server(t)
	path := storePath(t)
	c := newAgentClient(srv.URL, path)
	r := c.NewRun("Nightly", "machine")
	r.SetTrigger("schedule")
	r.Preparing()
	r.Running()
	waitIdle(t, r)

	freshProcess(path) // power loss: the process never finished the run
	c2 := newAgentClient(srv.URL, path)
	c2.DeliverPendingRuns()
	got := sink.terminals(r.RunUUID())
	if len(got) != 1 {
		t.Fatalf("want one terminal report, got %+v", got)
	}
	f := got[0]
	if f.Status != StatusFailed || f.ErrorSummary != InterruptedAtStartText {
		t.Errorf("got status %q summary %q", f.Status, f.ErrorSummary)
	}
	if f.JobName != "Nightly" || f.BackupType != "machine" || f.Trigger != "schedule" || f.StartedAt == "" || f.FinishedAt == "" {
		t.Errorf("the closing report must carry the run's identity: %+v", f)
	}
}

func TestRecoveryNeverClosesARunActiveInThisProcess(t *testing.T) {
	sink := &runSink{}
	c := newAgentClient(sink.server(t).URL, storePath(t))
	r := c.NewRun("Nightly", "directory")
	r.Preparing()
	waitIdle(t, r)
	c.DeliverPendingRuns() // what every check-in does mid-run
	if got := sink.terminals(r.RunUUID()); len(got) != 0 {
		t.Fatalf("a live run was closed by recovery: %+v", got)
	}
}

func TestServiceStopClosesActiveRunsAndTheEnginesReportIsNotSent(t *testing.T) {
	sink := &runSink{}
	path := storePath(t)
	c := newAgentClient(sink.server(t).URL, path)
	r := c.NewRun("Nightly", "directory")
	r.Preparing()
	waitIdle(t, r)

	if n := c.InterruptActiveRuns(InterruptedByStopText); n != 1 {
		t.Fatalf("closed %d runs, want 1", n)
	}
	// The engine then fails on the cut PBS session.
	r.Failed("write tcp: use of closed network connection", "")
	waitIdle(t, r)
	deadline := time.Now().Add(3 * time.Second)
	for len(records(t, path)) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := sink.terminals(r.RunUUID())
	if len(got) != 1 || got[0].ErrorSummary != InterruptedByStopText {
		t.Fatalf("want exactly the stop's terminal report; got %+v", got)
	}
	if len(records(t, path)) != 0 {
		t.Error("record kept after the stop's report was delivered")
	}
}

func TestRefusedTerminalIsDroppedServerErrorIsKept(t *testing.T) {
	fastRetries(t)
	for _, tc := range []struct {
		code int
		kept bool
	}{{http.StatusBadRequest, false}, {http.StatusConflict, false}, {http.StatusServiceUnavailable, true}, {http.StatusTooManyRequests, true}} {
		sink := &runSink{}
		path := storePath(t)
		c := newAgentClient(sink.server(t).URL, path)
		r := c.NewRun("J", "directory")
		sink.setStatus(tc.code)
		r.Failed("boom", "")
		waitIdle(t, r)
		if _, kept := records(t, path)[r.RunUUID()]; kept != tc.kept {
			t.Errorf("HTTP %d: record kept=%v, want %v", tc.code, kept, tc.kept)
		}
	}
}

func TestRecordsOfAnotherIdentityAreDroppedNotSent(t *testing.T) {
	sink := &runSink{}
	srv := sink.server(t)
	path := storePath(t)
	old := newAgentClient(srv.URL, path)
	r := old.NewRun("J", "directory")
	r.Preparing()
	waitIdle(t, r)

	freshProcess(path)
	reenrolled := &Client{BaseURL: srv.URL, AgentID: 8, Secret: "new"}
	reenrolled.SetInflight(OpenInflightRuns(path))
	reenrolled.DeliverPendingRuns()
	if got := sink.terminals(r.RunUUID()); len(got) != 0 {
		t.Fatalf("a previous identity's run was reported by the new one: %+v", got)
	}
	if len(records(t, path)) != 0 {
		t.Error("a previous identity's record was kept")
	}
}

func TestRecordsOlderThan30DaysAreDropped(t *testing.T) {
	sink := &runSink{}
	srv := sink.server(t)
	path := storePath(t)
	c := newAgentClient(srv.URL, path)
	r := c.NewRun("J", "directory")
	r.Preparing()
	waitIdle(t, r)

	freshProcess(path)
	c2 := newAgentClient(srv.URL, path)
	c2.inflight.now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	c2.DeliverPendingRuns()
	if got := sink.terminals(r.RunUUID()); len(got) != 0 {
		t.Fatalf("a 31-day-old record was delivered: %+v", got)
	}
	if len(records(t, path)) != 0 {
		t.Error("a 31-day-old record was kept")
	}
}

// Without a store (tools, tests) runs are reported from memory as before.
func TestNoStoreMeansMemoryOnly(t *testing.T) {
	sink := &runSink{}
	c := &Client{BaseURL: sink.server(t).URL, AgentID: 7, Secret: "s"}
	r := c.NewRun("J", "directory")
	r.Preparing()
	r.Success("host", "PC", 1, RunTotals{}, "")
	waitIdle(t, r)
	if len(sink.terminals(r.RunUUID())) != 1 || c.DeliverPendingRuns() != 0 || c.InterruptActiveRuns("x") != 0 {
		t.Error("memory-only reporting changed")
	}
}
