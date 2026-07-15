//go:build service
// +build service

package main

// ibEmit (service build): there is no webview; progress from delegated
// (control-server) browse operations goes to the debug log at coarse steps
// so a long scan is visible in ProgramData without flooding the file.

import "fmt"

func (a *App) ibEmit(pct float64, msg string) {
	step := int(pct) / 10
	if step != a.lastIbEmitStep || pct >= 100 {
		a.lastIbEmitStep = step
		writeDebugLog(fmt.Sprintf("[imagebrowse %3.0f%%] %s", pct, msg))
	}
}
