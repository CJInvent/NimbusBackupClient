//go:build service
// +build service

package main

// ibEmit (service build): there is no webview; progress from delegated
// (control-server) browse operations goes to the debug log at coarse steps
// so a long scan is visible in ProgramData without flooding the file.

import "fmt"

// lastIbEmitStep is package-level rather than an App field so the untagged
// build carries no unused field (golangci's `unused` analyzes without the
// service tag). One App exists per process, so package scope is equivalent.
var lastIbEmitStep = -1

func (a *App) ibEmit(pct float64, msg string) {
	step := int(pct) / 10
	if step != lastIbEmitStep || pct >= 100 {
		lastIbEmitStep = step
		writeDebugLog(fmt.Sprintf("[imagebrowse %3.0f%%] %s", pct, msg))
	}
}

// ibEmitTask (service build): same coarse log line; byte details included so
// a delegated extraction's throughput is visible in ProgramData.
func (a *App) ibEmitTask(pct float64, msg string, done, total int64, bps float64, _ int) {
	step := int(pct) / 10
	if step != lastIbEmitStep || pct >= 100 {
		lastIbEmitStep = step
		writeDebugLog(fmt.Sprintf("[imagebrowse %3.0f%%] %s (%s / %s, %.1f MB/s)",
			pct, msg, formatBytesGo(uint64(done)), formatBytesGo(uint64(total)), bps/1e6))
	}
}
