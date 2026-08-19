//go:build service
// +build service

package main

// ibEmit (service build): there is no webview; progress from delegated
// (control-server) browse operations goes to the debug log at coarse steps
// so a long scan is visible in ProgramData without flooding the file.

import (
	"errors"
	"fmt"
	"sync"
)

// lastIbEmitStep is package-level rather than an App field so the untagged
// build carries no unused field (golangci's `unused` analyzes without the
// service tag). One App exists per process, so package scope is equivalent.
var lastIbEmitStep = -1

func (a *App) ibEmit(pct float64, msg string) {
	ibRelay(pct, msg, 0, 0, 0, -1)
	step := int(pct) / 10
	if step != lastIbEmitStep || pct >= 100 {
		lastIbEmitStep = step
		writeDebugLog(fmt.Sprintf("[imagebrowse %3.0f%%] %s", pct, msg))
	}
}

// ibEmitTask (service build): same coarse log line; byte details included so
// a delegated extraction's throughput is visible in ProgramData.
func (a *App) ibEmitTask(pct float64, msg string, done, total int64, bps float64, etaSec int) {
	ibRelay(pct, msg, done, total, bps, etaSec)
	step := int(pct) / 10
	if step != lastIbEmitStep || pct >= 100 {
		lastIbEmitStep = step
		writeDebugLog(fmt.Sprintf("[imagebrowse %3.0f%%] %s (%s / %s, %.1f MB/s)",
			pct, msg, formatBytesGo(uint64(done)), formatBytesGo(uint64(total)), bps/1e6))
	}
}

// ---------------------------------------------------------------------------
// Relaying image-browse progress back to a console that asked for it.
//
// The service reports image progress to its debug log, which is right for a
// browse the CONTROL SERVER delegated — nobody is watching a window. A browse
// the local console delegated is different: somebody is, and the progress bar
// they are watching lives in another process.
//
// ONE SINK, NOT A REGISTRY, because the engine allows one image operation at a
// time by construction: App.ibRestoreCancel is a single slot, so a second
// restore would already be clobbering the first's cancel. If that ever becomes
// concurrent, this has to become per-operation — and the refusal below is what
// will say so, rather than two jobs silently interleaving one progress bar.

var (
	ibSinkMu sync.Mutex
	ibSink   func(pct float64, msg string, done, total int64, bps float64, etaSec int)
)

// withImageProgress runs fn with image-browse progress relayed to sink.
//
// It refuses to nest. Overwriting a live sink would send one job's progress to
// the other job's console, which reads as a bar that jumps backwards — and the
// engine's single cancel slot means two at once is already a bug elsewhere.
func withImageProgress(sink func(pct float64, msg string, done, total int64, bps float64, etaSec int), fn func() error) error {
	ibSinkMu.Lock()
	if ibSink != nil {
		ibSinkMu.Unlock()
		return errors.New("[NB-3430] another image operation is already running on this machine")
	}
	ibSink = sink
	ibSinkMu.Unlock()

	defer func() {
		ibSinkMu.Lock()
		ibSink = nil
		ibSinkMu.Unlock()
	}()
	return fn()
}

// ibRelay forwards to the sink, if a console is listening.
func ibRelay(pct float64, msg string, done, total int64, bps float64, etaSec int) {
	ibSinkMu.Lock()
	sink := ibSink
	ibSinkMu.Unlock()
	if sink != nil {
		sink(pct, msg, done, total, bps, etaSec)
	}
}
