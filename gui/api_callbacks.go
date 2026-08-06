//go:build service
// +build service

package main

import (
	"fmt"
	"sync"
)

// The engine -> local API progress relay.
//
// Service-only. The API server exists only in the service process, and only
// the service runs backups; the GUI is a CLIENT of that API, not an
// implementer of it (docs/V4-PIPELINE.md §3.1). Leaving this in the shared
// file left the GUI build registering callbacks that nothing in that build
// could ever fire.

// Callback state used to live on App, in the shared struct. It moved here
// when the relay became service-only: a struct field that no code in the GUI
// build can reach is dead weight in every GUI process, and the `unused`
// linter says so.
var (
	callbacksMutex sync.RWMutex
	callbacksMap   = make(map[string]*progressCallbacks)
)

// progressCallbacks stores the callback functions for a backup operation
type progressCallbacks struct {
	onProgress func(jobID string, percent float64, message string)
	onStats    func(jobID string, bytesDone, bytesTotal, newChunks, reusedChunks uint64)
	onComplete func(jobID string, success bool, message string)
}

// SetProgressCallbacks registers per-job progress/stats/completion callbacks for
// API mode. Shared file: the SERVICE build needs this too — it used to live in
// the !service main.go only, so the service never implemented the interface the
// API server asserts, the progress map never updated during a run, and the GUI
// showed a frozen "Starting backup..." for the entire backup.
func (a *App) SetProgressCallbacks(jobID string, onProgress func(string, float64, string), onStats func(string, uint64, uint64, uint64, uint64), onComplete func(string, bool, string)) {
	writeDebugLog(fmt.Sprintf("[SetProgressCallbacks] Registered callbacks for jobID: %s", jobID))
	callbacksMutex.Lock()
	callbacksMap[jobID] = &progressCallbacks{
		onProgress: onProgress,
		onStats:    onStats,
		onComplete: onComplete,
	}
	callbacksMutex.Unlock()
}

// notifyProgressCallbacks fans a progress update (percent 0-100) out to all
// registered per-job callbacks. Returns whether any callback was registered.
func (a *App) notifyProgressCallbacks(percent float64, message string) bool {
	callbacksMutex.RLock()
	defer callbacksMutex.RUnlock()
	for jobID, callbacks := range callbacksMap {
		if callbacks.onProgress != nil {
			callbacks.onProgress(jobID, percent, message)
		}
	}
	return len(callbacksMap) > 0
}

// notifyStatsCallbacks fans structured stats out to registered callbacks.
func (a *App) notifyStatsCallbacks(bytesDone, bytesTotal, newChunks, reusedChunks uint64) {
	callbacksMutex.RLock()
	defer callbacksMutex.RUnlock()
	for jobID, callbacks := range callbacksMap {
		if callbacks.onStats != nil {
			callbacks.onStats(jobID, bytesDone, bytesTotal, newChunks, reusedChunks)
		}
	}
}

// notifyCompleteCallbacks fans completion out and clears the registry.
func (a *App) notifyCompleteCallbacks(success bool, message string) bool {
	callbacksMutex.Lock()
	defer callbacksMutex.Unlock()
	had := len(callbacksMap) > 0
	for jobID, callbacks := range callbacksMap {
		if callbacks.onComplete != nil {
			callbacks.onComplete(jobID, success, message)
		}
		delete(callbacksMap, jobID)
	}
	return had
}
