package main

import (
	"bufio"
	"controlplane"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// cpHandleRunLogCommand answers a fetch_run_log command: filter the local
// service log to the run's own time window (and optionally a checkpoint
// keyword), upload the result as the command's artifact. Server side:
// ImageBrowseController::fetchLog (queues) / receiveArtifact (kind=
// 'run_log', accepts this same upload path with a 10MB cap instead of
// extraction's 5GB).
//
// SCOPE, DELIBERATELY: reads ONLY the current service-service.log, not
// any rotated .log.<timestamp>[.gz] files RotatingLogger may have already
// rotated away (log_rotation.go: 10MB/file, 5 kept). A run whose window
// has already rotated out of the current file returns a partial or empty
// result rather than an error -- the artifact still uploads, just thin.
// This covers the common case (a recent run, which is what "detailed
// report" is for) without the added complexity of globbing, ordering, and
// decompressing rotated files in this pass.
func (a *App) cpHandleRunLogCommand(cmd controlplane.Command) (controlplane.CommandResult, bool) {
	if cmd.Command != "fetch_run_log" {
		return controlplane.CommandResult{}, false
	}
	if cpClient == nil {
		return cpErr("control server client not initialised"), true
	}

	runUUID, _ := cmd.Payload["run_uuid"].(string)
	startedAtRaw, _ := cmd.Payload["started_at"].(string)
	finishedAtRaw, _ := cmd.Payload["finished_at"].(string) // may be absent/empty: run still in progress
	checkpoint, _ := cmd.Payload["checkpoint"].(string)

	if runUUID == "" || startedAtRaw == "" {
		return cpErr("fetch_run_log payload missing run_uuid or started_at"), true
	}
	started, err := time.Parse(time.RFC3339, startedAtRaw)
	if err != nil {
		return cpErr("started_at not RFC3339: " + err.Error()), true
	}
	// No finished_at (run still in progress when the request was made, or
	// never completed) -- window extends to now rather than being open-
	// ended, so a stalled/crashed run doesn't pull in unrelated LATER log
	// content from whatever ran on this machine afterward.
	ended := time.Now()
	if finishedAtRaw != "" {
		if t, err := time.Parse(time.RFC3339, finishedAtRaw); err == nil {
			ended = t
		}
		// A bad finished_at is not fatal -- fall back to "now" rather than
		// refuse the whole fetch over an optional, secondary field.
	}
	// Small margin on both sides: log lines for "starting" and "finishing"
	// a run are written a few milliseconds either side of the timestamps
	// backup_runs itself records, and an exact-boundary window would clip
	// them.
	started = started.Add(-5 * time.Second)
	ended = ended.Add(5 * time.Second)

	content, kept, err := filterLogByTimeWindow(GetServiceLogPath(), started, ended, checkpoint)
	if err != nil {
		return cpErr("reading service log: " + err.Error()), true
	}

	tmp, err := os.CreateTemp("", "nimbus-runlog-*.log")
	if err != nil {
		return cpErr("temp file: " + err.Error()), true
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return cpErr("writing temp log: " + err.Error()), true
	}
	if err := tmp.Close(); err != nil {
		return cpErr("closing temp log: " + err.Error()), true
	}

	if err := cpClient.PostCommandArtifact(cmd.ID, tmpPath); err != nil {
		return cpErr("artifact upload: " + err.Error()), true
	}
	writeDebugLog(fmt.Sprintf("[controlplane] run %s log fetch: %d line(s) uploaded for command %d", runUUID, kept, cmd.ID))
	return controlplane.CommandResult{OK: true, Result: map[string]interface{}{
		"artifact": true, "lines": kept,
	}}, true
}

// logLineTimestamp matches writeLogToLogger's exact format:
// "[SERVICE] [2026-07-28 09:06:42] message". The bracketed prefix
// (SERVICE/BACKUP) varies; the timestamp shape does not.
var logLineTimestamp = regexp.MustCompile(`^\[\w+\]\s+\[(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\]`)

// filterLogByTimeWindow returns every line of path whose embedded
// timestamp falls within [start, end], optionally further narrowed to
// lines containing keyword (case-insensitive substring — a best-effort
// checkpoint filter, not exact: the live milestone events
// (RunReporter.Event, see controlplane_glue.go/machine_backup_windows.go)
// are the authoritative per-checkpoint source; this is a convenience
// narrowing of the full log, not a replacement for them).
//
// A line with no parseable timestamp prefix is SKIPPED, not included:
// every line writeLogToLogger produces has this exact prefix, so an
// unparseable line is unexpected content this function has no way to
// place in time, and guessing it belongs in the window risks leaking
// unrelated log content into the artifact.
func filterLogByTimeWindow(path string, start, end time.Time, keyword string) (string, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()

	var out strings.Builder
	kept := 0
	keywordLower := strings.ToLower(keyword)
	sc := bufio.NewScanner(f)
	// Default bufio.Scanner line cap is 64KB; a log line is never that
	// long in practice, but size the buffer generously rather than have
	// a single unusually long line abort the whole scan.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		m := logLineTimestamp.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1], time.Local)
		if err != nil {
			continue
		}
		if t.Before(start) || t.After(end) {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(line), keywordLower) {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
		kept++
	}
	return out.String(), kept, sc.Err()
}
