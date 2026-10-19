package main

// The console: Nimbus Backup's window, in the user's session.
//
// It is a client of the service and nothing else (docs/V4-CONSOLE.md). This
// module cannot import the service's code: its dependencies are gui/api,
// Wails, systray and x/sys, and CI fails on any other module (§5 item 1).

import (
	"embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"
)

//go:embed all:frontend/dist
var assets embed.FS

const appName = "Nimbus Backup"

func main() {
	initLogging()
	defer recoverAndReport("main")

	minimized := flag.Bool("minimized", false, "Start minimized to system tray")
	flag.Parse()

	// ONE CONSOLE PER USER SESSION is Wails' SingleInstanceLock (guiOptions):
	// a second launch hands its arguments to the first and exits inside
	// wails.Run. The hand-made mutex this replaced read a SECOND
	// GetLastError() after CreateMutex had already returned
	// ERROR_ALREADY_EXISTS as its error, and in Go that value is not reliable,
	// so every launch "was the only instance" (ledger F-44). So nothing
	// between here and wails.Run may have side effects a second launch
	// should not cause.

	writeInfoLog(fmt.Sprintf("=== %s console v%s starting (%s) ===", appName, appVersion, time.Now().Format(time.RFC3339)))

	console := NewConsole()

	// WebView2's user-data folder, under the console's own per-user folder.
	// Local AppData rather than roaming: it is always provisioned for the
	// profile, which a domain account's roaming profile may not be (the
	// "couldn't create the data directory" error when elevating as another
	// user). Pre-created so WebView2 does not fail to initialize and leave the
	// window invisible.
	webviewDataDir := filepath.Join(userDataDir(), "WebView2")
	// #nosec G301 G703 -- the user's own LOCALAPPDATA; the suffix is constant
	if err := os.MkdirAll(webviewDataDir, 0o755); err != nil {
		writeWarnLog(fmt.Sprintf("WebView2 data dir %s not creatable: %v; falling back to temp", webviewDataDir, err))
		webviewDataDir = filepath.Join(os.TempDir(), "NimbusBackup", "WebView2")
		_ = os.MkdirAll(webviewDataDir, 0o755)
	}

	if *minimized {
		writeInfoLog("Starting minimized (hidden to tray)")
	}
	if err := wails.Run(guiOptions(console, *minimized, webviewDataDir)); err != nil {
		msg := fmt.Sprintf("wails.Run failed: %v", err)
		writeErrorLog(msg)
		writeCrashReport(msg)
		fmt.Fprintf(os.Stderr, "\n!!! APPLICATION FAILED TO START !!!\nError: %v\nSee %s\n", err, consoleLogPath())
		os.Exit(1)
	}
	writeInfoLog("Console shut down normally")
}
