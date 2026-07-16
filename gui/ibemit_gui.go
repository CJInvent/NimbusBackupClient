//go:build !service
// +build !service

package main

// ibEmit (GUI build): image-browse progress goes to the webview as the same
// restore:progress event the rest of the Browse tab listens for.

import (
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) ibEmit(pct float64, msg string) {
	a.ibEmitTask(pct, msg, 0, 0, 0, -1)
}

// ibEmitTask is the byte-aware variant: done/total drive an exact progress
// bar, bps and etaSec drive the "42 MB/s · ~2 min left" line. Zero totals
// mean "indeterminate" — the frontend shows an animated bar without a
// percentage (used by phases that cannot know their size up front).
func (a *App) ibEmitTask(pct float64, msg string, done, total int64, bps float64, etaSec int) {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "restore:progress", map[string]interface{}{
			"percent":     pct,
			"message":     msg,
			"done_bytes":  done,
			"total_bytes": total,
			"bps":         bps,
			"eta_seconds": etaSec,
		})
	}
}
