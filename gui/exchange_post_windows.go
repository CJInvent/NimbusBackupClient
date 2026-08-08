//go:build windows && service
// +build windows,service

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Exchange post-backup tasks: database health checks and log truncation.
//
// Two tags, both load-bearing. Windows because Exchange is; SERVICE because
// only the service runs backups (docs/V4-PIPELINE.md §3.1), and these run
// AFTER a successful one. Leaving them in exchange_windows.go put an
// automation that runs diskshadow.exe and PowerShell against live Exchange
// databases inside the front end, where nothing could ever call it.
//
// exchange_windows.go keeps detection and the log-mode query, because the GUI
// still displays both.

// runExchangePostBackup runs the enabled app-aware tasks after a successful
// backup. Version-independent (EMS). Best-effort; every outcome is logged.
func runExchangePostBackup(version string, healthCheck, truncate bool) {
	if healthCheck {
		ps := emsPreamble + "Get-MailboxDatabase -Status | Select-Object Name,Mounted,BackupInProgress,LastFullBackup | Format-List"
		runExchangeCommand("database health", "powershell.exe", "-NonInteractive", "-Command", ps)
	}
	if truncate {
		runExchangeLogTruncation()
	}
}

// runExchangeLogTruncation truncates committed Exchange transaction logs the
// SUPPORTED way: a writer-participating VSS full backup via diskshadow whose
// end-backup causes the Exchange writer to truncate logs for the databases on
// the snapshotted volumes. It never deletes .log files directly (that corrupts
// the database). The shadow is volatile - discarded immediately - because we
// only want the truncation side effect; the databases were already captured by
// the main backup. If anything fails, diskshadow simply does not truncate.
func runExchangeLogTruncation() {
	vols := exchangeVolumes()
	if len(vols) == 0 {
		writeWarnLog("[Exchange] log truncation skipped: could not determine Exchange volumes (no truncation performed)")
		return
	}

	var sb strings.Builder
	sb.WriteString("set context volatile\r\n")
	sb.WriteString("set verbose on\r\n")
	fmt.Fprintf(&sb, "writer verify {%s}\r\n", exchangeWriterGUID)
	sb.WriteString("begin backup\r\n")
	for i, v := range vols {
		fmt.Fprintf(&sb, "add volume %s alias exvol%d\r\n", v, i)
	}
	sb.WriteString("create\r\n")
	sb.WriteString("end backup\r\n")

	tmp, err := os.CreateTemp("", "nimbus-exch-*.dsh")
	if err != nil {
		writeErrorLog(fmt.Sprintf("[Exchange] log truncation: cannot create diskshadow script: %v", err))
		return
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.WriteString(sb.String()); err != nil {
		_ = tmp.Close()
		writeErrorLog(fmt.Sprintf("[Exchange] log truncation: cannot write diskshadow script: %v", err))
		return
	}
	_ = tmp.Close()

	writeDebugLog(fmt.Sprintf("[Exchange] log truncation: running diskshadow for volumes %v", vols))
	runExchangeCommand("log truncation (diskshadow)", "diskshadow.exe", "/s", tmpPath)
}

// exchangeVolumes returns the unique drive letters (e.g. "D:") holding Exchange
// database and log files.
func exchangeVolumes() []string {
	ps := emsPreamble +
		"Get-MailboxDatabase | ForEach-Object { $_.EdbFilePath.DriveName; $_.LogFolderPath.DriveName } | Sort-Object -Unique"
	out, err := exec.Command("powershell.exe", "-NonInteractive", "-Command", ps).CombinedOutput()
	if err != nil {
		writeErrorLog(fmt.Sprintf("[Exchange] could not query database volumes: %v", err))
		return nil
	}
	seen := map[string]bool{}
	var vols []string
	for _, line := range strings.Split(string(out), "\n") {
		d := strings.TrimSpace(line)
		if len(d) == 2 && d[1] == ':' && !seen[d] {
			seen[d] = true
			vols = append(vols, d)
		}
	}
	return vols
}

// runExchangeCommand executes one task and logs its full outcome (with exit
// code on failure).
func runExchangeCommand(label, name string, args ...string) {
	out, err := exec.Command(name, args...).CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		code := ""
		if ee, ok := err.(*exec.ExitError); ok {
			code = " (exit " + strconv.Itoa(ee.ExitCode()) + ")"
		}
		writeErrorLog(fmt.Sprintf("[Exchange] TASK FAILED: %s%s: %v", label, code, err))
		if trimmed != "" {
			writeDebugLog(fmt.Sprintf("[Exchange] %s output: %s", label, truncateForLog(trimmed, 3000)))
		}
		return
	}
	writeDebugLog(fmt.Sprintf("[Exchange] task OK: %s", label))
	if trimmed != "" {
		writeCatLog(catSecurity, fmt.Sprintf("[Exchange] %s output: %s", label, truncateForLog(trimmed, 3000)))
	}
}

func truncateForLog(s string, max int) string {
	if len(s) > max {
		return s[:max] + "…(truncated)"
	}
	return s
}
