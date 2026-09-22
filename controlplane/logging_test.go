package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// captureLog installs a sink for the duration of one test and returns what it
// received. Tests in this file must not run in parallel with each other.
type captured struct {
	mu    sync.Mutex
	lines []string
}

func (c *captured) add(l LogLevel, m string) {
	c.mu.Lock()
	c.lines = append(c.lines, l.String()+" "+m)
	c.mu.Unlock()
}

func (c *captured) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

func (c *captured) count(prefix, substr string) int {
	n := 0
	for _, l := range c.all() {
		if strings.HasPrefix(l, prefix) && strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

func captureLog(t *testing.T) *captured {
	t.Helper()
	c := &captured{}
	SetLogger(c.add)
	t.Cleanup(func() { SetLogger(nil) })
	return c
}

func TestRepeatGateCollapsesAndReportsRecovery(t *testing.T) {
	var g repeatGate
	if g.ok() != "" {
		t.Fatal("a gate that never failed must have nothing to report on success")
	}
	if got := g.fail("A"); got != "A" {
		t.Fatalf("first failure must be emitted, got %q", got)
	}
	if g.fail("A") != "" || g.fail("A") != "" {
		t.Fatal("repeats of the same failure must be silent")
	}
	got := g.fail("B")
	if !strings.HasPrefix(got, "B") || !strings.Contains(got, "repeated 2 more") {
		t.Fatalf("a changed failure must be emitted with the old one's repeat count, got %q", got)
	}
	rec := g.ok()
	if !strings.Contains(rec, "recovered after 1 consecutive") || !strings.Contains(rec, "B") {
		t.Fatalf("recovery must say how many failures and the last one, got %q", rec)
	}
	if g.ok() != "" {
		t.Fatal("recovery is reported once")
	}
}

// A server that refuses the agent every cycle used to produce one line per
// cycle. It must produce one WARN when it starts and one INFO when it ends.
func TestCheckinFailureIsOneWarnThenOneRecovery(t *testing.T) {
	var refuse atomic.Bool
	refuse.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse.Load() {
			w.WriteHeader(401) // non-retryable, so the test does not sleep through backoff
			_, _ = w.Write([]byte(`{"error":"Invalid agent credentials"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(CheckinResponse{CheckinSeconds: 120})
	}))
	defer srv.Close()
	logs := captureLog(t)
	a := &Agent{Client: &Client{BaseURL: srv.URL, AgentID: 1, Secret: "x"}}

	for i := 0; i < 5; i++ {
		a.CheckinNow()
	}
	if n := logs.count("WARN", "check-in failed"); n != 1 {
		t.Fatalf("five identical failures must log ONE warn, got %d: %v", n, logs.all())
	}
	refuse.Store(false)
	a.CheckinNow()
	if n := logs.count("INFO", "recovered after 5 consecutive"); n != 1 {
		t.Fatalf("recovery must be logged once at INFO with the count, got %v", logs.all())
	}
	a.CheckinNow()
	if n := logs.count("INFO", "recovered"); n != 1 {
		t.Fatalf("a healthy cycle after recovery must not log recovery again, got %v", logs.all())
	}
}

// Severity is carried, not flattened: a failed dispatch is WARN and a panic
// is ERROR, because the push to the server (§4.1) selects on level.
func TestCommandOutcomesCarrySeverity(t *testing.T) {
	srv, _ := fakeServer(t)
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, AgentID: 7, Secret: "s3cret"}
	logs := captureLog(t)

	a := &Agent{Client: c, HandleCommand: func(Command) CommandResult {
		return CommandResult{OK: false, Result: map[string]interface{}{"error": "nope"}}
	}}
	a.CheckinNow()
	if logs.count("WARN", "dispatch FAILED") != 1 {
		t.Fatalf("a failed dispatch must be WARN: %v", logs.all())
	}
	if logs.count("INFO", "delivered 1 command") != 1 {
		t.Fatalf("receipt is INFO: %v", logs.all())
	}

	a2 := &Agent{Client: c, HandleCommand: func(Command) CommandResult { panic("boom") }}
	a2.CheckinNow()
	if logs.count("ERROR", "handler panicked") != 1 {
		t.Fatalf("a handler panic must be ERROR: %v", logs.all())
	}
}

// The log batch rides the check-in, the ack comes from the server's answer,
// and the debug deadline is delivered every cycle -- including 0.
func TestCheckinCarriesLogsAckAndDebug(t *testing.T) {
	var got CheckinRequest
	var debugUntil atomic.Int64
	debugUntil.Store(1893456000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = CheckinRequest{}
		_ = json.NewDecoder(r.Body).Decode(&got)
		ack := int64(0)
		if got.Logs != nil && len(got.Logs.Entries) > 0 {
			ack = got.Logs.Entries[len(got.Logs.Entries)-1].Seq
		}
		_ = json.NewEncoder(w).Encode(CheckinResponse{CheckinSeconds: 120, LogAckSeq: ack, DebugUntil: debugUntil.Load()})
	}))
	defer srv.Close()

	var acked, debug int64 = -1, -1
	batch := &LogBatch{Dropped: 3, Entries: []LogEntry{
		{Seq: 41, At: "2026-09-22T10:00:00Z", Level: "warn", Component: "x", Message: "m1"},
		{Seq: 42, At: "2026-09-22T10:00:01Z", Level: "error", Component: "x", Message: "m2", RunUUID: "r"},
	}}
	a := &Agent{
		Client:       &Client{BaseURL: srv.URL, AgentID: 1, Secret: "x"},
		PendingLogs:  func() *LogBatch { return batch },
		OnLogAck:     func(s int64) { acked = s },
		OnDebugUntil: func(u int64) { debug = u },
	}
	a.CheckinNow()
	if got.Logs == nil || len(got.Logs.Entries) != 2 || got.Logs.Dropped != 3 || got.Logs.Entries[1].RunUUID != "r" {
		t.Fatalf("the batch must be sent whole: %+v", got.Logs)
	}
	if acked != 42 {
		t.Fatalf("the server's ack must reach the queue, got %d", acked)
	}
	if debug != 1893456000 {
		t.Fatalf("the debug deadline must be delivered, got %d", debug)
	}

	// Nothing queued: no batch on the wire, no ack callback, and a debug
	// deadline of 0 is still delivered so "off" reaches the machine.
	batch, acked = nil, -1
	debugUntil.Store(0)
	a.CheckinNow()
	if got.Logs != nil {
		t.Fatalf("an empty queue must send no batch, sent %+v", got.Logs)
	}
	if acked != -1 {
		t.Fatal("no batch sent, so no ack callback")
	}
	if debug != 0 {
		t.Fatalf("debug off must be delivered as 0, got %d", debug)
	}
}

// The regression for runs 63/66's missing timeline lines: events emitted
// right after Preparing() must reach the server AFTER the report that
// creates the run, in emission order, and none may be dropped.
func TestRunReporterDeliversInOrder(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	known := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/agent/v1/runs" {
			var rep RunReport
			_ = json.NewDecoder(r.Body).Decode(&rep)
			known = true
			seen = append(seen, "report:"+string(rep.Status))
			// A little latency, so an unordered sender would lose the race.
			time.Sleep(20 * time.Millisecond)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if !known {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"Unknown run for this agent"}`))
			seen = append(seen, "404")
			return
		}
		var ev RunEvent
		_ = json.NewDecoder(r.Body).Decode(&ev)
		seen = append(seen, "event:"+ev.Message)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	captureLog(t)

	rr := (&Client{BaseURL: srv.URL, AgentID: 1, Secret: "x"}).NewRun("J", "machine")
	rr.Preparing()
	for i := 0; i < 5; i++ {
		rr.Event(CheckpointDisksPartitions, "info", string(rune('a'+i)))
	}
	rr.Running()
	rr.Event(CheckpointSnapshotVSS, "info", "f")
	waitIdle(t, rr)

	want := "report:preparing,event:a,event:b,event:c,event:d,event:e,report:running,event:f"
	mu.Lock()
	gotSeq := strings.Join(seen, ",")
	mu.Unlock()
	if gotSeq != want {
		t.Fatalf("delivery order\n got: %s\nwant: %s", gotSeq, want)
	}
}

// When the creating report itself was refused, an event must not be dropped:
// the reporter re-asserts the latest status and retries the event once.
func TestRunReporterRecoversEventAfterLostCreate(t *testing.T) {
	var mu sync.Mutex
	var reports, stored int
	known := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/agent/v1/runs" {
			reports++
			if reports == 1 {
				w.WriteHeader(400) // non-retryable: the create is lost
				_, _ = w.Write([]byte(`{"error":"transient refusal"}`))
				return
			}
			known = true
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if !known {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"Unknown run for this agent"}`))
			return
		}
		stored++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	logs := captureLog(t)

	rr := (&Client{BaseURL: srv.URL, AgentID: 1, Secret: "x"}).NewRun("J", "machine")
	rr.Preparing()
	rr.Event(CheckpointBackupStart, "info", "after a lost create")
	waitIdle(t, rr)

	mu.Lock()
	defer mu.Unlock()
	if reports != 2 || stored != 1 {
		t.Fatalf("expected the report re-asserted once and the event stored once; reports=%d stored=%d", reports, stored)
	}
	if logs.count("WARN", "report (preparing) not delivered") != 1 {
		t.Fatalf("the lost create is itself reported at WARN: %v", logs.all())
	}
	if logs.count("WARN", "milestone event") != 0 {
		t.Fatalf("a recovered event must not be reported as lost: %v", logs.all())
	}
}

func waitIdle(t *testing.T, rr *RunReporter) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !rr.idle() {
		if time.Now().After(deadline) {
			t.Fatal("reporter did not drain")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
