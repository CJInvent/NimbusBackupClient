package main

// THE TWO LISTS MUST AGREE.
//
// The restore seam has an op name written down in three places, and each is
// load-bearing in a different way:
//
//	gui/restore_wire.go     the console's constants — what it will ASK for
//	gui/api/restore.go      the gate's table — what the service will ALLOW
//	gui/restore_service.go  the dispatch — what the engine can DO
//
// Drift between them fails at runtime on a customer's machine, and fails
// quietly in the worst direction: a console asking for an op the table does
// not declare gets a 400 that reads like a broken restore, and a table
// declaring an op nothing implements is a permission granted over nothing.
//
// So this file pins all three against each other. It is untagged on purpose —
// the console half and the service half are compiled apart, and the whole
// point is that they still describe the same protocol.

import (
	"strings"
	"testing"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// consoleOps is every op the console can name, taken from the constants
// themselves rather than retyped — a copy would be one more thing to drift.
var consoleOps = []string{
	opSnapshots, opContents, opMeta, opRestore, opDownload, opSearch, opCancelSearch,
	opVolumePartitions, opVolumeFiles, opVolumeDirectory, opVolumeDownload, opVolumeFileRestore, opCancelVolumeFileRestore,
}

func TestEveryOpTheConsoleCanAskForIsDeclaredInTheGate(t *testing.T) {
	declared := map[string]bool{}
	for _, op := range api.RestoreOps() {
		declared[op.Name] = true
	}
	for _, name := range consoleOps {
		if !declared[name] {
			t.Errorf("the console can ask for %q but gui/api/restore.go does not declare it — "+
				"the gate will refuse it as unknown, and the button that calls it is dead", name)
		}
	}
}

func TestEveryDeclaredOpIsOneTheConsoleCanAskFor(t *testing.T) {
	asked := map[string]bool{}
	for _, name := range consoleOps {
		asked[name] = true
	}
	for _, op := range api.RestoreOps() {
		if !asked[op.Name] {
			t.Errorf("gui/api/restore.go declares %q but nothing in restore_wire.go names it — "+
				"either a caller was removed and the permission outlived it, or the constant is missing", op.Name)
		}
	}
}

// EVERY DECLARED OP MUST BE IMPLEMENTED. A permission the engine cannot honour
// is worse than no permission: the gate says yes and the service then answers
// "declared but not implemented", which reads to an operator like a bug in
// their datastore rather than a gap in ours.
//
// Checked against the source because the dispatch lives in a `service`-tagged
// file this test cannot call into. That is the same honest limitation
// gui/backupkey_wiring_test.go carries, and it is recorded here for the same
// reason: a source pin catches deletion and rename, not behaviour.
func TestEveryDeclaredOpAppearsInTheServiceDispatch(t *testing.T) {
	src := sourceOf(t, "restore_service.go")
	for _, op := range api.RestoreOps() {
		// The dispatch switches on the CONSTANT, so look for the constant's
		// name rather than its value — that is what the code actually says.
		if !strings.Contains(src, constNameFor(op.Name)) {
			t.Errorf("restore_service.go has no case for %q (%s), so the gate permits an op "+
				"the service will refuse as unimplemented", op.Name, constNameFor(op.Name))
		}
	}
}

// constNameFor maps a wire name back to the constant that carries it. Written
// out rather than derived, so a renamed constant fails this test loudly
// instead of being silently reconstructed.
func constNameFor(op string) string {
	switch op {
	case opSnapshots:
		return "opSnapshots"
	case opContents:
		return "opContents"
	case opMeta:
		return "opMeta"
	case opRestore:
		return "opRestore"
	case opDownload:
		return "opDownload"
	case opSearch:
		return "opSearch"
	case opCancelSearch:
		return "opCancelSearch"
	case opVolumePartitions:
		return "opVolumePartitions"
	case opVolumeFiles:
		return "opVolumeFiles"
	case opVolumeDirectory:
		return "opVolumeDirectory"
	case opVolumeDownload:
		return "opVolumeDownload"
	case opVolumeFileRestore:
		return "opVolumeFileRestore"
	case opCancelVolumeFileRestore:
		return "opCancelVolumeFileRestore"
	}
	return "<no constant for " + op + ">"
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
