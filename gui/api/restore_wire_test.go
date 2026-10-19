package api

// THE CONSTANTS AND THE TABLE MUST AGREE.
//
// Until V4-CONSOLE C3 an op name was written down in three places: the
// console's constants (gui/restore_wire.go), this package's gate table, and
// the service's dispatch. The constants now live here and the table is built
// from them, so a misspelling is a compile error. What a compiler cannot see
// is a constant the table never declares: the console can ask for it and the
// gate refuses it as unknown, so the button that calls it is dead. And a
// table entry with no constant is a permission no caller can name.
//
// The service's dispatch is pinned against the same constants in
// gui/restore_ops_test.go.

import "testing"

// allRestoreOpConstants is written out rather than derived: deriving it from
// the table would make the test agree with itself.
var allRestoreOpConstants = []string{
	RestoreOpSnapshots, RestoreOpContents, RestoreOpMeta, RestoreOpRestore,
	RestoreOpDownload, RestoreOpSearch, RestoreOpCancelSearch, RestoreOpSpace,
	RestoreOpVolumePartitions, RestoreOpVolumeFiles, RestoreOpVolumeDirectory,
	RestoreOpVolumeDownload, RestoreOpVolumeFileRestore, RestoreOpCancelVolumeFileRestore,
}

func TestEveryRestoreOpConstantIsDeclaredInTheGate(t *testing.T) {
	declared := map[string]bool{}
	for _, op := range RestoreOps() {
		declared[op.Name] = true
	}
	if len(declared) == 0 {
		t.Fatal("the gate declares no ops at all; nothing below would mean anything")
	}
	for _, name := range allRestoreOpConstants {
		if !declared[name] {
			t.Errorf("%q is a constant the console can ask for, but the gate does not declare it: "+
				"it would be refused as unknown", name)
		}
	}
}

func TestEveryDeclaredOpHasAConstant(t *testing.T) {
	named := map[string]bool{}
	for _, name := range allRestoreOpConstants {
		if named[name] {
			t.Errorf("two constants carry %q", name)
		}
		named[name] = true
	}
	for _, op := range RestoreOps() {
		if !named[op.Name] {
			t.Errorf("the gate declares %q but no constant names it: either its caller was removed "+
				"and the permission outlived it, or the constant is missing", op.Name)
		}
	}
}
