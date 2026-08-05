//go:build service
// +build service

package main

// emitBackupEvent is a no-op in the service process: there is no Wails
// runtime, and no window to deliver to. Observers of a service-run backup read
// the run registry over the local API instead (gui/api/runs.go), which is the
// path that works for every trigger rather than only for runs started in this
// process.
func (a *App) emitBackupEvent(name string, payload map[string]interface{}) {}
