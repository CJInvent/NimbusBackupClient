package main

// fileutil_core.go — the free-space evaluation, used by BOTH processes.
//
// The console pre-flights it for its warning dialog; the service enforces it
// before writing a byte (download_service.go). Both need the same arithmetic,
// so it lives untagged.
//
// The PACKAGING helpers that used to sit here — zip a directory, find the one
// staged file, copy it out — moved to fileutil_service.go with the restore
// engine. Nothing in the console packages anything any more.

// SpaceCheck is the result of a free-space evaluation for a pending write of
// neededBytes onto the drive holding path.
type SpaceCheck struct {
	Path          string  `json:"path"`
	FreeBytes     uint64  `json:"free_bytes"`
	TotalBytes    uint64  `json:"total_bytes"`
	NeededBytes   uint64  `json:"needed_bytes"`
	Fits          bool    `json:"fits"`    // needed <= free
	Warn90        bool    `json:"warn_90"` // fits, but usage after >= 90%
	UsageAfterPct float64 `json:"usage_after_pct"`
}

func evaluateSpace(path string, needed uint64) (SpaceCheck, error) {
	free, total, err := driveSpace(path)
	if err != nil {
		return SpaceCheck{}, err
	}
	sc := SpaceCheck{Path: path, FreeBytes: free, TotalBytes: total, NeededBytes: needed}
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
