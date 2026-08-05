package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// RunRegistry tests.
//
// The bug this type exists to fix (docs/V4-PIPELINE.md §2) shipped because
// nothing ever asked the service "what is running" — so the assertions here are
// mostly about that question having a correct answer under conditions the old
// code never faced: a run nobody started over HTTP, a callback arriving after
// completion, a run older than retention that is still going, and concurrent
// writers from upload workers.
//
// Note on copies: Get/Active/Recent return Run BY VALUE, and that signature is
// the guarantee — a test asserting "the returned struct is a copy" cannot fail
// while the signature says so, which makes it a test of Go rather than of this
// code (dev rule 25). What actually catches a regression to *Run is
// TestConcurrentWritersAndReaders under -race, which reads the fields.
//
// Time is injected rather than slept. A test that sleeps to age a run is slow
// and, worse, flaky on a loaded CI runner — and this repository already has one
// scar from a fixture that only passed after 02:00.

// fakeClock is a settable clock for retention and duration assertions.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestRegistry() (*RunRegistry, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 8, 5, 2, 0, 0, 0, time.UTC)}
	r := NewRunRegistry()
	r.now = clk.now
	return r, clk
}

func TestRunLifecycle(t *testing.T) {
	r, clk := newTestRegistry()

	id := r.Begin(TriggerSchedule, "job-7", "Nightly-C", "WS-01", "machine")
	if id == "" {
		t.Fatal("Begin returned an empty run id")
	}

	run, ok := r.Get(id)
	if !ok {
		t.Fatal("run not found immediately after Begin")
	}
	// Observable from the moment it is created, not from first progress:
	// the gap between the scheduler deciding and the first chunk uploading
	// is minutes on a large volume, and a panel showing nothing during it is
	// the reported bug all over again.
	if run.State != RunPreparing {
		t.Errorf("state = %q, want %q", run.State, RunPreparing)
	}
	if run.Trigger != TriggerSchedule {
		t.Errorf("trigger = %q, want %q — a scheduled run must not look manual", run.Trigger, TriggerSchedule)
	}
	if run.JobID != "job-7" || run.JobName != "Nightly-C" || run.BackupType != "machine" {
		t.Errorf("identity fields not carried through: %+v", run)
	}
	if !run.Running() {
		t.Error("a just-begun run reports as not running")
	}

	r.Progress(id, 12.5, "reading C:")
	r.Stats(id, 1<<20, 1<<30, 4, 96)
	r.SetCurrentDir(id, `C:\Users`)

	run, _ = r.Get(id)
	if run.State != RunRunning {
		t.Errorf("state after progress = %q, want %q", run.State, RunRunning)
	}
	if run.Percent != 12.5 || run.BytesTotal != 1<<30 || run.ReusedChunks != 96 || run.CurrentDir != `C:\Users` {
		t.Errorf("live fields not recorded: %+v", run)
	}

	clk.advance(90 * time.Second)
	r.Complete(id, true, "Backup completed successfully")

	run, _ = r.Get(id)
	if run.State != RunDone || !run.Success {
		t.Errorf("terminal state wrong: state=%q success=%v", run.State, run.Success)
	}
	if run.Percent != 100 {
		t.Errorf("a successful run ends at %v%%, want 100 — a panel stuck at 97%% reads as hung", run.Percent)
	}
	if got := run.DurationSec(clk.now()); got != 90 {
		t.Errorf("duration = %v, want 90", got)
	}
	if run.Running() {
		t.Error("a completed run still reports as running")
	}
}

func TestProgressAfterCompleteIsIgnored(t *testing.T) {
	r, _ := newTestRegistry()
	id := r.Begin(TriggerManual, "", "", "WS-01", "directory")
	r.Complete(id, true, "done")

	// Engines emit late callbacks after their completion callback. Letting
	// one through would move the run out of the terminal state and leave a
	// permanently "running" entry the status panel never clears.
	r.Progress(id, 40, "still going")
	r.Stats(id, 999, 999, 9, 9)
	r.SetCurrentDir(id, "should-not-appear")

	run, _ := r.Get(id)
	if run.State != RunDone {
		t.Fatalf("a late progress callback resurrected a finished run: state=%q", run.State)
	}
	if run.Percent != 100 || run.NewChunks != 0 || run.CurrentDir != "" {
		t.Errorf("late callbacks mutated a finished run: %+v", run)
	}
}

func TestCompleteIsIdempotentAndFirstWriterWins(t *testing.T) {
	r, _ := newTestRegistry()
	id := r.Begin(TriggerManual, "", "", "WS-01", "machine")

	// This is exactly the cancel path: handleBackupCancel marks the run
	// cancelled, then the engine unwinds and the async runner calls Complete
	// again with the resulting error. The cancellation must be what survives.
	r.Complete(id, false, "Backup cancelled")
	r.Complete(id, false, "context canceled")

	run, _ := r.Get(id)
	if run.Message != "Backup cancelled" {
		t.Errorf("message = %q, want the first outcome to stick", run.Message)
	}
}

func TestUnknownRunIsANoOpNotAPanic(t *testing.T) {
	r, _ := newTestRegistry()
	r.Progress("nope", 50, "x")
	r.Stats("nope", 1, 2, 3, 4)
	r.SetCurrentDir("nope", "x")
	r.Complete("nope", true, "x")
	r.SetState("nope", RunRunning)
	if _, ok := r.Get("nope"); ok {
		t.Error("Get invented a run")
	}
	if n := r.ActiveCount(); n != 0 {
		t.Errorf("ActiveCount = %d, want 0", n)
	}
}

func TestSetStateCannotReachTerminal(t *testing.T) {
	r, _ := newTestRegistry()
	id := r.Begin(TriggerManual, "", "", "WS-01", "directory")

	// Terminal state is reachable only through Complete, so no caller can
	// mark a run finished without recording whether it succeeded.
	r.SetState(id, RunDone)
	run, _ := r.Get(id)
	if run.State == RunDone {
		t.Fatal("SetState reached the terminal state without an outcome")
	}

	r.SetState(id, RunFinalizing)
	run, _ = r.Get(id)
	if run.State != RunFinalizing {
		t.Errorf("state = %q, want %q", run.State, RunFinalizing)
	}
}

func TestActiveSeesRunsNobodyStartedOverHTTP(t *testing.T) {
	// The whole point. A run registered directly by the service — which is
	// what a scheduled backup is — must be visible to every observer.
	r, _ := newTestRegistry()

	manual := r.Begin(TriggerManual, "", "", "WS-01", "directory")
	r.Complete(manual, true, "done")
	sched := r.Begin(TriggerSchedule, "job-7", "Nightly-C", "WS-01", "machine")

	active := r.Active()
	if len(active) != 1 || active[0].RunID != sched {
		t.Fatalf("Active() = %+v, want exactly the scheduled run", active)
	}
	if r.ActiveCount() != 1 {
		t.Errorf("ActiveCount = %d, want 1", r.ActiveCount())
	}
}

func TestRecentIsNewestFirstAndIncludesLiveRuns(t *testing.T) {
	r, clk := newTestRegistry()

	old := r.Begin(TriggerSchedule, "", "", "WS-01", "machine")
	r.Complete(old, true, "done")
	clk.advance(48 * time.Hour)
	mid := r.Begin(TriggerSchedule, "", "", "WS-01", "machine")
	r.Complete(mid, false, "failed")
	clk.advance(48 * time.Hour)
	live := r.Begin(TriggerSchedule, "", "", "WS-01", "machine")

	got := r.Recent(7 * 24 * time.Hour)
	if len(got) != 3 {
		t.Fatalf("Recent(7d) returned %d runs, want 3", len(got))
	}
	if got[0].RunID != live {
		t.Errorf("newest run is %q, want the live one (%q) at the top of the panel", got[0].RunID, live)
	}
	if got[2].RunID != old {
		t.Errorf("oldest run is %q, want %q", got[2].RunID, old)
	}

	// The window is a window: a run outside it is not "recent".
	if got := r.Recent(24 * time.Hour); len(got) != 1 || got[0].RunID != live {
		t.Errorf("Recent(1d) = %+v, want only the live run", got)
	}
}

func TestRetentionNeverPrunesARunningBackup(t *testing.T) {
	// A multi-terabyte image backup over a slow link can outlive any sensible
	// retention window. Dropping it would make the panel report that nothing
	// is running while the disk is visibly busy — the exact failure this type
	// exists to end.
	r, clk := newTestRegistry()

	longRun := r.Begin(TriggerSchedule, "job-7", "Full image", "SRV-01", "machine")
	finished := r.Begin(TriggerManual, "", "", "SRV-01", "directory")
	r.Complete(finished, true, "done")

	clk.advance(30 * 24 * time.Hour) // far past defaultRetain
	r.Begin(TriggerManual, "", "", "SRV-01", "directory")

	if _, ok := r.Get(longRun); !ok {
		t.Fatal("a still-running backup was pruned by retention")
	}
	if _, ok := r.Get(finished); ok {
		t.Error("a finished run older than retention was kept")
	}
}

func TestCountCapDropsOldestFinishedAndTerminates(t *testing.T) {
	r, _ := newTestRegistry()
	r.maxRuns = 3

	live := r.Begin(TriggerSchedule, "", "", "WS-01", "machine")
	var firstDone string
	for i := 0; i < 5; i++ {
		id := r.Begin(TriggerManual, "", "", "WS-01", "directory")
		if i == 0 {
			firstDone = id
		}
		r.Complete(id, true, "done")
	}

	if _, ok := r.Get(live); !ok {
		t.Error("the count cap pruned a running backup")
	}
	if _, ok := r.Get(firstDone); ok {
		t.Error("the count cap kept the oldest finished run")
	}

	// Every entry running and over the cap must not spin forever.
	r2, _ := newTestRegistry()
	r2.maxRuns = 1
	for i := 0; i < 4; i++ {
		r2.Begin(TriggerSchedule, "", "", "WS-01", "machine")
	}
	if n := r2.ActiveCount(); n != 4 {
		t.Errorf("ActiveCount = %d, want 4 — running runs must survive the cap", n)
	}
}

func TestPercentIsClamped(t *testing.T) {
	r, _ := newTestRegistry()
	id := r.Begin(TriggerManual, "", "", "WS-01", "directory")

	r.Progress(id, 140, "")
	if run, _ := r.Get(id); run.Percent != 100 {
		t.Errorf("percent = %v, want clamped to 100", run.Percent)
	}
	r.Progress(id, -5, "")
	if run, _ := r.Get(id); run.Percent != 0 {
		t.Errorf("percent = %v, want clamped to 0", run.Percent)
	}
}

func TestRunIDsAreUniqueWithinTheSameSecond(t *testing.T) {
	// The clock does not move here, which is the realistic case: Windows'
	// clock is coarse and two runs starting in one second is ordinary. The
	// previous scheme needed a counter for exactly this reason.
	r, _ := newTestRegistry()
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id := r.Begin(TriggerManual, "", "", "WS-01", "directory")
		if seen[id] {
			t.Fatalf("duplicate run id %q — two runs would share one entry", id)
		}
		seen[id] = true
	}
}

func TestConcurrentWritersAndReaders(t *testing.T) {
	// Run under -race in CI. Engine callbacks fire from upload workers while
	// HTTP handlers read; this is the shape of that.
	r, _ := newTestRegistry()
	ids := make([]string, 4)
	for i := range ids {
		ids[i] = r.Begin(TriggerSchedule, "", "", "WS-01", "machine")
	}

	var wg sync.WaitGroup
	for _, id := range ids {
		id := id
		wg.Add(3)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				r.Progress(id, float64(i%100), "chunking")
				r.Stats(id, uint64(i), 1000, uint64(i), uint64(i))
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				// Read the FIELDS, not just the struct. If an accessor
				// ever hands back the live *Run instead of a copy, this
				// is what -race trips on; a test that only calls the
				// accessor and discards the value proves nothing.
				if run, ok := r.Get(id); ok {
					_ = run.Percent + float64(run.BytesDone)
					_ = run.Message + string(run.State)
				}
				for _, run := range r.Active() {
					_ = run.Percent + float64(run.NewChunks)
					_ = run.CurrentDir
				}
				_ = r.ActiveCount()
			}
		}()
		go func() {
			defer wg.Done()
			_ = r.Recent(7 * 24 * time.Hour)
		}()
	}
	wg.Wait()

	for _, id := range ids {
		r.Complete(id, true, "done")
	}
	if n := r.ActiveCount(); n != 0 {
		t.Errorf("ActiveCount = %d after completing everything, want 0", n)
	}
}

// ---- HTTP surface -------------------------------------------------------

func newRunsTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	// The real stub from the smoke suite: /status delegates to the handler,
	// and a nil one would make this pass or fail for the wrong reason.
	s := NewServer("127.0.0.1:0", newStub(), testToken)
	ts := httptest.NewServer(s.authMiddleware(s.mux))
	t.Cleanup(ts.Close)
	return s, ts
}

func getJSON(t *testing.T, ts *httptest.Server, path string, out any) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(tokenHeader, testToken)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decoding %s: %v", path, err)
		}
	}
	return resp.StatusCode
}

func TestRunsEndpointsSeeAServiceStartedRun(t *testing.T) {
	s, ts := newRunsTestServer(t)

	// Registered directly on the registry — no HTTP call started this. That
	// is precisely what the scheduler does, and precisely what used to be
	// invisible.
	id := s.Runs().Begin(TriggerSchedule, "job-7", "Nightly-C", "WS-01", "machine")
	s.Runs().Progress(id, 33, "reading C:")

	var active RunsResponse
	if code := getJSON(t, ts, "/runs/active", &active); code != http.StatusOK {
		t.Fatalf("/runs/active = %d, want 200", code)
	}
	if active.Count != 1 || len(active.Runs) != 1 {
		t.Fatalf("/runs/active returned %d runs, want 1", active.Count)
	}
	if active.Runs[0].RunID != id || active.Runs[0].Trigger != TriggerSchedule {
		t.Errorf("wrong run reported: %+v", active.Runs[0])
	}
	if active.Runs[0].Percent != 33 {
		t.Errorf("percent = %v, want 33", active.Runs[0].Percent)
	}
	if active.ServerTime.IsZero() {
		t.Error("server_time absent — a client cannot compute elapsed time safely without it")
	}

	var one Run
	if code := getJSON(t, ts, "/runs/"+id, &one); code != http.StatusOK {
		t.Fatalf("/runs/{id} = %d, want 200", code)
	}
	if one.JobName != "Nightly-C" {
		t.Errorf("job name = %q, want Nightly-C", one.JobName)
	}

	var recent RunsResponse
	if code := getJSON(t, ts, "/runs/recent", &recent); code != http.StatusOK {
		t.Fatalf("/runs/recent = %d, want 200", code)
	}
	if recent.Count != 1 {
		t.Errorf("/runs/recent count = %d, want 1 — a live run belongs in the panel", recent.Count)
	}
}

func TestStatusReportsRealActiveCountAndVersion(t *testing.T) {
	s, ts := newRunsTestServer(t)
	s.SetVersion("4.0.0-dev.99")

	var st StatusResponse
	if code := getJSON(t, ts, "/status", &st); code != http.StatusOK {
		t.Fatalf("/status = %d, want 200", code)
	}
	if st.ActiveJobs != 0 {
		t.Errorf("ActiveJobs = %d with nothing running, want 0", st.ActiveJobs)
	}
	if st.Version != "4.0.0-dev.99" {
		t.Errorf("Version = %q, want the stamped build version", st.Version)
	}

	s.Runs().Begin(TriggerSchedule, "", "", "WS-01", "machine")
	s.Runs().Begin(TriggerPortal, "", "", "WS-01", "directory")

	if code := getJSON(t, ts, "/status", &st); code != http.StatusOK {
		t.Fatalf("/status = %d, want 200", code)
	}
	// The literal `ActiveJobs: 0, // TODO` is what made /status useless for
	// answering "is a backup running".
	if st.ActiveJobs != 2 {
		t.Errorf("ActiveJobs = %d, want 2", st.ActiveJobs)
	}
}

func TestStatusVersionIsUnknownRatherThanWrong(t *testing.T) {
	_, ts := newRunsTestServer(t)
	var st StatusResponse
	if code := getJSON(t, ts, "/status", &st); code != http.StatusOK {
		t.Fatalf("/status = %d, want 200", code)
	}
	// It used to report a hardcoded "0.1.92". A wrong version sends a support
	// engineer to the wrong changelog.
	if st.Version != "unknown" {
		t.Errorf("Version = %q, want %q when nothing stamped it", st.Version, "unknown")
	}
}

func TestRunsRecentRejectsAWindowItCannotHonour(t *testing.T) {
	_, ts := newRunsTestServer(t)
	for _, q := range []string{"?days=0", "?days=-1", "?days=90", "?days=abc"} {
		if code := getJSON(t, ts, "/runs/recent"+q, nil); code != http.StatusBadRequest {
			t.Errorf("/runs/recent%s = %d, want 400 — retention is %v, so a larger window would imply history the service does not keep",
				q, code, defaultRetain)
		}
	}
	if code := getJSON(t, ts, "/runs/recent?days=7", nil); code != http.StatusOK {
		t.Errorf("/runs/recent?days=7 = %d, want 200", code)
	}
}

func TestRunByIDBoundaries(t *testing.T) {
	_, ts := newRunsTestServer(t)
	if code := getJSON(t, ts, "/runs/", nil); code != http.StatusBadRequest {
		t.Errorf("/runs/ = %d, want 400", code)
	}
	if code := getJSON(t, ts, "/runs/no-such-run", nil); code != http.StatusNotFound {
		t.Errorf("/runs/no-such-run = %d, want 404", code)
	}
}

func TestRunsEndpointsRequireTheLocalToken(t *testing.T) {
	s, ts := newRunsTestServer(t)
	s.Runs().Begin(TriggerSchedule, "", "", "WS-01", "machine")

	// The new routes are privileged like every other: a run record names
	// hostnames, directories and job names.
	for _, path := range []string{"/runs/active", "/runs/recent", "/runs/anything"} {
		resp, err := ts.Client().Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		code := resp.StatusCode
		_ = resp.Body.Close()
		if code != http.StatusUnauthorized {
			t.Errorf("unauthenticated GET %s = %d, want 401", path, code)
		}
	}
}

func TestRunsEndpointsRefuseWrites(t *testing.T) {
	_, ts := newRunsTestServer(t)
	for _, path := range []string{"/runs/active", "/runs/recent", "/runs/x"} {
		req, err := http.NewRequest(http.MethodPost, ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(tokenHeader, testToken)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		code := resp.StatusCode
		_ = resp.Body.Close()
		if code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405 — these are read-only", path, code)
		}
	}
}
