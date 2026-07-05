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
	"sync"
)

var (
	postureOnce     sync.Once
	postureWarnings []string
)

// GetSecurityWarnings returns human-readable security posture warnings for
// this machine (empty when nothing is wrong). Bound to the frontend; also
// logged once per process at first use.
func (a *App) GetSecurityWarnings() []string {
	postureOnce.Do(func() {
		postureWarnings = collectSecurityWarnings()
		for _, w := range postureWarnings {
			writeDebugLog(fmt.Sprintf("[SecurityPosture] WARNING: %s", w))
		}
		if len(postureWarnings) == 0 {
			writeDebugLog("[SecurityPosture] No issues detected")
		}
	})
	return postureWarnings
}
