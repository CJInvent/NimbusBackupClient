package main

// Security posture checks (zero-trust support). Detection is outcome-based:
// rather than guessing whether Windows Update delivered a microcode package
// for a given chipset (Microsoft only ships microcode KBs for a subset of
// Intel platforms; OEM BIOS/UEFI is the primary channel and older chipsets
// often never receive updates from either), we ask the kernel what mitigation
// support actually exists right now via NtQuerySystemInformation - the same
// source Microsoft's own SpeculationControl module reads. That catches every
// cause: unpatched microcode, missing OS support, or mitigations disabled by
// policy. Windows Update health is checked separately as a supporting signal.

import (
	"fmt"
	"slices"
	"sync"
	"time"
)

// postureMaxAge is how long a posture reading is reused. The service answers
// /status from it for as long as it runs, which is months, and the
// Windows Update check is about recency: computed once per process, it would
// keep saying "fine" long after updates had stopped arriving.
const postureMaxAge = time.Hour

var (
	postureMu       sync.Mutex
	postureAt       time.Time
	postureWarnings []string
)

// GetSecurityWarnings returns human-readable security posture warnings for
// this machine (empty when nothing is wrong). The service serves it on
// /status (security_warnings); until V4-CONSOLE C2 the console also calls it
// itself. Re-read at most hourly; logged when the answer changes.
func (a *App) GetSecurityWarnings() []string {
	postureMu.Lock()
	defer postureMu.Unlock()
	if !postureAt.IsZero() && time.Since(postureAt) < postureMaxAge {
		return slices.Clone(postureWarnings)
	}
	fresh := collectSecurityWarnings()
	if postureAt.IsZero() || !slices.Equal(fresh, postureWarnings) {
		for _, w := range fresh {
			writeWarnLog(fmt.Sprintf("[SecurityPosture] WARNING: %s", w))
		}
		if len(fresh) == 0 {
			writeInfoLog("[SecurityPosture] No issues detected")
		}
	}
	postureWarnings, postureAt = fresh, time.Now()
	return slices.Clone(postureWarnings)
}
