//go:build !service
// +build !service

package main

import "github.com/wailsapp/wails/v2/pkg/runtime"

// emitBackupEvent pushes a live backup event to the Wails window.
//
// This is the ONLY thing about the backup pipeline that is genuinely
// build-specific: whether this process has a window to emit into. Isolating it
// here is what let the two copies of the pipeline become one
// (docs/V4-PIPELINE.md §3.1).
//
// The isServiceProcess guard stays even in this build. The GUI binary can be
// launched as the service in a legacy configuration, and emitting into a nil
// or foreign Wails context there panics.
func (a *App) emitBackupEvent(name string, payload map[string]interface{}) {
	if a.isServiceProcess || a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, name, payload)
}
