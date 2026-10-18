package api

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The run history file (runs_store.go, docs/V4-CONSOLE.md §4.2).
//
// "Restart" here is what it is in production: a new registry handed the bytes
// the old one last wrote. The file sits between them as bytes, so these tests
// cover the format as well as the logic, and nothing in them reaches back into
// the first registry's memory.

// memFile is the service's atomic writer, in memory: the last bytes written
// win, and every write is counted.
type memFile struct {
	mu     sync.Mutex
	data   []byte
	writes int
	fail   error
}

func (f *memFile) save(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if f.fail != nil {
		return f.fail
	}
	f.data = append([]byte(nil), b...)
	return nil
}

func (f *memFile) bytes() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.data...)
}

func (f *memFile) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

// restart is a new service process: a fresh registry on the same clock, loading
// what the previous one left on disk.
func restart(t *testing.T, f *memFile, clk *fakeClock) (*RunRegistry, int) {
	t.Helper()
	r := NewRunRegistry()
	r.now = clk.now
	n, err := r.Persist(f.bytes(), f.save)
	if err != nil {
		t.Fatalf("Persist after restart: %v", err)
	}
	return r, n
}

func TestRunHistorySurvivesARestart(t *testing.T) {
	r, clk := newTestRegistry()
	f := &memFile{}
	if _, err := r.Persist(nil, f.save); err != nil {
		t.Fatalf("Persist on a first start: %v", err)
	}

	ok := r.Begin(TriggerSchedule, "job-7", "Nightly-C", "WS-01", "machine")
	clk.advance(time.Minute)
	r.Stats(ok, 4096, 8192, 3, 5)
	r.Complete(ok, true, "Backup completed successfully")
	failed := r.Begin(TriggerManual, "", "Manual backup", "WS-01", "directory")
	clk.advance(time.Minute)
	r.Complete(failed, false, "PBS HTTP 403 for datastore status")

	clk.advance(time.Hour)
	r2, interrupted := restart(t, f, clk)
	if interrupted != 0 {
		t.Errorf("interrupted = %d, want 0: nothing was running", interrupted)
	}

	got, found := r2.Get(ok)
	if !found {
		t.Fatalf("a finished run did not survive the restart; history: %s", f.bytes())
	}
	if got.State != RunDone || !got.Success || got.JobName != "Nightly-C" || got.Trigger != TriggerSchedule {
		t.Errorf("the successful run came back changed: %+v", got)
	}
	if got.BytesDone != 4096 || got.NewChunks != 3 || got.ReusedChunks != 5 {
		t.Errorf("the run's counters did not survive: %+v", got)
	}
	bad, found := r2.Get(failed)
	if !found || bad.Success || bad.Error != "PBS HTTP 403 for datastore status" {
		t.Errorf("the failed run came back as %+v (found %v)", bad, found)
	}
	// The endpoint the console's history table moves onto in C2.
	if recent := r2.Recent(7 * 24 * time.Hour); len(recent) != 2 || recent[0].RunID != failed {
		t.Errorf("Recent after restart = %+v, want both runs, newest first", recent)
	}
}

func TestARunTheServiceDiedDuringIsClosedAsInterrupted(t *testing.T) {
	r, clk := newTestRegistry()
	f := &memFile{}
	if _, err := r.Persist(nil, f.save); err != nil {
		t.Fatal(err)
	}
	live := r.Begin(TriggerSchedule, "job-1", "Nightly", "SRV-01", "machine")
	r.Progress(live, 40, "uploading")
	// The service is killed here: no Complete. Only Begin wrote the file.

	clk.advance(10 * time.Minute)
	r2, interrupted := restart(t, f, clk)
	if interrupted != 1 {
		t.Fatalf("interrupted = %d, want 1", interrupted)
	}
	run, ok := r2.Get(live)
	if !ok {
		t.Fatal("the run the service died during is missing from the history: it began, so it must be there")
	}
	if run.State != RunDone || run.Success || run.Error != InterruptedRunText || run.Message != InterruptedRunText {
		t.Errorf("the interrupted run reads %+v; want done, failed, with %q", run, InterruptedRunText)
	}
	if run.EndedAt.IsZero() {
		t.Error("the interrupted run has no end time; its duration would grow forever")
	}
	if r2.ActiveCount() != 0 {
		t.Error("a run from a dead process is reported as running")
	}

	// And the file says so too, so the next restart does not count it again.
	r3, again := restart(t, f, clk)
	if again != 0 {
		t.Errorf("the same run was closed as interrupted twice (second count %d)", again)
	}
	if run, _ := r3.Get(live); run.Error != InterruptedRunText {
		t.Errorf("after a second restart the run reads %+v", run)
	}
}

func TestHistoryIsWrittenAtBeginAndEndNotOnProgress(t *testing.T) {
	r, _ := newTestRegistry()
	f := &memFile{}
	if _, err := r.Persist(nil, f.save); err != nil {
		t.Fatal(err)
	}
	if f.count() != 0 {
		t.Fatalf("an empty first start wrote the file %d times", f.count())
	}

	id := r.Begin(TriggerManual, "", "", "WS-01", "directory")
	if f.count() != 1 {
		t.Fatalf("writes after Begin = %d, want 1", f.count())
	}
	for i := 0; i < 50; i++ {
		r.Progress(id, float64(i), "working")
		r.Stats(id, uint64(i), 100, 1, 1)
	}
	r.SetState(id, RunFinalizing)
	r.SetCurrentDir(id, `C:\Users`)
	if f.count() != 1 {
		t.Errorf("progress wrote the file: %d writes, want 1", f.count())
	}
	r.Complete(id, true, "done")
	r.Complete(id, false, "late duplicate") // ignored, so no write either
	if f.count() != 2 {
		t.Errorf("writes after Complete = %d, want 2", f.count())
	}
}

func TestHistoryPrunesPastRetentionOnLoad(t *testing.T) {
	r, clk := newTestRegistry()
	f := &memFile{}
	if _, err := r.Persist(nil, f.save); err != nil {
		t.Fatal(err)
	}
	old := r.Begin(TriggerManual, "", "", "WS-01", "directory")
	r.Complete(old, true, "done")

	// The machine was off for longer than retention.
	clk.advance(defaultRetain + 24*time.Hour)
	r2, _ := restart(t, f, clk)
	if _, ok := r2.Get(old); ok {
		t.Error("a run older than retention was loaded")
	}
	var file runHistoryFile
	if err := json.Unmarshal(f.bytes(), &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Runs) != 0 {
		t.Errorf("the file still carries %d expired runs after the load rewrote it", len(file.Runs))
	}
}

func TestANewRunAfterRestartNeverReusesALoadedID(t *testing.T) {
	r, clk := newTestRegistry()
	f := &memFile{}
	if _, err := r.Persist(nil, f.save); err != nil {
		t.Fatal(err)
	}
	first := r.Begin(TriggerManual, "", "", "WS-01", "directory")
	r.Complete(first, true, "done")

	// The service restarts within the same second: the counter starts over.
	r2, _ := restart(t, f, clk)
	second := r2.Begin(TriggerManual, "", "", "WS-01", "directory")
	if second == first {
		t.Fatalf("the new run reused the loaded id %q, overwriting its history", first)
	}
	if run, _ := r2.Get(first); !run.Success {
		t.Errorf("the loaded run was overwritten: %+v", run)
	}
}

func TestAnUnreadableHistoryIsRefusedNotHalfRead(t *testing.T) {
	for name, data := range map[string]string{
		"not json":      "{this is not json",
		"other version": `{"version":99,"runs":[{"run_id":"run-1-1","state":"done"}]}`,
		"bare array":    `[{"run_id":"run-1-1","state":"done"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := newTestRegistry()
			f := &memFile{}
			if _, err := r.Persist([]byte(data), f.save); err == nil {
				t.Fatal("Persist accepted an unreadable history")
			}
			if n := len(r.Recent(defaultRetain)); n != 0 {
				t.Errorf("%d runs were loaded from an unreadable file", n)
			}
			if f.count() != 0 {
				t.Error("refusing the file wrote over it; the caller must get the chance to move it aside")
			}
			// The registry still works and still writes from here on.
			id := r.Begin(TriggerManual, "", "", "WS-01", "directory")
			if _, ok := r.Get(id); !ok || f.count() != 1 {
				t.Errorf("the registry did not carry on after a bad file (writes %d)", f.count())
			}
		})
	}
}

func TestAWriteFailureDoesNotStopTheRegistry(t *testing.T) {
	r, _ := newTestRegistry()
	f := &memFile{fail: errors.New("disk full")}
	if _, err := r.Persist(nil, f.save); err != nil {
		t.Fatal(err)
	}
	id := r.Begin(TriggerManual, "", "", "WS-01", "directory")
	r.Complete(id, true, "done")
	if run, ok := r.Get(id); !ok || !run.Success {
		t.Errorf("a failing disk changed what the registry reports: %+v", run)
	}
	if f.count() != 2 {
		t.Errorf("writes attempted = %d, want 2: each one tries again", f.count())
	}
}

func TestTheHistoryFileCarriesItsVersionAndNoProgressNoise(t *testing.T) {
	r, _ := newTestRegistry()
	f := &memFile{}
	if _, err := r.Persist(nil, f.save); err != nil {
		t.Fatal(err)
	}
	id := r.Begin(TriggerSchedule, "job-1", "Nightly", "WS-01", "machine")
	r.Complete(id, true, "done")

	var file runHistoryFile
	if err := json.Unmarshal(f.bytes(), &file); err != nil {
		t.Fatalf("the file is not the documented shape: %v", err)
	}
	if file.Version != runHistoryVersion {
		t.Errorf("version = %d, want %d", file.Version, runHistoryVersion)
	}
	if file.SavedAt.IsZero() {
		t.Error("saved_at is missing")
	}
	if len(file.Runs) != 1 || file.Runs[0].RunID != id {
		t.Errorf("runs = %+v", file.Runs)
	}
	if !strings.Contains(string(f.bytes()), `"version":1`) {
		t.Errorf("version is not a top-level field: %s", f.bytes())
	}
}

func TestPersistUnderConcurrentRunsLeavesTheNewestState(t *testing.T) {
	// Many runs ending at once from different goroutines. Whatever order the
	// writes land in, the file must end as the registry ends.
	r, clk := newTestRegistry()
	f := &memFile{}
	if _, err := r.Persist(nil, f.save); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = r.Begin(TriggerSchedule, "", "", "WS-01", "machine")
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			r.Progress(id, 50, "half")
			r.Complete(id, true, "done")
		}(id)
	}
	wg.Wait()

	r2, interrupted := restart(t, f, clk)
	if interrupted != 0 {
		t.Errorf("%d runs read as interrupted: an older snapshot was written last", interrupted)
	}
	for _, id := range ids {
		if run, ok := r2.Get(id); !ok || !run.Success {
			t.Errorf("run %s after restart = %+v (found %v)", id, run, ok)
		}
	}
}
