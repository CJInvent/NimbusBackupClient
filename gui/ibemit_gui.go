//go:build !service
// +build !service

package main

// ibEmit (GUI build): image-browse progress goes to the webview as the same
// restore:progress event the rest of the Browse tab listens for.

import (
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) ibEmit(pct float64, msg string) {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "restore:progress", map[string]interface{}{
			"percent": pct, "message": msg,
		})
	}
}
