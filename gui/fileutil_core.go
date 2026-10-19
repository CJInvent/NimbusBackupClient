package main

// fileutil_core.go — the free-space evaluation behind restore op "space" and
// the download block (download_service.go). The service is the process that
// writes the bytes, so it is the one that measures the drive; the console asks
// it (V4-CONSOLE C1) and no longer carries a copy.
//
// The PACKAGING helpers that used to sit here — zip a directory, find the one
// staged file, copy it out — moved to fileutil_service.go with the restore
// engine.

import "github.com/tizbac/proxmoxbackupclient_go/gui/api"

func evaluateSpace(path string, needed uint64) (api.SpaceCheck, error) {
	free, total, err := driveSpace(path)
	if err != nil {
		return api.SpaceCheck{}, err
	}
	sc := api.SpaceCheck{Path: path, FreeBytes: free, TotalBytes: total, NeededBytes: needed}
	sc.Fits = needed <= free
	if total > 0 {
		// used_after = total - free + needed. Guard the arithmetic: all
		// uint64, and needed > free is already the !Fits case (no subtraction
		// underflow possible in the used computation: total >= free always).
		usedAfter := (total - free) + needed
		sc.UsageAfterPct = float64(usedAfter) / float64(total) * 100.0
		sc.Warn90 = sc.Fits && float64(usedAfter) >= 0.90*float64(total)
	}
	return sc, nil
}
