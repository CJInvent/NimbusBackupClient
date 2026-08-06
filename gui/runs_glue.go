//go:build service
// +build service

package main

import (
	"sync"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// Run-registry glue: the seam that makes a scheduled backup visible.
//
// The reported bug (docs/V4-PIPELINE.md §2) had four layers. Three were in the
// API package — no endpoint, no tracking, no live poll. This file is the
// fourth: a backup begun by the SCHEDULER never touched the API server's
// progress store at all, because the scheduler calls App.StartBackup directly
// and the engine runs in-process, never passing through the HTTP handler that
// was the store's only writer. Fixing the other three without this one would
// have produced a correct endpoint reporting nothing.
//
// Deliberately shaped as a mirror of registerRunReporter / takeRunReporter /
// attachControlPlaneHooks in controlplane_glue.go. That pattern already solves
// exactly this problem for the control plane — the site that DECIDES to run is
// not the site that constructs BackupOptions — and it is proven in production.
// A second, differently-shaped mechanism for the same handoff would be a thing
// to keep in step forever.
//
// This file carries no build tag, so the service build and the GUI build get
// the same glue. Only the service ever holds a registry (SetRunRegistry is
// called from service.go); in the GUI build runRegistry stays nil and every
// function here is a no-op, which is the correct behaviour for a front end
// that is on its way to having no engine at all.

var (
	runRegistryMu sync.RWMutex
	runRegistry   *api.RunRegistry
)

// SetRunRegistry installs the registry owned by the local API server. Called
// once, from service startup, so the scheduler and the API report into one
// store rather than two.
func SetRunRegistry(r *api.RunRegistry) {
	runRegistryMu.Lock()
	runRegistry = r
	runRegistryMu.Unlock()
}

func currentRunRegistry() *api.RunRegistry {
	runRegistryMu.RLock()
	defer runRegistryMu.RUnlock()
	return runRegistry
}

// normalizeRunKind maps the engine's backup-type vocabulary to the two words
// the status panel shows. "vm" is the engine's name for a whole-machine image;
// showing that word to a technician looking at a workstation would be wrong.
func normalizeRunKind(backupType string) string {
	if backupType == "vm" {
		return "machine"
	}
	return "directory"
}
