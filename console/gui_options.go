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

// webView2Missing is what the console says when the WebView2 runtime is
// missing or too old. It is built with Wails' `wv2runtime.error` and installs
// nothing itself: the service installs the runtime machine-wide when it
// starts (V4-CONSOLE D2), so the message points there.
const webView2Missing = "Nimbus Backup needs the Microsoft Edge WebView2 runtime, which is not installed yet. " +
	"The Nimbus Backup service installs it when it starts; try again in a few minutes. " +
	"If this message persists, the service log in ProgramData\\NimbusBackup says why the install failed."

// guiMessages is Wails' messages with the console's own for a missing runtime.
func guiMessages() *wailswin.Messages {
	m := wailswin.DefaultMessages()
	m.ContactAdmin = webView2Missing
	m.MissingRequirements = appName
	return m
}

// guiOptions is the GUI's Wails configuration, separate from main() so the
// single-instance wiring and the bound type can be tested.
func guiOptions(console *Console, minimized bool, webviewDataDir string) *options.App {
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
		OnStartup:        console.startup,
		OnDomReady:       console.domReady,
		OnBeforeClose:    console.beforeClose,
		OnShutdown:       console.shutdown,
		// ONE bound type, and it is not App: every exported method of a
		// bound type is callable from the webview (docs/V4-CONSOLE.md §1.2).
		Bind: []interface{}{
			console,
		},
		// F-44: a second launch activates this window and exits.
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               guiSingleInstanceID,
			OnSecondInstanceLaunch: console.onSecondInstanceLaunch,
		},
		Windows: &wailswin.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
			WebviewUserDataPath:  webviewDataDir,
			Messages:             guiMessages(),
		},
	}
}

// onSecondInstanceLaunch runs in THIS (the first) instance when the GUI is
// launched again: bring the window back, even from the tray, and to the
// front. The brief always-on-top toggle is what gets past Windows' focus
// stealing rules; FindWindow/ShowWindow from the other process could not
// restore a window Wails had hidden.
func (c *Console) onSecondInstanceLaunch(_ options.SecondInstanceData) {
	writeInfoLog("GUI launched again: showing this window instead of starting a second one")
	if c.ctx == nil {
		return
	}
	runtime.WindowUnminimise(c.ctx)
	runtime.WindowShow(c.ctx)
	runtime.WindowSetAlwaysOnTop(c.ctx, true)
	ctx := c.ctx
	time.AfterFunc(300*time.Millisecond, func() { runtime.WindowSetAlwaysOnTop(ctx, false) })
}
