package main

// The console's log (docs/V4-CONSOLE.md §9, "The console's logging").
//
// The service's logging module stays the service's: it redacts secrets, rotates
// under ProgramData and pushes WARN/ERROR to the control plane, none of which a
// process in the user's session should do. The console holds no secret, so
// there is nothing to redact; what it records is its own lifecycle and the
// calls the service refused or did not answer. It names PBS servers by id and
// never logs a URL.
//
// One file, per user, under %LOCALAPPDATA%\NimbusBackup (the WebView2 folder's
// parent), capped: past logMaxBytes it becomes console.log.1 and a new file
// starts. A user can have two profiles' worth of history and no more.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

// logMaxBytes is the cap on console.log before it rolls to console.log.1.
const logMaxBytes = 1 << 20

var (
	logMu   sync.Mutex
	logDir  string // resolved once, by initLogging
	logFile = "console.log"
)

// userDataDir is the console's per-user folder: %LOCALAPPDATA%\NimbusBackup,
// falling back to %APPDATA% and then the temp folder, the order main uses for
// the WebView2 data. Local, not roaming: it is always provisioned for the
// profile, which a domain account's roaming profile may not be.
func userDataDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.Getenv("APPDATA")
	}
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "NimbusBackup")
}

// initLogging resolves and creates the log folder. A folder that cannot be
// created leaves the console logging nowhere rather than failing to start.
func initLogging() {
	dir := userDataDir()
	// #nosec G301 G703 -- the user's own LOCALAPPDATA; the console runs as the user
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "console log folder %s not creatable: %v\n", dir, err)
		return
	}
	logMu.Lock()
	logDir = dir
	logMu.Unlock()
}

// consoleLogPath is where the log goes, or "" before initLogging succeeded.
func consoleLogPath() string {
	logMu.Lock()
	defer logMu.Unlock()
	if logDir == "" {
		return ""
	}
	return filepath.Join(logDir, logFile)
}

func writeLog(level, msg string) {
	logMu.Lock()
	defer logMu.Unlock()
	line := fmt.Sprintf("%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05.000"), level, msg)
	if logDir == "" {
		fmt.Fprint(os.Stderr, line)
		return
	}
	path := filepath.Join(logDir, logFile)
	if st, err := os.Stat(path); err == nil && st.Size() > logMaxBytes {
		// The previous generation is replaced; a failure here only means
		// this file keeps growing past the cap until the next attempt.
		_ = os.Rename(path, path+".1")
	}
	// #nosec G304 -- fixed name inside the console's own folder
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprint(os.Stderr, line)
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

func writeInfoLog(msg string)  { writeLog("INFO", msg) }
func writeWarnLog(msg string)  { writeLog("WARN", msg) }
func writeErrorLog(msg string) { writeLog("ERROR", msg) }

// writeCrashReport records a panic beside the log, overwritten each time.
// It used to go next to the executable, which under Program Files a standard
// user cannot write.
func writeCrashReport(message string) {
	path := filepath.Join(userDataDir(), "crash_report.txt")
	content := fmt.Sprintf(`=== NIMBUS BACKUP CONSOLE CRASH REPORT ===
Time: %s
Version: %s

%s

Console log: %s

Please report this issue to RDEM Systems:
- Website: https://nimbus.rdem-systems.com
- Include this crash_report.txt file
`, time.Now().Format("2006-01-02 15:04:05"), appVersion, message, consoleLogPath())
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write crash report: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "Crash report written to: %s\n", path)
}

// recoverAndReport is main's panic handler: report, give the tray a bounded
// chance to remove its icon, and exit 1 (a crash must never report success).
func recoverAndReport(where string) {
	r := recover()
	if r == nil {
		return
	}
	msg := fmt.Sprintf("PANIC in %s: %v\n%s", where, r, debug.Stack())
	writeErrorLog(msg)
	writeCrashReport(msg)
	fmt.Fprint(os.Stderr, "\n!!! APPLICATION CRASHED !!!\nSee crash_report.txt for details\n")
	attemptTrayCleanupBeforeCrash()
	os.Exit(1)
}
