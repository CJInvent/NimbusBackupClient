//go:build windows
// +build windows

package snapshot

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	ole "github.com/go-ole/go-ole"
)

// shadowIDRe matches a bare VSS shadow-copy GUID (8-4-4-4-12 hex).
var shadowIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// normalizeShadowID strips optional braces and validates a VSS shadow ID,
// returning the bare GUID or "" if the name is not shadow-id-shaped.
func normalizeShadowID(name string) string {
	s := strings.TrimSpace(name)
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	if !shadowIDRe.MatchString(s) {
		return ""
	}
	return s
}

func SymlinkSnapshot(symlinkPath string, id string, deviceObjectPath string) (string, error) {

	snapshotSymLinkFolder := symlinkPath + "\\" + id + "\\"

	snapshotSymLinkFolder = filepath.Clean(snapshotSymLinkFolder)
	os.RemoveAll(snapshotSymLinkFolder)
	if err := os.MkdirAll(snapshotSymLinkFolder, 0700); err != nil {
		return "", fmt.Errorf("failed to create snapshot symlink folder for snapshot: %s, err: %s", id, err)
	}

	os.Remove(snapshotSymLinkFolder)

	LogFn(fmt.Sprintf("VSS: symlink %s -> %s", snapshotSymLinkFolder, deviceObjectPath))

	if err := os.Symlink(deviceObjectPath, snapshotSymLinkFolder); err != nil {
		return "", fmt.Errorf("failed to create symlink from: %s to: %s, error: %s", deviceObjectPath, snapshotSymLinkFolder, err)
	}

	return snapshotSymLinkFolder, nil
}

func getAppDataFolder() (string, error) {
	// Get information about the current user
	currentUser, err := user.Current()
	if err != nil {
		return "", err
	}

	// Construct the path to the application data folder
	appDataFolder := filepath.Join(currentUser.HomeDir, "AppData", "Roaming", "PBSBackupGO")

	// Create the folder if it doesn't exist
	err = os.MkdirAll(appDataFolder, os.ModePerm)
	if err != nil {
		return "", err
	}

	return appDataFolder, nil
}

// CreateVSSSnapshot shadow-copies the volumes of paths, runs backup_callback
// with the snapshots, and releases them.
//
// ONE OS THREAD, ONE COM APARTMENT, FOR THE WHOLE LIFE OF THE SNAPSHOT
// (V4-BETA-FIXES §3.2, ledger F-34). go-vss calls CoInitialize and then a long
// sequence of COM calls, and nothing pinned the goroutine to a thread: the
// scheduler could move it between CoInitialize and InitializeForBackup onto a
// thread with no apartment (0x800401f0 CO_E_NOTINITIALIZED, Windows events
// 8193/13 -- run 113). The library's own CoUninitialize also ran when
// CreateSnapshot RETURNED, before the components were used again by Release.
// So the thread is locked and an STA apartment is opened here, before the
// first snapshotter exists, and closed only after the last Release: the
// library's nested CoInitialize returns S_FALSE and its CoUninitialize
// balances it, while ours keeps the apartment alive. STA, not MTA, because the
// library initializes STA and a nested STA-on-MTA call would return
// RPC_E_CHANGED_MODE and unbalance the uninitialize. The backup callback runs
// on this locked thread; its worker goroutines do no COM.
//
// ONE SNAPSHOTTER PER VOLUME. The old loop reused one go-vss Snapshotter for
// every path, and go-vss refuses a second CreateSnapshot on it ("snapshotter
// is already in use"), so an image of a disk with two lettered volumes could
// never be taken (ledger F-54).
func CreateVSSSnapshot(paths []string, backup_callback func(sn map[string]SnapShot) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		// S_FALSE (already initialized on this thread) is success; anything
		// else means COM is unusable here and every VSS call would fail.
		if oe, ok := err.(*ole.OleError); !ok || oe.Code() != 1 {
			ErrorFn(fmt.Sprintf("VSS: COM initialization failed: %v", err))
			return fmt.Errorf("COM initialization failed: %w", err)
		}
	}
	defer ole.CoUninitialize()

	appDataFolder, err := getAppDataFolder()
	if err != nil {
		ErrorFn(fmt.Sprintf("VSS: cannot resolve the snapshot folder: %v", err))
		return err
	}

	writersBefore := listWriters("before the snapshot")
	describeWritersBefore(writersBefore)

	var created []*createdSnapshot
	defer func() {
		for i := len(created) - 1; i >= 0; i-- {
			if rerr := created[i].sn.Release(); rerr != nil {
				WarnFn(fmt.Sprintf("VSS: releasing the snapshot of %s: %s", created[i].volume, StripHex(rerr.Error())))
			}
		}
	}()

	type target struct{ path, volume, sub string }
	var targets []target
	byVolume := map[string]*createdSnapshot{}
	for _, path := range paths {
		path, _ = filepath.Abs(path)
		volName := filepath.VolumeName(path) + "\\"
		targets = append(targets, target{path: path, volume: volName, sub: path[len(volName):]})
		if _, done := byVolume[volName]; done {
			continue
		}
		LogFn(fmt.Sprintf("VSS: creating snapshot of %s (backup context, non-persistent, timeout %ds)", volName, vssTimeoutSeconds))
		tSnap := time.Now()
		cs, cerr := createVolumeSnapshot(volName, writersBefore)
		if cerr != nil {
			return cerr
		}
		created = append(created, cs)
		byVolume[volName] = cs
		LogFn(fmt.Sprintf("VSS: snapshot {%s} of %s created in %s -> %s",
			cs.snap.Id, volName, time.Since(tSnap).Round(time.Millisecond), cs.snap.DeviceObjectPath))
		if _, lerr := SymlinkSnapshot(filepath.Join(appDataFolder, "VSS"), cs.snap.Id, cs.snap.DeviceObjectPath); lerr != nil {
			ErrorFn(fmt.Sprintf("VSS: %v", lerr))
			return lerr
		}
	}

	// The writers' outcome for THIS snapshot set, now that it exists.
	failed := writerOutcome()

	snapshots := make(map[string]SnapShot, len(targets))
	for _, t := range targets {
		cs := byVolume[t.volume]
		snapshots[t.path] = SnapShot{FullPath: filepath.Join(appDataFolder, "VSS", cs.snap.Id, t.sub),
			Id: cs.snap.Id, ObjectPath: cs.snap.DeviceObjectPath, Valid: true, WriterFailures: failed}
	}
	return backup_callback(snapshots)
}

// VSSCleanup removes orphaned VSS snapshots left by a previously crashed Nimbus
// run. It deletes ONLY shadow copies Nimbus created — recorded as the subfolder
// names of the <appData>/VSS symlink directory — never `vssadmin delete shadows
// /all`, which destroyed EVERY shadow copy on the host (other backup tools, DCs,
// SQL/Exchange) on each service start (audit v2-H-05).
//
// Best-effort by design: the worst case here is that an orphan remains until a
// later run, which is far safer than wiping other applications' shadow copies.
//
// WINDOWS-VERIFY: the exact `vssadmin delete shadows /shadow={id}` form may
// require `/for=<volume>` on some Windows versions; we do not persist the source
// volume. The robust long-term path is the VSS API DeleteSnapshots(by ID). If the
// invocation is rejected, the orphan simply remains (no collateral damage).
func VSSCleanup() error {
	appData, err := getAppDataFolder()
	if err != nil {
		WarnFn(fmt.Sprintf("VSS cleanup: cannot resolve the snapshot folder: %v", err))
		return nil
	}
	vssDir := filepath.Join(appData, "VSS")
	entries, err := os.ReadDir(vssDir)
	if err != nil {
		// No VSS symlink directory ⇒ no Nimbus-created shadows to clean.
		return nil
	}

	for _, e := range entries {
		id := normalizeShadowID(e.Name())
		if id == "" {
			continue // not a shadow-id-shaped entry
		}
		marker := filepath.Join(vssDir, e.Name())

		// If the symlink no longer resolves, the shadow was already released (a
		// normal-backup leftover, not a live orphan): just drop the stale marker so
		// these don't accumulate and slow every startup.
		if _, statErr := os.Stat(marker); statErr != nil {
			_ = os.Remove(marker)
			continue
		}

		// Live symlink ⇒ the shadow still exists ⇒ a genuine orphan from a crash.
		LogFn(fmt.Sprintf("VSS cleanup: removing orphaned Nimbus shadow %s", id))
		if out, derr := runCommand("vssadmin", "delete", "shadows", "/shadow={"+id+"}", "/quiet"); derr != nil {
			// Keep the marker so a later run retries; never fall back to /all.
			WarnFn(fmt.Sprintf("VSS cleanup: could not delete Nimbus shadow %s (will retry): %v - %s", id, derr, strings.TrimSpace(string(out))))
			continue
		}
		LogFn(fmt.Sprintf("VSS cleanup: removed Nimbus shadow %s", id))
		_ = os.Remove(marker)
	}

	// Never a VSS service restart and never `delete shadows /all`: both
	// reach every VSS consumer on the host. A stuck context of OUR OWN, should
	// one ever need a service bounce, is an operator action surfaced on the
	// run report, not something this agent does (V4-BETA-FIXES §3.3).
	return nil
}
