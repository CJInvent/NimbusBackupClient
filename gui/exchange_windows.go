//go:build windows
// +build windows

package main

// Windows Exchange detection, log-mode query, and post-backup tasks.

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// emsPreamble loads the Exchange Management Shell snap-in; version-independent
// across 2010-2019.
const emsPreamble = "Add-PSSnapin Microsoft.Exchange.Management.PowerShell.SnapIn -ErrorAction SilentlyContinue; "

// exchangeWriterGUID is the well-known "Microsoft Exchange Writer" VSS writer.
const exchangeWriterGUID = "76fe1ac4-15f7-4bcd-987e-8e1acb462fb7"

var exchangeVersionKeys = []struct {
	subkey string
	base   string
}{
	{"v15", "2013+"},
	{"v14", "2010"},
	{"v8", "2007"},
}

func detectExchange() (bool, string) {
	for _, ev := range exchangeVersionKeys {
		path := `SOFTWARE\Microsoft\ExchangeServer\` + ev.subkey + `\Setup`
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
		if err != nil {
			continue
		}
		installed := false
		if v, _, err := k.GetStringValue("MsiInstallPath"); err == nil && v != "" {
			installed = true
		}
		version := ev.base
		if ev.subkey == "v15" {
			version = refineV15(k)
		}
		_ = k.Close()
		if installed {
			return true, version
		}
	}
	return false, ""
}

// refineV15 maps the v15 minor build to a product year: 15.0=2013, 15.1=2016,
// 15.2=2019.
func refineV15(k registry.Key) string {
	minor, _, err := k.GetIntegerValue("MsiProductMinor")
	if err != nil {
		return "2013+"
	}
	switch minor {
	case 0:
		return "2013"
	case 1:
		return "2016"
	case 2:
		return "2019"
	default:
		return "2013+"
	}
}

// getExchangeCircularLogging queries how many mailbox databases have circular
// logging DISABLED (i.e. logs accumulate until a truncating backup). Returns
// whether the query succeeded, whether any database accumulates logs, and a
// human-readable detail line. Runs the EMS, which can be slow, so callers query
// it lazily rather than on every status refresh.
func getExchangeCircularLogging() (queried, accumulate bool, detail string) {
	ps := emsPreamble +
		"$d = @(Get-MailboxDatabase); $off = @($d | Where-Object { -not $_.CircularLoggingEnabled }); " +
		"Write-Output ($d.Count.ToString() + '|' + $off.Count.ToString())"
	out, err := exec.Command("powershell.exe", "-NonInteractive", "-Command", ps).CombinedOutput()
	if err != nil {
		writeDebugLog(fmt.Sprintf("[Exchange] circular-logging query failed: %v", err))
		return false, false, ""
	}
	fields := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(fields) != 2 {
		return false, false, ""
	}
	total, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
	off, _ := strconv.Atoi(strings.TrimSpace(fields[1]))
	if total == 0 {
		return false, false, ""
	}
	return true, off > 0, fmt.Sprintf("%d of %d databases have circular logging disabled (logs accumulate)", off, total)
}

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
		writeDebugLog("[Exchange] log truncation skipped: could not determine Exchange volumes (no truncation performed)")
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
		writeDebugLog(fmt.Sprintf("[Exchange] log truncation: cannot create diskshadow script: %v", err))
		return
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.WriteString(sb.String()); err != nil {
		_ = tmp.Close()
		writeDebugLog(fmt.Sprintf("[Exchange] log truncation: cannot write diskshadow script: %v", err))
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
		writeDebugLog(fmt.Sprintf("[Exchange] could not query database volumes: %v", err))
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
		writeDebugLog(fmt.Sprintf("[Exchange] TASK FAILED: %s%s: %v", label, code, err))
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
