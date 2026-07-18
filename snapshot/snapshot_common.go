package snapshot

import "fmt"

type SnapShot struct {
	FullPath   string
	Id         string
	ObjectPath string
	Valid      bool
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
