package snapshot

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	vss "github.com/st-matskevich/go-vss"
)

// Creating the shadow copies, portable half (V4-BETA-FIXES §3.2, §3.3, §4).
//
// Everything that decides something lives here, untagged, behind small seams,
// so the Linux gate runs it: the retry on another program's shadow copy, the
// refusal to delete anything this process did not create, and the writer
// outcome read AFTER the snapshot exists. win_snapshot.go owns only what is
// Windows by nature: the COM apartment, the symlinks, the cleanup.

// snapshotter is the part of go-vss this package drives.
type snapshotter interface {
	CreateSnapshot(drive string, bootable bool, timeout int) (*vss.Snapshot, error)
	Release() error
}

// Seams, swapped by tests. Production values only below.
var (
	newSnapshotter = func() snapshotter { return &vss.Snapshotter{} }
	runCommand     = func(name string, args ...string) ([]byte, error) {
		// #nosec G204 -- callers pass fixed program names and literal arguments
		return exec.Command(name, args...).CombinedOutput()
	}
	sleepFn = time.Sleep
)

const (
	inProgressAttempts = 3
	inProgressWait     = 60 * time.Second
	vssTimeoutSeconds  = 180
)

// InProgressMessage is how a run ends when another program's shadow copy
// creation outlasts our wait (V4-BETA-FIXES §3.3).
const InProgressMessage = "Another program is creating a shadow copy; the backup will run at its next scheduled time."

// ErrSnapshotInProgress marks that outcome for callers that branch on it.
var ErrSnapshotInProgress = errors.New(InProgressMessage)

// createdSnapshot is one volume's shadow copy and the snapshotter holding it.
type createdSnapshot struct {
	volume string
	sn     snapshotter
	snap   *vss.Snapshot
}

// createVolumeSnapshot creates one volume's shadow copy.
//
// ANOTHER PROGRAM'S SHADOW COPY IS WAITED OUT, NEVER DESTROYED (ledger F-42).
// VSS_E_SNAPSHOT_SET_IN_PROGRESS means someone else -- another backup product,
// Windows Server Backup, a hypervisor quiesce, System Restore -- is creating a
// shadow copy right now. The code this replaces answered it with `vssadmin
// delete shadows /all` and a VSS service restart: it deleted every shadow copy
// on the machine and killed the other program's snapshot. Now: up to three
// attempts a minute apart, one WARN saying what is going on, and a clean
// failure if it is still busy. Any other failure fails at once.
//
// A FAILED ATTEMPT'S SNAPSHOTTER IS DISCARDED, NOT RELEASED. go-vss has
// already aborted and released its backup components on the failure path,
// but leaves its pointer set; calling Release on it would call BackupComplete
// on a released COM object (found while building this, ledger F-53).
func createVolumeSnapshot(volume string, writersBefore []WriterState) (*createdSnapshot, error) {
	var lastErr error
	for attempt := 1; attempt <= inProgressAttempts; attempt++ {
		sn := newSnapshotter()
		snap, err := sn.CreateSnapshot(volume, false, vssTimeoutSeconds)
		if err == nil {
			if snap == nil || snap.Id == "" {
				ErrorFn(fmt.Sprintf("VSS: no snapshot object returned for %s", volume))
				_ = sn.Release()
				return nil, &Error{Volume: volume, message: "VSS returned no snapshot for " + volume}
			}
			return &createdSnapshot{volume: volume, sn: sn, snap: snap}, nil
		}
		info := DescribeVSSError(err)
		if info.Code != uint32(vss.VSS_E_SNAPSHOT_SET_IN_PROGRESS) {
			ErrorFn(fmt.Sprintf("VSS: snapshot of %s failed: %s", volume, info.Summary()))
			return nil, newError(volume, err)
		}
		lastErr = err
		if attempt == 1 {
			busy := busyWriters(writersBefore)
			active := "no writer reported busy before the attempt"
			if len(busy) > 0 {
				active = "busy writers: " + strings.Join(busy, ", ")
			}
			WarnFn(fmt.Sprintf("VSS: another program is creating a shadow copy (%s); waiting up to %d x %s before giving up (%s). Nothing is deleted.",
				info.Summary(), inProgressAttempts-1, inProgressWait, active))
		}
		if attempt < inProgressAttempts {
			sleepFn(inProgressWait)
		}
	}
	ErrorFn(fmt.Sprintf("VSS: %s (%s)", InProgressMessage, DescribeVSSError(lastErr).Summary()))
	return nil, &Error{Volume: volume, Info: DescribeVSSError(lastErr), message: InProgressMessage,
		cause: fmt.Errorf("%w: %v", ErrSnapshotInProgress, StripHex(lastErr.Error()))}
}

// listWriters runs `vssadmin list writers`; a failure to run it is a WARN and
// an empty list, never a failed backup.
func listWriters(when string) []WriterState {
	out, err := runCommand("vssadmin", "list", "writers")
	if err != nil {
		WarnFn(fmt.Sprintf("VSS: could not read writer status %s: %v", when, err))
		return nil
	}
	return parseWriters(string(out))
}

// writerOutcome reads the writers AFTER the snapshot exists -- the state of
// THIS session. The listing taken before it reports each writer's previous
// session (another program's backup, or ours last night), which blamed
// tonight for last night's failures and missed tonight's own. Failed writers
// are WARNed one line each, with the last error as vssadmin gave it, and their
// names are returned for the run's Warning outcome.
func writerOutcome() []string {
	failed := failedWriters(listWriters("after the snapshot"))
	for _, w := range failed {
		WarnFn(fmt.Sprintf("VSS writer '%s' failed during this snapshot: state [%d] %s, last error: %s",
			w.Name, w.State, w.StateText, w.LastError))
	}
	if len(failed) > 0 {
		WarnFn(fmt.Sprintf("VSS: %d writer(s) failed; the volume data is captured but application consistency is not guaranteed",
			len(failed)))
	}
	return WriterNames(failed)
}

// describeWritersBefore is the pre-snapshot listing, as INFO context only.
func describeWritersBefore(ws []WriterState) {
	bad := 0
	for _, w := range ws {
		if w.Failed() {
			bad++
		}
	}
	if len(ws) > 0 {
		LogFn(fmt.Sprintf("VSS: %d writer(s) before the snapshot, %d still showing a failure from an earlier session (context only)",
			len(ws), bad))
	}
}
