//go:build !service
// +build !service

package main

import (
	"fmt"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailswin "github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// guiSingleInstanceID names Wails' single-instance lock. FIXED: a changed id
// would let an old and a new GUI run side by side. Wails scopes its lock to
// the user session, which is right for a tray app on a terminal server (the
// Global\ mutex it replaces would have stopped a second user's tray).
const guiSingleInstanceID = "nimbus-backup-gui-5d9f3c2e-7b1a-4e8f-a6c4-2f0b9d1e3a57"

// guiOptions is the GUI's Wails configuration, separate from main() so the
// single-instance wiring can be tested.
func guiOptions(app *App, minimized bool, webviewDataDir string) *options.App {
	return &options.App{
		Title:     fmt.Sprintf("%s v%s", appName, appVersion),
		Width:     1000,
		Height:    700,
		MaxWidth:  1400, // Prevent window from being too large
		MaxHeight: 900,  // Prevent title bar from going off-screen
		MinWidth:  400,  // Allow very small windows for low-res screens
		MinHeight: 300,  // Allow very small windows for low-res screens
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		StartHidden:      minimized, // Start hidden if --minimized flag is set
		OnStartup:        app.startup,
		OnDomReady:       app.domReady,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
		// F-44: a second launch activates this window and exits.
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               guiSingleInstanceID,
			OnSecondInstanceLaunch: app.onSecondInstanceLaunch,
		},
		Windows: &wailswin.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
			WebviewUserDataPath:  webviewDataDir,
		},
	}
}

// onSecondInstanceLaunch runs in THIS (the first) instance when the GUI is
// launched again: bring the window back, even from the tray, and to the
// front. The brief always-on-top toggle is what gets past Windows' focus
// stealing rules; FindWindow/ShowWindow from the other process could not
// restore a window Wails had hidden.
func (a *App) onSecondInstanceLaunch(_ options.SecondInstanceData) {
	writeInfoLog("GUI launched again: showing this window instead of starting a second one")
	if a.ctx == nil {
		return
	}
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
	runtime.WindowSetAlwaysOnTop(a.ctx, true)
	ctx := a.ctx
	time.AfterFunc(300*time.Millisecond, func() { runtime.WindowSetAlwaysOnTop(ctx, false) })
}
