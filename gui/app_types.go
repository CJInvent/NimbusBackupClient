package main

import (
	"controlplane"
	"sync"
)

// App is the service's state. It is no longer bound to the webview: the
// console binds Console (console.go), so none of App's methods is callable
// from a user's session. isServiceProcess goes in V4-CONSOLE C3, with the
// service build tag.
type App struct {
	storageMu        sync.Mutex
	storageOpMu      sync.Mutex
	storageState     *controlplane.StorageState
	storageFailure   string
	config           *Config
	isServiceProcess bool // True if running as Windows Service (never re-detect mode)
}
