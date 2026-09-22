package main

import (
	"controlplane"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// The WARN/ERROR push to the control plane (V4-RUN-AUDIT §4.1).
//
// WHY. The client has well over a hundred writeWarnLog/writeErrorLog sites and
// none of them reached the server. The only path was an on-demand pull scoped
// to one run's time window that needed the machine online -- so a warning at
// 03:00 on a laptop that has since slept was unreachable. These lines now go
// into a bounded queue ON DISK and ride the next check-in.
//
// WHY PERSISTENT. The failure this fixes is a warning on a machine that then
// sleeps, reboots or loses the network; a queue in memory loses exactly the
// records that matter most.
//
// BOUNDS, all of them reported rather than silent:
//   - count and bytes: overflow drops the OLDEST and increments Dropped, which
//     travels with every batch. A queue that silently drops is a queue that
//     lies; one that grows without bound fills a customer's disk.
//   - rate (§5 rule 7): per component per minute. Past the ceiling, lines are
//     counted, and one summary line says how many when the minute closes.
//
// ONLY THE SERVICE QUEUES. The GUI process writes its own log and does not
// check in; two processes appending to one queue file would race. Enabled by
// logqueue_enable_service.go, so the GUI build compiles this and never uses it.

const (
	logQueueMaxEntries = 500
	logQueueMaxBytes   = 256 << 10
	// A check-in carries at most this many. 50 x the 2,000-byte message cap is
	// well under the server's 256 KiB agent-body limit with inventory beside
	// it; a backlog drains over a few cycles instead of oversizing one.
	logBatchMax = 50
	// §5 rule 7. Twenty distinct warnings a minute from one component is
	// already a flood from a human's point of view.
	logRatePerComponentPerMinute = 20
	logMessageMax                = 2000
	logQueueFileName             = "logqueue.json"
)

type logQueueState struct {
	Queue   string                  `json:"queue"`
	Seq     int64                   `json:"seq"`
	Dropped int64                   `json:"dropped"`
	Entries []controlplane.LogEntry `json:"entries"`
}

type rateWindow struct {
	minute     int64
	count      int
	suppressed int
}

type logQueue struct {
	mu    sync.Mutex
	path  string
	st    logQueueState
	rate  map[string]*rateWindow
	now   func() time.Time
	bytes int
}

var (
	logQueueEnabled bool // set by the service build only
	activeLogQueue  *logQueue
	activeLogQMu    sync.Mutex
)

// openLogQueue loads (or starts) the queue at path. A corrupt file is not
// fatal: it is set aside, a fresh queue starts, and that fact is itself the
// first entry -- the one thing this queue must never do is stop recording.
func openLogQueue(path string) *logQueue {
	q := &logQueue{path: path, rate: map[string]*rateWindow{}, now: time.Now}
	data, err := os.ReadFile(path)
	if err == nil {
		if jerr := json.Unmarshal(data, &q.st); jerr != nil {
			_ = os.Rename(path, path+".corrupt")
			q.st = logQueueState{Queue: newQueueID()}
			q.appendLocked("error", "logqueue", fmt.Sprintf("log queue file was unreadable and was set aside: %v", jerr), "")
		}
	}
	if q.st.Queue == "" {
		q.st.Queue = newQueueID()
		q.persistLocked()
	}
	q.recount()
	return q
}

// newQueueID names one instance of the queue file (see LogBatch.Queue).
func newQueueID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; a clock-derived
		// id is still unique per instance in practice, and never empty.
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (q *logQueue) recount() {
	q.bytes = 0
	for _, e := range q.st.Entries {
		q.bytes += entrySize(e)
	}
}

func entrySize(e controlplane.LogEntry) int {
	return len(e.Message) + len(e.Component) + len(e.RunUUID) + 64
}

// Add queues one line. level is "warn" or "error". Never blocks on the
// network and never returns an error: logging must not be able to fail a
// caller.
func (q *logQueue) Add(level, component, message, runUUID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.flushClosedWindowsLocked()
	minute := q.now().Unix() / 60
	w := q.rate[component]
	if w == nil || w.minute != minute {
		w = &rateWindow{minute: minute}
		q.rate[component] = w
	}
	if w.count >= logRatePerComponentPerMinute {
		w.suppressed++
		return
	}
	w.count++
	q.appendLocked(level, component, message, runUUID)
	q.persistLocked()
}

// flushClosedWindowsLocked turns every closed minute that suppressed lines
// into one summary entry. Called on every Add and every Pending, so a flood
// that stops is still summarized at the next check-in.
func (q *logQueue) flushClosedWindowsLocked() {
	minute := q.now().Unix() / 60
	comps := make([]string, 0, len(q.rate))
	for c := range q.rate {
		comps = append(comps, c)
	}
	sort.Strings(comps)
	for _, c := range comps {
		w := q.rate[c]
		if w.minute == minute {
			continue
		}
		if w.suppressed > 0 {
			q.appendLocked("warn", c, fmt.Sprintf(
				"%d more warning/error line(s) from this component were suppressed by the per-minute ceiling (%d/min); see the machine's local log", w.suppressed, logRatePerComponentPerMinute), "")
		}
		delete(q.rate, c)
	}
}

func (q *logQueue) appendLocked(level, component, message, runUUID string) {
	if len(message) > logMessageMax {
		message = message[:logMessageMax]
	}
	q.st.Seq++
	e := controlplane.LogEntry{
		Seq: q.st.Seq, At: q.now().UTC().Format(time.RFC3339), Level: level,
		Component: component, Message: message, RunUUID: runUUID,
	}
	q.st.Entries = append(q.st.Entries, e)
	q.bytes += entrySize(e)
	for len(q.st.Entries) > logQueueMaxEntries || (q.bytes > logQueueMaxBytes && len(q.st.Entries) > 1) {
		q.bytes -= entrySize(q.st.Entries[0])
		q.st.Entries = q.st.Entries[1:]
		q.st.Dropped++
	}
}

// Pending returns the oldest batch for the next check-in, or nil.
func (q *logQueue) Pending() *controlplane.LogBatch {
	q.mu.Lock()
	defer q.mu.Unlock()
	before := len(q.st.Entries)
	q.flushClosedWindowsLocked()
	if len(q.st.Entries) != before {
		q.persistLocked()
	}
	if len(q.st.Entries) == 0 {
		return nil
	}
	n := len(q.st.Entries)
	if n > logBatchMax {
		n = logBatchMax
	}
	out := make([]controlplane.LogEntry, n)
	copy(out, q.st.Entries[:n])
	return &controlplane.LogBatch{Queue: q.st.Queue, Entries: out, Dropped: q.st.Dropped}
}

// Ack trims everything the server says it has stored.
func (q *logQueue) Ack(seq int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := 0
	for i < len(q.st.Entries) && q.st.Entries[i].Seq <= seq {
		i++
	}
	if i == 0 {
		return
	}
	q.st.Entries = q.st.Entries[i:]
	q.recount()
	q.persistLocked()
}

// persistLocked writes the queue atomically (temp file, then rename), so a
// power cut mid-write leaves the previous queue rather than half of one.
// Failures go to stderr only: reporting them through writeWarnLog would
// re-enter this queue.
func (q *logQueue) persistLocked() {
	if q.path == "" {
		return
	}
	data, err := json.Marshal(q.st)
	if err != nil {
		return
	}
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "log queue: write failed: %v\n", err)
		return
	}
	if err := os.Rename(tmp, q.path); err != nil {
		fmt.Fprintf(os.Stderr, "log queue: rename failed: %v\n", err)
	}
}

// enableLogQueue opens the service's queue. Idempotent.
func enableLogQueue() {
	if !logQueueEnabled {
		return
	}
	activeLogQMu.Lock()
	defer activeLogQMu.Unlock()
	if activeLogQueue == nil {
		activeLogQueue = openLogQueue(filepath.Join(logDir, logQueueFileName))
	}
}

func currentLogQueue() *logQueue {
	activeLogQMu.Lock()
	defer activeLogQMu.Unlock()
	return activeLogQueue
}

// componentPattern reads the "[Component]" prefix most call sites already
// write ("[BackupKey] ...", "[controlplane] ..."). Lines without one are
// filed under "service".
var componentPattern = regexp.MustCompile(`^\[([A-Za-z][A-Za-z0-9_ .-]{0,31})\]\s*`)

func componentOf(message string) string {
	if m := componentPattern.FindStringSubmatch(message); m != nil {
		return m[1]
	}
	return "service"
}

// queueForServer is the one hook writeWarnLog/writeErrorLog call. The message
// has already been redacted by the caller (§5 rule 8: these lines now leave
// the machine).
func queueForServer(level, redacted string) {
	q := currentLogQueue()
	if q == nil {
		return
	}
	q.Add(level, componentOf(redacted), redacted, currentRunForLogs())
}

// ------------------------------------------------------------ run context
//
// A line gets the run_uuid of the run it happened inside, so it shows on that
// run's report. There is no per-goroutine context in this codebase to carry it,
// so the rule is deliberately conservative: when EXACTLY ONE run is active, a
// line belongs to it; with none or several, the line is attached to the machine
// only. Mis-attributing a line to the wrong run would be worse than not
// attributing it.

var (
	runCtxMu   sync.Mutex
	activeRuns = map[string]int{}
)

func beginRunLogContext(runUUID string) func() {
	if runUUID == "" {
		return func() {}
	}
	runCtxMu.Lock()
	activeRuns[runUUID]++
	runCtxMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			runCtxMu.Lock()
			if activeRuns[runUUID]--; activeRuns[runUUID] <= 0 {
				delete(activeRuns, runUUID)
			}
			runCtxMu.Unlock()
		})
	}
}

func currentRunForLogs() string {
	runCtxMu.Lock()
	defer runCtxMu.Unlock()
	if len(activeRuns) != 1 {
		return ""
	}
	for u := range activeRuns {
		return u
	}
	return ""
}
