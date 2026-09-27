package snapshot

import "fmt"

type SnapShot struct {
	FullPath   string
	Id         string
	ObjectPath string
	Valid      bool
	// WriterFailures names the VSS writers that failed during THIS snapshot
	// set, read after it was created (V4-BETA-FIXES §4). The volume data is
	// captured; application consistency is not guaranteed, and the run
	// reports Warning rather than Success. The same list on every entry.
	WriterFailures []string
}

// LogFn receives every VSS diagnostic line. The old code fmt.Println'd them
// — which in the Windows SERVICE goes nowhere, so snapshot successes,
// failures, writer warnings and shadow IDs were all computed and then
// discarded. The host wires this to its debug log at startup; the default
// falls back to stdout so CLI use still prints.
//
// It lives in the platform-neutral file (not win_snapshot.go) so that
// non-Windows builds referencing snapshot.LogFn — e.g. the gui package's
// pbslog glue — still compile. On those builds it simply is never called.
var LogFn = func(msg string) { fmt.Println(msg) }

// WarnFn and ErrorFn are LogFn's leveled siblings (V4-BETA-FIXES §4, ledger
// F-36). LogFn was the only hook and the host wired it to INFO, so writer
// failures and snapshot errors never reached the WARN/ERROR queue the server
// receives. They default to LogFn, so a host that wires only LogFn still sees
// every line; the service wires them to its WARN and ERROR writers.
var (
	WarnFn  = func(msg string) { LogFn(msg) }
	ErrorFn = func(msg string) { LogFn(msg) }
)
