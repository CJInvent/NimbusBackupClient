package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testQueue(t *testing.T) (*logQueue, string, *time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), logQueueFileName)
	clock := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	q := openLogQueue(path)
	q.now = func() time.Time { return clock }
	return q, path, &clock
}

// The queue survives a restart: the whole reason it is on disk.
func TestLogQueueSurvivesRestart(t *testing.T) {
	q, path, _ := testQueue(t)
	q.Add("warn", "BackupKey", "[BackupKey] first", "")
	q.Add("error", "service", "second", "run-1")

	again := openLogQueue(path)
	b := again.Pending()
	if b == nil || len(b.Entries) != 2 {
		t.Fatalf("a reopened queue must hold what was written, got %+v", b)
	}
	if b.Entries[0].Seq != 1 || b.Entries[1].Seq != 2 || b.Entries[1].RunUUID != "run-1" || b.Entries[1].Level != "error" {
		t.Fatalf("entries lost fields across restart: %+v", b.Entries)
	}
	if b.Queue == "" || b.Queue != q.Pending().Queue {
		t.Fatalf("the queue id must be stable across restart, got %q vs %q", b.Queue, q.Pending().Queue)
	}
	// The sequence continues rather than restarting at 1, or the server's
	// idempotency key would swallow new lines as duplicates.
	again.Add("warn", "service", "third", "")
	if got := again.Pending().Entries[2].Seq; got != 3 {
		t.Fatalf("sequence must continue across restart, got %d", got)
	}
}

// Ack trims what the server stored, and only that; a lost response is
// harmless because the next ack names a sequence, not a batch.
func TestLogQueueAckTrimsStoredOnly(t *testing.T) {
	q, path, _ := testQueue(t)
	for i := 0; i < 5; i++ {
		q.Add("warn", "c", "m", "")
	}
	q.Ack(3)
	b := q.Pending()
	if len(b.Entries) != 2 || b.Entries[0].Seq != 4 {
		t.Fatalf("ack 3 must leave 4 and 5, got %+v", b.Entries)
	}
	q.Ack(3) // repeated ack: no-op
	q.Ack(0)
	if len(q.Pending().Entries) != 2 {
		t.Fatal("a stale ack must not trim anything")
	}
	if len(openLogQueue(path).Pending().Entries) != 2 {
		t.Fatal("the trim must be persisted")
	}
	q.Ack(99)
	if q.Pending() != nil {
		t.Fatal("everything acked: nothing pending")
	}
}

// Overflow drops the OLDEST and says so; it never grows without bound.
func TestLogQueueCapsByCountAndReportsDrops(t *testing.T) {
	q, _, clock := testQueue(t)
	total := logQueueMaxEntries + 37
	for i := 0; i < total; i++ {
		// A new component every line keeps the rate ceiling out of this test.
		q.Add("warn", "c"+string(rune('a'+i%26))+strings.Repeat("x", i/26), "m", "")
		if i%logRatePerComponentPerMinute == 0 {
			*clock = clock.Add(time.Minute)
		}
	}
	if n := len(q.st.Entries); n != logQueueMaxEntries {
		t.Fatalf("queue must hold exactly its cap, holds %d", n)
	}
	b := q.Pending()
	if b.Dropped < 37 {
		t.Fatalf("drops must be counted and reported, got %d", b.Dropped)
	}
	if b.Entries[0].Seq <= 37 {
		t.Fatalf("the OLDEST must be the ones dropped; first remaining seq %d", b.Entries[0].Seq)
	}
	if len(b.Entries) != logBatchMax {
		t.Fatalf("one check-in carries at most %d, got %d", logBatchMax, len(b.Entries))
	}
}

func TestLogQueueCapsByBytes(t *testing.T) {
	q, _, clock := testQueue(t)
	big := strings.Repeat("y", logMessageMax+500) // also exercises the message clip
	for i := 0; i < 400; i++ {
		q.Add("error", "c", big, "")
		if i%logRatePerComponentPerMinute == logRatePerComponentPerMinute-1 {
			*clock = clock.Add(time.Minute)
		}
	}
	if q.bytes > logQueueMaxBytes {
		t.Fatalf("byte cap exceeded: %d > %d", q.bytes, logQueueMaxBytes)
	}
	for _, e := range q.st.Entries {
		if len(e.Message) > logMessageMax {
			t.Fatalf("message not clipped: %d", len(e.Message))
		}
	}
	if q.st.Dropped == 0 {
		t.Fatal("byte overflow must be counted as drops")
	}
}

// §5 rule 7: a flood from one component collapses to the ceiling plus ONE
// summary line saying how many were suppressed; another component is not
// silenced by it.
func TestLogQueueRateCeilingSummarizes(t *testing.T) {
	q, _, clock := testQueue(t)
	for i := 0; i < logRatePerComponentPerMinute+30; i++ {
		q.Add("warn", "Flood", "[Flood] again", "")
	}
	q.Add("warn", "Quiet", "[Quiet] once", "")
	if n := len(q.st.Entries); n != logRatePerComponentPerMinute+1 {
		t.Fatalf("the flood must stop at the ceiling, queue holds %d", n)
	}
	*clock = clock.Add(time.Minute)
	b := q.Pending()
	last := b.Entries[len(b.Entries)-1]
	if last.Component != "Flood" || !strings.Contains(last.Message, "30 more") {
		t.Fatalf("a closed minute must produce one summary with the count, got %+v", last)
	}
	q.Pending()
	count := 0
	for _, e := range q.st.Entries {
		if strings.Contains(e.Message, "suppressed by the per-minute ceiling") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the summary is written once, found %d", count)
	}
}

func TestLogQueueCorruptFileIsSetAsideNotFatal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, logQueueFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	q := openLogQueue(path)
	b := q.Pending()
	if b == nil || len(b.Entries) != 1 || !strings.Contains(b.Entries[0].Message, "unreadable") {
		t.Fatalf("a corrupt queue must be reported as the first entry, got %+v", b)
	}
	if b.Queue == "" {
		t.Fatal("a recreated queue must carry a (new) queue id")
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("the corrupt file must be kept for inspection: %v", err)
	}
}

func TestComponentOf(t *testing.T) {
	for in, want := range map[string]string{
		"[BackupKey] fetch failed":            "BackupKey",
		"[controlplane] check-in":             "controlplane",
		"no prefix here":                      "service",
		"[not closed":                         "service",
		"[" + strings.Repeat("a", 40) + "] x": "service",
	} {
		if got := componentOf(in); got != want {
			t.Errorf("componentOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// A line belongs to a run only when that run is the ONLY one active.
func TestRunLogContextAttributesOnlyUnambiguously(t *testing.T) {
	if currentRunForLogs() != "" {
		t.Fatal("no run active: no attribution")
	}
	endA := beginRunLogContext("A")
	if currentRunForLogs() != "A" {
		t.Fatal("one run active: its lines carry its uuid")
	}
	endB := beginRunLogContext("B")
	if currentRunForLogs() != "" {
		t.Fatal("two runs active: attributing a line to either could be wrong")
	}
	endB()
	endB() // idempotent
	if currentRunForLogs() != "A" {
		t.Fatal("after B ends, A is unambiguous again")
	}
	endA()
	if currentRunForLogs() != "" {
		t.Fatal("all ended: no attribution")
	}
}

// The server debug window lowers the level and expires by itself.
func TestServerDebugWindowExpiresWithoutServer(t *testing.T) {
	prevLevel := activeLevel
	defer func() { activeLevel = prevLevel; debugUntil.Store(0) }()
	activeLevel = levelInfo

	if logLevelEnabled(levelDebug) {
		t.Fatal("INFO machine: DEBUG off by default")
	}
	setServerDebugUntil(time.Now().Add(time.Hour).Unix())
	if !logLevelEnabled(levelDebug) {
		t.Fatal("an open server window must enable DEBUG")
	}
	// The deadline passes with no further word from the server.
	debugUntil.Store(time.Now().Add(-time.Second).Unix())
	if logLevelEnabled(levelDebug) {
		t.Fatal("a passed deadline must restore the machine's own level with no server contact")
	}
	// The server can never make a machine QUIETER than its own setting.
	activeLevel = levelTrace
	setServerDebugUntil(0)
	if !logLevelEnabled(levelTrace) {
		t.Fatal("the window only ever lowers the level")
	}
}
