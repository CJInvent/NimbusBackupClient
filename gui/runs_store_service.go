//go:build service
// +build service

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// The run registry's history file (gui/api/runs_store.go,
// docs/V4-CONSOLE.md §4.2): run_history.json beside config.json, written with
// the same atomic temp-file-and-rename as every other file the service owns.
//
// It replaces job_history.json. Until V4-CONSOLE C2 moves the console's
// history table onto /runs/recent, the pipeline still writes the old file for
// that table to read; C2 deletes both.

const runHistoryFileName = "run_history.json"

func runHistoryPath() (string, error) {
	dir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, runHistoryFileName), nil
}

// loadRunHistory installs the history file on the service's registry. Called
// once at service start, before the registry is shared with the scheduler and
// the API server, so nothing begins a run while the file is read.
//
// An unreadable file is moved aside, not overwritten: the registry starts
// empty and writes a new one, and the old bytes stay on disk for whoever
// wants to know what happened to them.
func loadRunHistory(reg *api.RunRegistry) {
	path, err := runHistoryPath()
	if err != nil {
		writeErrorLog(fmt.Sprintf("[Runs] no directory for the run history (history is memory-only until restart): %v", err))
		return
	}
	// #nosec G304 -- the path is the service's own data directory, not input
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		writeErrorLog(fmt.Sprintf("[Runs] could not read %s; starting a new history: %v", path, err))
		data = nil
	}

	interrupted, err := reg.Persist(data, func(b []byte) error { return atomicWriteFile(path, b, 0o600) })
	if err != nil {
		aside := path + ".unreadable"
		if mvErr := os.Rename(path, aside); mvErr != nil {
			writeErrorLog(fmt.Sprintf("[Runs] %v; it could not be moved aside (%v) and will be replaced", err, mvErr))
		} else {
			writeWarnLog(fmt.Sprintf("[Runs] %v; moved to %s, starting a new history", err, aside))
		}
		return
	}
	if interrupted > 0 {
		writeWarnLog(fmt.Sprintf("[Runs] %d run(s) were still running when the service last stopped; recorded as interrupted", interrupted))
	}
}
