package main

// logredirect.go — routes Go's standard `log` package into the SAME rotating
// log the rest of the client writes to (writeDebugLog).
//
// WHY THIS EXISTS
//
// A Windows service has no console. Anything written to os.Stderr by a process
// started by the SCM is discarded by the operating system — there is nowhere
// for it to go. Go's stdlib `log` package defaults to os.Stderr, and until
// this file existed log.SetOutput was never called ANYWHERE in the repo.
//
// The practical consequence was a silent agent. controlplane/loop.go reports
// every check-in failure with log.Printf, so an agent that enrolled correctly
// and then failed authentication on every subsequent check-in produced exactly
// one visible line ("enrolled as agent N") and then nothing at all, forever,
// while the portal showed it offline. The same hole swallowed
// controlplane's command-post and handler-panic reports, runreporter's
// delivery failures, and — worst — log.Fatal in service_main.go, so a service
// that failed to START logged nothing whatsoever.
//
// This is deliberately a CATCH-ALL rather than a logger injected into the
// controlplane package: it captures stdlib log output from every package in
// the process, including third-party dependencies we do not control, so no
// future import can reintroduce a silent failure path. Nothing in this
// process should ever write a diagnostic that lands nowhere.
//
// Safe against recursion: writeDebugLog -> writeLogToLogger uses the rotating
// logger and fmt.Fprint(os.Stderr, ...) directly, and never calls back into
// the stdlib log package.

import (
	"bytes"
	"io"
	"log"
)

// debugLogWriter adapts writeDebugLog to io.Writer so it can back log.SetOutput.
type debugLogWriter struct{}

// Write forwards each line to the visible rotating log. The stdlib log package
// emits exactly one Write per log call with a trailing newline, but multi-line
// messages (a formatted error, a panic dump) arrive as one Write containing
// several lines — splitting keeps each on its own prefixed, timestamped row
// instead of producing one ragged entry.
func (debugLogWriter) Write(p []byte) (int, error) {
	for _, line := range bytes.Split(bytes.TrimRight(p, "\n"), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		writeDebugLog(string(line))
	}
	// Report the full input as consumed: a short count is an io.Writer
	// contract violation and makes the stdlib log package report an error
	// about the logger itself, which is precisely the failure this file
	// exists to prevent.
	return len(p), nil
}

// redirectStdlibLog points the standard log package at the visible log. Call it
// as the FIRST statement of main(), before any work that might log — output
// written before this runs goes to the discarded stderr.
//
// Flags are cleared because writeLogToLogger already stamps every line with
// its own "[SERVICE] [YYYY-MM-DD HH:MM:SS]" prefix; leaving the stdlib date
// and time flags on would print the timestamp twice.
func redirectStdlibLog() {
	log.SetFlags(0)
	log.SetOutput(debugLogWriter{})
}

// Compile-time assertion that the adapter really satisfies io.Writer.
var _ io.Writer = debugLogWriter{}
