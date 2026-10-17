package main

// Running the storage inventory: NimbusControl docs/V4-FIRST-ENROLLMENT.md
// section 3.4 (as built: 10.1). A failed inventory latches an error the
// operator has to look at, so it must say why it failed (the exit status and
// what the command printed, not just "exit status 1"), and a transient
// failure (a disk busy during a backup, a slow WMI answer) gets a bounded
// retry before anything is latched.
//
// OS-neutral on purpose: the PowerShell query is Windows-only
// (storage_inventory_windows.go), but the retry, the wording and the exec
// handling are tested on any OS with a real child process.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// The bounds. A var only so a test can shorten the attempt count.
var storageInventoryAttempts = 3

const (
	// storageInventoryTimeout bounds one run of the query.
	storageInventoryTimeout = 30 * time.Second
	// storageInventoryRetryCap: no attempt STARTS after this much time, so a
	// query that times out twice is not run a third time (about a minute,
	// not a minute and a half, on the check-in path).
	storageInventoryRetryCap = 40 * time.Second
	// storageInventoryStderrMax bounds what is quoted from stderr. The
	// report's own limit on its error (512 bytes, storageWireStatus) applies
	// on top, so the start of it -- the cmdlet and what it said -- is what
	// reaches the machine page.
	storageInventoryStderrMax = 1024
)

// storageInventoryBackoff is the pause before the second and third attempt.
var storageInventoryBackoff = []time.Duration{2 * time.Second, 5 * time.Second}

// inventoryClock is what the retry reads time from and waits with.
type inventoryClock struct {
	now   func() time.Time
	sleep func(time.Duration)
}

// retryStorageInventory runs attempt until it succeeds, has run
// storageInventoryAttempts times, or the next attempt would start after
// storageInventoryRetryCap. The error is the last attempt's, with the count
// when more than one ran ("... (3 attempts)").
func retryStorageInventory(attempt func() ([]byte, error), clock inventoryClock) ([]byte, error) {
	start := clock.now()
	var last error
	ran := 0
	for ran < storageInventoryAttempts {
		if ran > 0 {
			pause := storageInventoryBackoff[min(ran-1, len(storageInventoryBackoff)-1)]
			if clock.now().Add(pause).Sub(start) >= storageInventoryRetryCap {
				break
			}
			clock.sleep(pause)
		}
		ran++
		out, err := attempt()
		if err == nil {
			return out, nil
		}
		last = err
	}
	if ran > 1 {
		return nil, fmt.Errorf("%w (%d attempts)", last, ran)
	}
	return nil, last
}

// runInventoryCommand runs the query once and returns its stdout, or an error
// that names what failed: "timed out after 30s", or the exit status and the
// condensed stderr ("exit status 1: Get-Disk : Access is denied."). prepare,
// when set, adjusts the command before it starts (Windows hides its window).
func runInventoryCommand(timeout time.Duration, prepare func(*exec.Cmd), name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// A killed process can leave a child holding its output pipes open, and
	// Output would wait for that child. Stop waiting soon after the kill.
	cmd.WaitDelay = 2 * time.Second
	if prepare != nil {
		prepare(cmd)
	}
	out, err := cmd.Output() // stderr lands in ExitError.Stderr (bounded by os/exec)
	if err == nil {
		return out, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("timed out after %s", timeout)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if reason := condenseInventoryStderr(exit.Stderr); reason != "" {
			return nil, fmt.Errorf("%s: %s", exit.Error(), reason)
		}
		return nil, errors.New(exit.Error())
	}
	return nil, err
}

var (
	cliXMLErrorString = regexp.MustCompile(`(?s)<S S="Error">(.*?)</S>`)
	cliXMLEscape      = regexp.MustCompile(`_x([0-9A-Fa-f]{4})_`)
	inventorySpaces   = regexp.MustCompile(`\s+`)
)

// condenseInventoryStderr turns what PowerShell printed into one line an
// operator can read on the machine page: the message and its category, not
// the position report, the echoed script line or its ~~~ underline. CLIXML
// (what PowerShell writes when its parent looks like PowerShell) is read for
// its Error strings first. Cut at storageInventoryStderrMax bytes on a
// character boundary.
func condenseInventoryStderr(b []byte) string {
	text := string(b)
	if strings.HasPrefix(strings.TrimSpace(text), "#< CLIXML") {
		var parts []string
		for _, m := range cliXMLErrorString.FindAllStringSubmatch(text, -1) {
			s := cliXMLEscape.ReplaceAllStringFunc(m[1], func(code string) string {
				var r rune
				if _, err := fmt.Sscanf(code, "_x%04X_", &r); err != nil {
					return code
				}
				return string(r)
			})
			parts = append(parts, html.UnescapeString(s))
		}
		text = strings.Join(parts, "")
	}
	var keep []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", "\n"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "",
			strings.HasPrefix(line, "At ") && strings.Contains(line, " char:"): // "At line:2 char:11", "At C:\x.ps1:3 char:5"
			continue
		case strings.HasPrefix(line, "+"):
			rest := strings.TrimSpace(strings.TrimPrefix(line, "+"))
			if !strings.HasPrefix(rest, "CategoryInfo") && !strings.HasPrefix(rest, "FullyQualifiedErrorId") {
				continue // the echoed script line, or its underline
			}
			line = rest
		}
		keep = append(keep, inventorySpaces.ReplaceAllString(line, " "))
	}
	out := strings.Join(keep, " | ")
	if len(out) <= storageInventoryStderrMax {
		return out
	}
	cut := storageInventoryStderrMax
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return out[:cut]
}
