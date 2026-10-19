//go:build !service
// +build !service

package main

import (
	"embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/wailsapp/wails/v2"
)

//go:embed all:frontend/dist
var assets embed.FS

const (
	appName = "Nimbus Backup"
)

var (
	crashReportPath string
)

func init() {

	// Get executable directory for crash reports
	exePath, _ := os.Executable()
	exeDir := filepath.Dir(exePath)
	crashReportPath = filepath.Join(exeDir, "crash_report.txt")

	// Setup panic recovery
	defer func() {
		if r := recover(); r != nil {
			crashMsg := fmt.Sprintf("PANIC during init: %v\n%s", r, debug.Stack())
			writeInfoLog(crashMsg)
			writeCrashReport(crashMsg)
		}
	}()
}

func main() {
	// Route stdlib log into the visible rotating log before anything can
	// log. Matters less here than in the service build (the GUI may have a
	// console) but keeps both entry points on one logging path.
	redirectStdlibLog()

	// Parse command line flags
	minimized := flag.Bool("minimized", false, "Start minimized to system tray")
	logcat := flag.String("logcat", "", "Enable detailed log categories (comma-separated): pbs,chunks,security,api or 'all'")
	flag.Parse()
	if *logcat != "" {
		SetLogCategories(*logcat)
		writeInfoLog("Detailed log categories enabled: " + *logcat)
	}

	// ONE GUI PER USER SESSION is Wails' SingleInstanceLock (guiOptions
	// below): a second launch hands its arguments to the first and exits
	// inside wails.Run. The hand-made mutex this replaces read a SECOND
	// GetLastError() after CreateMutex had already returned
	// ERROR_ALREADY_EXISTS as its error, and in Go that value is not
	// reliable, so every launch "was the only instance" (ledger F-44). So
	// nothing between here and wails.Run may have side effects a second
	// launch should not cause: legacy auto-start cleanup moved to startup.

	// Setup panic recovery for main
	defer func() {
		if r := recover(); r != nil {
			crashMsg := fmt.Sprintf("PANIC in main: %v\n%s", r, debug.Stack())
			writeInfoLog(crashMsg)
			writeCrashReport(crashMsg)
			fmt.Fprint(os.Stderr, "\n!!! APPLICATION CRASHED !!!\nSee crash_report.txt for details\n")
			// Best-effort tray cleanup: an unhandled panic used to skip the
			// tray's own Shell_NotifyIcon(NIM_DELETE) entirely, leaking the
			// icon on every crash (see gui/tray.go's onExit doc comment).
			// No-op off Windows / when no tray was ever set up.
			attemptTrayCleanupBeforeCrash()
			os.Exit(1)
		}
	}()

	// Logging is now handled by RotatingLogger (initialized in logging_gui.go)
	writeInfoLog(fmt.Sprintf("=== %s v%s Starting ===", appName, appVersion))
	writeInfoLog(fmt.Sprintf("Time: %s", time.Now().Format(time.RFC3339)))
	writeInfoLog(fmt.Sprintf("Service log: %s", GetServiceLogPath()))
	writeInfoLog(fmt.Sprintf("Backup log: %s", GetBackupLogPath()))
	writeInfoLog(fmt.Sprintf("Crash report path: %s", crashReportPath))

	// The console: the one type bound to the webview (console.go). It
	// loads no configuration; everything it shows, it asks the service for.
	console := NewConsole()

	// Resolve the WebView2 user-data folder. Prefer Local AppData: it's always
	// provisioned per profile and isn't subject to roaming-profile redirection,
	// which fails for domain accounts whose roaming profile isn't present on the
	// machine (the "couldn't create the data directory" error when elevating as
	// a different user). Pre-create it so WebView2 doesn't fail to initialize and
	// leave the window invisible.
	webviewBase := os.Getenv("LOCALAPPDATA")
	if webviewBase == "" {
		webviewBase = os.Getenv("APPDATA")
	}
	if webviewBase == "" {
		webviewBase = os.TempDir()
	}
	webviewDataDir := filepath.Join(webviewBase, "NimbusBackup", "WebView2")
	// #nosec G703 -- base is the OS-provided LOCALAPPDATA/APPDATA env var (the GUI runs as the user, not the elevated service); path suffix is constant, no user-controlled traversal
	if err := os.MkdirAll(webviewDataDir, 0o755); err != nil {
		writeWarnLog(fmt.Sprintf("WebView2 data dir %s not creatable: %v; falling back to temp", webviewDataDir, err))
		webviewDataDir = filepath.Join(os.TempDir(), "NimbusBackup", "WebView2")
		_ = os.MkdirAll(webviewDataDir, 0o755)
	}
	writeInfoLog(fmt.Sprintf("WebView2 user-data path: %s", webviewDataDir))

	appOptions := guiOptions(console, *minimized, webviewDataDir)

	if *minimized {
		writeInfoLog("Starting in minimized mode (hidden to tray)")
	}

	writeInfoLog("Application options configured")

	// Run application
	writeInfoLog("Starting Wails runtime...")
	err := wails.Run(appOptions)

	if err != nil {
		errMsg := fmt.Sprintf("ERROR: Wails.Run failed: %v\nStack trace:\n%s", err, debug.Stack())
		writeInfoLog(errMsg)
		writeCrashReport(errMsg)
		fmt.Fprint(os.Stderr, "\n!!! APPLICATION FAILED TO START !!!\n")
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Check crash_report.txt and %s\n", GetServiceLogPath())
		os.Exit(1)
	}

	writeInfoLog("Application shutdown normally")
}

func writeCrashReport(message string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")

	crashContent := fmt.Sprintf(`=== NIMBUS BACKUP CRASH REPORT ===
Time: %s
Version: %s

%s

=== SYSTEM INFO ===
Service Log: %s
Backup Log: %s

Please report this issue to RDEM Systems:
- Website: https://nimbus.rdem-systems.com
- Include this crash_report.txt file
`, timestamp, appVersion, message, GetServiceLogPath(), GetBackupLogPath())

	// Write to crash report file (overwrite each time)
	err := os.WriteFile(crashReportPath, []byte(crashContent), 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write crash report: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "Crash report written to: %s\n", crashReportPath)
	}
}
