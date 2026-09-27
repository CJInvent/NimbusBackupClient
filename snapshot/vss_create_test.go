package snapshot

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	vss "github.com/st-matskevich/go-vss"
)

// A snapshotter that answers from a script, and a command runner that records
// what would have been executed. The in-progress failure is produced by the
// library's OWN error constructor and wrapped exactly as go-vss wraps it, so
// the classification is tested against upstream's text, not ours.
type scriptedSnapshotter struct {
	s        *scriptState
	released bool
}

type scriptState struct {
	mu        sync.Mutex
	results   []error // one per CreateSnapshot call; nil = success
	calls     int
	released  int
	failedRel int // Release called on a snapshotter whose create FAILED
}

func (f *scriptedSnapshotter) CreateSnapshot(drive string, _ bool, _ int) (*vss.Snapshot, error) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	i := f.s.calls
	f.s.calls++
	if i < len(f.s.results) && f.s.results[i] != nil {
		f.released = true // marks "failed": any Release after this is the bug
		return nil, f.s.results[i]
	}
	return &vss.Snapshot{Id: fmt.Sprintf("id-%d", i), Drive: drive, DeviceObjectPath: `\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy9\`}, nil
}

func (f *scriptedSnapshotter) Release() error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	if f.released {
		f.s.failedRel++
	}
	f.s.released++
	return nil
}

func libraryError(step string, h vss.HRESULT) error {
	// The two layers go-vss adds, verbatim from vss_windows.go.
	return fmt.Errorf("VSS_SNAPSHOT - Shadow copy creation failed: %s didn't finish properly, err: %s",
		step, vss.CreateVSSError("IVssBackupComponents."+step, uintptr(h)))
}

type harness struct {
	st       *scriptState
	commands []string
	sleeps   []time.Duration
	warns    []string
	errs     []string
}

func setup(t *testing.T, results ...error) *harness {
	t.Helper()
	h := &harness{st: &scriptState{results: results}}
	oldNew, oldRun, oldSleep, oldWarn, oldErr, oldLog := newSnapshotter, runCommand, sleepFn, WarnFn, ErrorFn, LogFn
	newSnapshotter = func() snapshotter { return &scriptedSnapshotter{s: h.st} }
	runCommand = func(name string, args ...string) ([]byte, error) {
		h.commands = append(h.commands, name+" "+strings.Join(args, " "))
		return []byte("Writer name: 'System Writer'\n   State: [1] Stable\n   Last error: No error\n"), nil
	}
	sleepFn = func(d time.Duration) { h.sleeps = append(h.sleeps, d) }
	WarnFn = func(m string) { h.warns = append(h.warns, m) }
	ErrorFn = func(m string) { h.errs = append(h.errs, m) }
	LogFn = func(string) {}
	t.Cleanup(func() {
		newSnapshotter, runCommand, sleepFn, WarnFn, ErrorFn, LogFn = oldNew, oldRun, oldSleep, oldWarn, oldErr, oldLog
	})
	return h
}

func (h *harness) destructive() []string {
	var out []string
	for _, c := range h.commands {
		if strings.Contains(c, "delete") || strings.HasPrefix(c, "net ") || strings.Contains(c, "resize") {
			out = append(out, c)
		}
	}
	return out
}

// Another program's shadow copy is waited out: in progress twice, then the
// snapshot is created. Nothing is deleted and no service is restarted
// (ledger F-42: the old code ran `vssadmin delete shadows /all` and bounced VSS).
func TestAnotherProgramsShadowCopyIsWaitedOut(t *testing.T) {
	inProgress := libraryError("DoSnapshotSet", vss.VSS_E_SNAPSHOT_SET_IN_PROGRESS)
	h := setup(t, inProgress, inProgress, nil)
	cs, err := createVolumeSnapshot(`C:\`, nil)
	if err != nil || cs == nil || cs.snap.Id == "" {
		t.Fatalf("want a snapshot on the third attempt, got %v", err)
	}
	if len(h.sleeps) != 2 || h.sleeps[0] != inProgressWait {
		t.Fatalf("waits %v, want two of %s", h.sleeps, inProgressWait)
	}
	if d := h.destructive(); len(d) > 0 {
		t.Fatalf("destructive commands issued: %q", d)
	}
	if len(h.warns) != 1 || !strings.Contains(h.warns[0], "VSS_E_SNAPSHOT_SET_IN_PROGRESS (0x80042316)") ||
		!strings.Contains(h.warns[0], "Nothing is deleted") {
		t.Fatalf("want ONE warning naming the condition, got %q", h.warns)
	}
	if h.st.failedRel != 0 {
		t.Fatalf("Release was called on %d snapshotter(s) whose create had failed (go-vss already released them)", h.st.failedRel)
	}
}

// Still busy after every attempt: the run fails with the sentence the spec
// names, and still nothing is deleted.
func TestAPersistentlyBusyVSSFailsCleanly(t *testing.T) {
	inProgress := libraryError("DoSnapshotSet", vss.VSS_E_SNAPSHOT_SET_IN_PROGRESS)
	h := setup(t, inProgress, inProgress, inProgress, inProgress)
	_, err := createVolumeSnapshot(`C:\`, []WriterState{{Name: "Hyper-V Writer", State: 2, StateText: "Waiting for freeze"}})
	if err == nil || err.Error() != InProgressMessage || !errors.Is(err, ErrSnapshotInProgress) {
		t.Fatalf("got %v", err)
	}
	if h.st.calls != inProgressAttempts {
		t.Fatalf("%d attempts, want %d", h.st.calls, inProgressAttempts)
	}
	if d := h.destructive(); len(d) > 0 {
		t.Fatalf("destructive commands issued: %q", d)
	}
	if !strings.Contains(strings.Join(h.warns, "\n"), "'Hyper-V Writer' [2] Waiting for freeze") {
		t.Fatalf("the warning does not say what is active: %q", h.warns)
	}
	if len(h.errs) != 1 {
		t.Fatalf("want one ERROR for the give-up, got %q", h.errs)
	}
}

// Any other failure fails at once: no retry, no wait, no delete.
func TestAProviderErrorFailsAtOnce(t *testing.T) {
	h := setup(t, libraryError("DoSnapshotSet", vss.VSS_E_UNEXPECTED_PROVIDER_ERROR))
	_, err := createVolumeSnapshot(`C:\`, nil)
	var ve *Error
	if !errors.As(err, &ve) || ve.Info.Name != "VSS_E_UNEXPECTED_PROVIDER_ERROR" {
		t.Fatalf("got %v", err)
	}
	if h.st.calls != 1 || len(h.sleeps) != 0 || len(h.destructive()) != 0 {
		t.Fatalf("calls=%d sleeps=%v destructive=%q", h.st.calls, h.sleeps, h.destructive())
	}
	if strings.Contains(err.Error(), "0x5653") {
		t.Fatalf("hex leaked into the error: %v", err)
	}
}

// The writer outcome is read AFTER the snapshot and a failed writer is a WARN
// line, not INFO (ledger F-36), and its name comes back for the run's Warning.
func TestFailedWritersAreWarnedAndNamed(t *testing.T) {
	h := setup(t)
	runCommand = func(name string, args ...string) ([]byte, error) {
		h.commands = append(h.commands, name+" "+strings.Join(args, " "))
		return []byte("Writer name: 'System Writer'\n   State: [1] Stable\n   Last error: No error\n\n" +
			"Writer name: 'SqlServerWriter'\n   State: [8] Failed\n   Last error: Timed out\n"), nil
	}
	infos := 0
	LogFn = func(string) { infos++ }
	got := writerOutcome()
	if len(got) != 1 || got[0] != "SqlServerWriter" {
		t.Fatalf("named %q", got)
	}
	if len(h.warns) != 2 || !strings.Contains(h.warns[0], "'SqlServerWriter' failed during this snapshot: state [8] Failed, last error: Timed out") {
		t.Fatalf("warnings %q", h.warns)
	}
	if infos != 0 {
		t.Fatalf("a writer failure went to LogFn (INFO) %d time(s)", infos)
	}
}
