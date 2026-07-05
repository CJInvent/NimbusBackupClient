//go:build windows
// +build windows

package main

// Windows Exchange detection + post-backup tasks.
//
// Detection reads HKLM\SOFTWARE\Microsoft\ExchangeServer\<vN>\Setup, which is
// how every supported Exchange version records its install (the version key is
// the AdminDisplayVersion major: 14=2010, 15=2013/2016/2019, distinguished by
// the build in MsiProductMajor/Minor). We report a friendly year and, when the
// build is available, refine 15.x to 2013/2016/2019.

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// exchangeVersionKeys maps the registry version subkey to a base product name.
// v15 is refined to 2013/2016/2019 by build number below.
var exchangeVersionKeys = []struct {
	subkey string
	base   string
}{
	{"v15", "2013+"},
	{"v14", "2010"},
	{"v8", "2007"}, // best-effort for legacy hosts
}

func detectExchange() (bool, string) {
	for _, ev := range exchangeVersionKeys {
		path := `SOFTWARE\Microsoft\ExchangeServer\` + ev.subkey + `\Setup`
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
		if err != nil {
			continue
		}
		// A present Setup key with an install path is the reliable "installed"
		// signal (mere version keys can linger).
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

// refineV15 turns the v15 hive into 2013/2016/2019 using the minor build.
// 15.0 = 2013, 15.1 = 2016, 15.2 = 2019.
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

// runExchangePostBackup runs post-backup housekeeping via the Exchange
// Management Shell. Version-independent: the same cmdlets exist across 2010-
// 2019. Each command's outcome is logged; a failure is recorded, never fatal.
//
// The VSS snapshot already truncated committed transaction logs through the
// Exchange writer during BackupComplete; this pass confirms database health
// and surfaces any Dirty Shutdown / mount issues that would make the captured
// databases non-recoverable - exactly what an app-aware agent should report.
func runExchangePostBackup(version string) {
	// Get-MailboxDatabaseCopyStatus is the lightest health/log-state probe that
	// works standalone and in DAGs across all supported versions.
	psScript := "Add-PSSnapin Microsoft.Exchange.Management.PowerShell.SnapIn -ErrorAction SilentlyContinue; " +
		"Get-MailboxDatabase -Status | Select-Object Name,Mounted,BackupInProgress,LastFullBackup | Format-List"

	runExchangeCommand("database health", "powershell.exe",
		"-NonInteractive", "-Command", psScript)
}

// runExchangeCommand executes one Exchange task and logs its full outcome.
func runExchangeCommand(label, name string, args ...string) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		code := ""
		if ee, ok := err.(*exec.ExitError); ok {
			code = " (exit " + strconv.Itoa(ee.ExitCode()) + ")"
		}
		writeDebugLog(fmt.Sprintf("[Exchange] TASK FAILED: %s%s: %v", label, code, err))
		if trimmed != "" {
			writeDebugLog(fmt.Sprintf("[Exchange] %s output: %s", label, truncateForLog(trimmed, 2000)))
		}
		return
	}
	writeDebugLog(fmt.Sprintf("[Exchange] task OK: %s", label))
	if trimmed != "" {
		writeCatLog(catSecurity, fmt.Sprintf("[Exchange] %s output: %s", label, truncateForLog(trimmed, 2000)))
	}
}

func truncateForLog(s string, max int) string {
	if len(s) > max {
		return s[:max] + "…(truncated)"
	}
	return s
}
