//go:build windows
// +build windows

package main

// Windows Exchange detection and log-mode query. The post-backup tasks live
// in exchange_post_windows.go, which is service-only.

import (
	"fmt"
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
		writeErrorLog(fmt.Sprintf("[Exchange] circular-logging query failed: %v", err))
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
