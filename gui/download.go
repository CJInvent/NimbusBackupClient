//go:build !service
// +build !service

package main

// download.go — what is LEFT of "Download" in the console.
//
// The extraction itself moved to the service (download_service.go), so what
// remains here is the two things that are genuinely the console's: the space
// PRE-FLIGHT behind the warning dialog, and the retired native dialog stub.
//
// The pre-flight is advisory and always was. It exists to warn before the user
// commits to a two-hour download that will not fit, and it is now doubly
// advisory because it runs as the user while the bytes are written by the
// service as SYSTEM. The check that BLOCKS is the one in download_service.go.
//
// Space safety (the rules, exactly):
//   - BLOCK when the download would not fit: needed > free (on either the
//     staging temp drive or the destination drive) — enforced in the service.
//   - WARN when it fits but would push the destination drive to >= 90% used:
//     used_after = total - free + needed;  warn if used_after >= 0.90 * total.

import "errors"

// CheckDownloadSpace is Wails-bound: frontend pre-flight for the warning /
// block UX before starting a download of neededBytes to destPath's drive.
func (a *App) CheckDownloadSpace(destPath string, neededBytes int64) (SpaceCheck, error) {
	if destPath == "" {
		return SpaceCheck{}, errors.New("destination path required")
	}
	if neededBytes < 0 {
		neededBytes = 0
	}
	return evaluateSpace(destPath, uint64(neededBytes))
}

// OpenSaveFileDialog is Wails-bound: native "save as" picker. Same headless-
// service guard as OpenRestoreDestDialog (native pickers fault in session 0).
// OpenSaveFileDialog is RETIRED. The Wails native Save dialog takes a native
// COM fault in this app — the process dies outright (tray icon and all) with
// no Go panic to log and nothing recover() can catch. A backup tool cannot
// ship a button that kills the process, so the picker is now rendered in the
// webview over ListDrives/ListFolders/CreateFolder (see pathpicker.go), which
// is pure Go and cannot fault. This stub stays so any stale caller gets a
// clear error instead of resurrecting the crash.
func (a *App) OpenSaveFileDialog(_ string) (string, error) {
	return "", errors.New("[NB-3009] the native save dialog is disabled — use the in-app picker")
}
