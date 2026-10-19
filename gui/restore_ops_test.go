package main

// EVERY DECLARED OP MUST BE IMPLEMENTED.
//
// The op names have one definition (gui/api/restore_wire.go), the gate's
// table is built from it, and gui/api/restore_wire_test.go pins the two
// against each other. What is left is the third list: the service's dispatch
// (restore_service.go). A permission the engine cannot honour is worse than no
// permission: the gate says yes and the service then answers "declared but
// not implemented", which reads to an operator like a bug in their datastore
// rather than a gap in ours.
//
// Until V4-CONSOLE C3 this was a source pin, because the dispatch lived in a
// `service`-tagged file the default test build could not call into. The tag
// is gone, so this calls the dispatch. Every op is sent with no parameters,
// which every implemented case refuses while decoding, before it reaches PBS
// or the disk; only an op with no case reaches the "not implemented" answer.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

func TestEveryDeclaredOpReachesACaseInTheServiceDispatch(t *testing.T) {
	a := &App{}
	ops := api.RestoreOps()
	if len(ops) == 0 {
		t.Fatal("the gate declares no ops; this test would pin nothing")
	}
	for _, op := range ops {
		var err error
		switch op.Kind {
		case api.OpQuery:
			_, err = a.RestoreQuery(op.Name, nil)
		case api.OpJob:
			_, err = a.RestoreJob(context.Background(), op.Name, nil, nil)
		case api.OpControl:
			err = a.RestoreControl(op.Name, nil)
		default:
			t.Errorf("op %q has a kind this test does not know (%d)", op.Name, op.Kind)
			continue
		}
		if err != nil && strings.Contains(err.Error(), "declared but not implemented") {
			t.Errorf("restore_service.go has no case for %q, so the gate permits an op the "+
				"service refuses as unimplemented: %v", op.Name, err)
		}
	}
}

// Every op declares a permission. An op with an empty Right would be gated on
// nothing — the gate would call restorePermitted("") and get whatever the
// predicate says about a right it has never heard of.
func TestEveryDeclaredOpNamesAPermission(t *testing.T) {
	for _, op := range api.RestoreOps() {
		if op.Right == "" {
			t.Errorf("op %q declares no permission, so nothing decides whether it is allowed", op.Name)
		}
		if op.Doc == "" {
			t.Errorf("op %q has no description; the table is meant to read as the enumeration of "+
				"everything the console may ask the service to do", op.Name)
		}
	}
}

// THE SERVICE REFUSES A DESTINATION THE CONSOLE USED TO REFUSE ALONE.
//
// Until V4-CONSOLE C3 the console ran security.ValidatePath on a restore's
// destination and the service did not, so any other caller of the local API
// skipped it (finding F1). Every op that writes to a caller-named path is
// refused here, before a PBS server is resolved, with NB-3013.
func TestEveryWritingOpRefusesATraversingDestination(t *testing.T) {
	// No PBS server is configured: without the check each op stops on that
	// instead, so a missing check fails below rather than reaching PBS.
	a := &App{config: &Config{}}
	const bad = `C:\Users\Public\..\..\Windows\System32`
	cases := map[string]any{
		api.RestoreOpRestore: api.RestoreParams{
			SnapshotRef: api.SnapshotRef{PBSID: "pbs1", BackupID: "WS-01", SnapshotID: "2026-10-19T00:00:00Z"},
			DestPath:    bad, Mode: string(api.RestoreModeAlternateAbs), IncludePaths: []string{"a"},
		},
		api.RestoreOpDownload: api.DownloadParams{
			SnapshotRef: api.SnapshotRef{PBSID: "pbs1", BackupID: "WS-01", SnapshotID: "2026-10-19T00:00:00Z"},
			DestPath:    bad, IncludePaths: []string{"a"}, AsZip: true,
		},
		api.RestoreOpVolumeDownload: api.VolumeDownloadParams{
			VolumeRef: api.VolumeRef{PBSID: "pbs1", BackupID: "WS-01"}, DestPath: bad, IncludePaths: []string{"a"},
		},
		api.RestoreOpVolumeFileRestore: api.VolumeFileRestoreParams{
			VolumeRef: api.VolumeRef{PBSID: "pbs1", BackupID: "WS-01"}, DestDir: bad, IncludePaths: []string{"a"},
		},
	}
	for op, params := range cases {
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.RestoreJob(context.Background(), op, raw, func(float64, string, int64, int64, float64, int) {})
		if err == nil || !strings.HasPrefix(err.Error(), errDestPathInvalid) {
			t.Errorf("%s to %q = %v, want %s", op, bad, err, errDestPathInvalid)
		}
	}
	// In-place restore takes its target from the snapshot's metadata, so a
	// destination it ignores is not refused.
	raw, _ := json.Marshal(api.RestoreParams{
		SnapshotRef: api.SnapshotRef{PBSID: "pbs1", BackupID: "WS-01", SnapshotID: "2026-10-19T00:00:00Z"},
		DestPath:    bad, Mode: string(api.RestoreModeOriginal),
	})
	if _, err := a.RestoreJob(context.Background(), api.RestoreOpRestore, raw, nil); err != nil &&
		strings.HasPrefix(err.Error(), errDestPathInvalid) {
		t.Errorf("an in-place restore was refused on the destination it ignores: %v", err)
	}
}
