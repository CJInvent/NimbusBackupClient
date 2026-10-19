package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf16"

	"controlplane"
)

// The service's half of self-update (NimbusControl docs/V4-CLIENT-RELEASES.md
// §7.2). Each step is in the order the spec numbers them:
//
//  1. an offer arrives on a check-in (the server stops offering a version
//     that failed twice, §7.3, so the agent keeps no count of its own);
//  2. it waits until nothing is running, and asks again next check-in;
//  3. it downloads into ProgramData\NimbusBackup\update (SYSTEM and
//     Administrators only) and checks size and SHA-256;
//  4. it reports `started`, writes a marker, and hands msiexec to a one-time
//     SYSTEM scheduled task, so the install outlives the service it replaces;
//  5. the next service to start finds the marker and the exit code the task
//     recorded, reports `installed` or `failed`, and deletes the files.

const (
	updateTaskName = "NimbusBackupUpdate"
	// How long the marker keeps a second install from starting. msiexec on a
	// slow machine takes minutes; an hour with no result means it never ran.
	updatePendingTimeout = time.Hour
	// How long a starting service waits for the task to record the exit code:
	// the new service starts inside InstallFinalize, before msiexec returns.
	updateResultWait   = 10 * time.Minute
	updateLogTailBytes = 8 << 10
)

var (
	updateBusy     atomic.Bool
	activeRestores func() int // set by service startup; nil counts as none
)

type updateMarker struct {
	Version string    `json:"version"`
	Started time.Time `json:"started"`
	MSI     string    `json:"msi"`
	Log     string    `json:"log"`
	Exit    string    `json:"exit"`
}

func init() {
	finishSelfUpdateAtStart = func(c *controlplane.Client) { go finishSelfUpdate(c) }
	startSelfUpdate = func(o controlplane.UpdateOffer) {
		if !updateBusy.CompareAndSwap(false, true) {
			return // one at a time; the next check-in offers again
		}
		go func() {
			defer updateBusy.Store(false)
			beginSelfUpdate(o)
		}()
	}
}

func updateDir() (string, error) {
	dir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "update"), nil
}

func readUpdateMarker(dir string) (*updateMarker, error) {
	b, err := os.ReadFile(filepath.Join(dir, "pending.json")) // #nosec G304 -- our own directory
	if err != nil {
		return nil, err
	}
	var m updateMarker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// machineIdle is step 2: no backup, no console restore. A portal browse runs
// inside the check-in loop that delivers this offer, so it cannot overlap.
func machineIdle() bool {
	if reg := currentRunRegistry(); reg != nil && reg.ActiveCount() > 0 {
		return false
	}
	if activeRestores != nil && activeRestores() > 0 {
		return false
	}
	return true
}

func beginSelfUpdate(o controlplane.UpdateOffer) {
	if cpClient == nil {
		return
	}
	dir, err := updateDir()
	if err != nil {
		writeWarnLog(fmt.Sprintf("[update] no update folder: %v", err))
		return
	}
	if m, err := readUpdateMarker(dir); err == nil && time.Since(m.Started) < updatePendingTimeout {
		return // an install is already under way
	}
	if !machineIdle() {
		return
	}

	if err := os.RemoveAll(dir); err != nil {
		writeWarnLog(fmt.Sprintf("[update] could not clear %s: %v", dir, err))
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeWarnLog(fmt.Sprintf("[update] could not create %s: %v", dir, err))
		return
	}
	if err := restrictToServiceOnly(dir); err != nil {
		writeWarnLog(fmt.Sprintf("[update] could not restrict %s: %v", dir, err))
		_ = os.RemoveAll(dir)
		return
	}

	msi := filepath.Join(dir, "agent.msi")
	writeInfoLog(fmt.Sprintf("[update] downloading %s", o.Version))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	err = cpClient.DownloadUpdate(ctx, o, msi)
	cancel()
	if err != nil {
		writeWarnLog(fmt.Sprintf("[update] %s did not download: %v", o.Version, err))
		reportUpdate(controlplane.UpdateResult{Version: o.Version, Outcome: "failed", LogTail: redactLogLine(err.Error())})
		_ = os.RemoveAll(dir)
		return
	}
	// A backup may have started during the download.
	if !machineIdle() {
		_ = os.RemoveAll(dir)
		return
	}

	m := updateMarker{
		Version: o.Version, Started: time.Now().UTC(), MSI: msi,
		Log: filepath.Join(dir, "install.log"), Exit: filepath.Join(dir, "exit.txt"),
	}
	cmdPath := filepath.Join(dir, "install.cmd")
	if err := os.WriteFile(cmdPath, []byte(updateCommand(m)), 0o600); err != nil {
		writeWarnLog(fmt.Sprintf("[update] could not write the install command: %v", err))
		_ = os.RemoveAll(dir)
		return
	}
	mb, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "pending.json"), mb, 0o600); err != nil {
		writeWarnLog(fmt.Sprintf("[update] could not write the marker: %v", err))
		_ = os.RemoveAll(dir)
		return
	}

	reportUpdate(controlplane.UpdateResult{Version: o.Version, Outcome: "started"})
	setSelfUpdatePending(o.Version)
	writeInfoLog(fmt.Sprintf("[update] installing %s; the service restarts during the install", o.Version))
	if err := runUpdateTask(updateTaskName, cmdPath); err != nil {
		writeWarnLog(fmt.Sprintf("[update] the install task did not start: %v", err))
		reportUpdate(controlplane.UpdateResult{Version: o.Version, Outcome: "failed", LogTail: redactLogLine(err.Error())})
		setSelfUpdatePending("")
		_ = os.RemoveAll(dir)
	}
}

// updateCommand is the batch file the task runs: msiexec, then its exit code
// beside the log. Paths are ours (ProgramData), quoted.
func updateCommand(m updateMarker) string {
	return "@echo off\r\n" +
		`msiexec.exe /i "` + m.MSI + `" /qn /norestart /l*v "` + m.Log + "\"\r\n" +
		`echo %ERRORLEVEL%>"` + m.Exit + "\"\r\n"
}

// finishSelfUpdate is step 5, run once when the service's agent starts: report
// what the last install did, then delete its files and its task.
func finishSelfUpdate(c *controlplane.Client) {
	dir, err := updateDir()
	if err != nil {
		return
	}
	m, err := readUpdateMarker(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			_ = os.RemoveAll(dir) // a marker we cannot read is not one we can act on
		}
		return
	}
	setSelfUpdatePending(m.Version)
	deadline := time.Now().Add(updateResultWait)
	code, ok := readExitCode(m.Exit)
	for !ok && time.Now().Before(deadline) && time.Since(m.Started) < updatePendingTimeout {
		time.Sleep(5 * time.Second)
		code, ok = readExitCode(m.Exit)
	}
	res := updateOutcome(m, appVersion, code, ok, readLogTail(m.Log))
	if err := c.ReportUpdate(res); err != nil {
		// Leave the marker: the next start reports it.
		writeWarnLog(fmt.Sprintf("[update] could not report the result of %s: %v", m.Version, err))
		return
	}
	if res.Outcome == "installed" {
		writeInfoLog(fmt.Sprintf("[update] now running %s", appVersion))
	} else {
		writeWarnLog(fmt.Sprintf("[update] %s did not install (exit %s)", m.Version, exitText(res.ExitCode)))
	}
	setSelfUpdatePending("")
	deleteUpdateTask(updateTaskName)
	_ = os.RemoveAll(dir)
}

// updateOutcome decides installed or failed. Installed means msiexec said so
// (0, or 3010: reboot required) AND this process is the version installed.
func updateOutcome(m *updateMarker, running string, code int, recorded bool, logTail string) controlplane.UpdateResult {
	res := controlplane.UpdateResult{Version: m.Version}
	if recorded {
		c := code
		res.ExitCode = &c
	}
	if recorded && (code == 0 || code == 3010) && running == m.Version {
		res.Outcome = "installed"
		return res
	}
	res.Outcome = "failed"
	switch {
	case !recorded:
		res.LogTail = "The installer recorded no result.\n" + logTail
	case code == 0 || code == 3010:
		res.LogTail = fmt.Sprintf("The installer succeeded but this service is %s.\n%s", running, logTail)
	default:
		res.LogTail = logTail
	}
	if len(res.LogTail) > updateLogTailBytes {
		res.LogTail = res.LogTail[len(res.LogTail)-updateLogTailBytes:]
	}
	return res
}

func readExitCode(path string) (int, bool) {
	b, err := os.ReadFile(path) // #nosec G304 -- our own directory
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return n, err == nil
}

// readLogTail returns the last 8 KB of msiexec's log, redacted. msiexec /l*v
// writes UTF-16LE with a byte-order mark.
func readLogTail(path string) string {
	b, err := os.ReadFile(path) // #nosec G304 -- our own directory
	if err != nil {
		return ""
	}
	text := decodeMSILog(b)
	if len(text) > updateLogTailBytes {
		text = text[len(text)-updateLogTailBytes:]
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = redactLogLine(strings.TrimRight(l, "\r"))
	}
	return strings.Join(lines, "\n")
}

func decodeMSILog(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		b = b[2:]
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
		}
		return string(utf16.Decode(u))
	}
	return string(b)
}

func exitText(p *int) string {
	if p == nil {
		return "not recorded"
	}
	return strconv.Itoa(*p)
}

func reportUpdate(r controlplane.UpdateResult) {
	if cpClient == nil {
		return
	}
	if err := cpClient.ReportUpdate(r); err != nil {
		writeWarnLog(fmt.Sprintf("[update] could not report %s for %s: %v", r.Outcome, r.Version, err))
	}
}

// setSelfUpdatePending sets the version the status card shows as installing.
func setSelfUpdatePending(version string) {
	selfUpdateMu.Lock()
	selfUpdatePending = version
	selfUpdateMu.Unlock()
}
