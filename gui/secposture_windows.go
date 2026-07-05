//go:build windows
// +build windows

package main

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// collectSecurityWarnings probes CPU speculative-execution mitigation support
// (Spectre/Meltdown family) and Windows Update recency. A machine whose
// hardware/microcode cannot support branch-target or speculative-store-bypass
// mitigations is a real compromise risk for a process that decrypts backup
// credentials in memory, which is why we surface it prominently.
func collectSecurityWarnings() []string {
	var warnings []string
	if w := checkSpeculationControl(); w != "" {
		warnings = append(warnings, w)
	}
	if w := checkUpdateRecency(); w != "" {
		warnings = append(warnings, w)
	}
	return warnings
}

// --- Speculation control -------------------------------------------------

// SystemSpeculationControlInformation (info class 201) returns a flags DWORD.
// We rely on ONE well-documented, high-confidence bit -
// BpbDisabledNoHardwareSupport (bit 2): Windows sets it when it wanted a branch
// prediction barrier but the CPU/microcode cannot provide one - precisely the
// "old chipset, microcode never delivered by Windows Update" case. Default is
// no warning, so an unknown/healthy state never false-alarms. Deeper per-bit
// interpretation is intentionally omitted: the layout is version-sensitive and
// a wrong bit would cry wolf on healthy machines.
const systemSpeculationControlInformation = 201

const bpbDisabledNoHardwareSupport = 1 << 2

func checkSpeculationControl() string {
	var flags uint32
	var retLen uint32
	ntdll := windows.NewLazySystemDLL("ntdll.dll")
	proc := ntdll.NewProc("NtQuerySystemInformation")
	r, _, _ := proc.Call(
		uintptr(systemSpeculationControlInformation),
		uintptr(unsafe.Pointer(&flags)),
		uintptr(unsafe.Sizeof(flags)),
		uintptr(unsafe.Pointer(&retLen)),
	)
	if r != 0 {
		// STATUS_NOT_IMPLEMENTED / invalid class: a Windows build predating the
		// Spectre/Meltdown mitigations - genuinely old and unpatched.
		return "This Windows build predates Spectre/Meltdown mitigations - the platform may be unpatched against speculative-execution attacks. Update Windows and the system firmware/BIOS."
	}
	if flags&bpbDisabledNoHardwareSupport != 0 {
		return "This CPU/microcode lacks hardware support for branch-target speculative-execution mitigation (Spectre v2). Windows Update does not deliver microcode for many chipsets - apply the manufacturer's latest BIOS/UEFI firmware. Note: backup credentials are decrypted in memory on this machine."
	}
	return ""
}

// --- Windows Update recency ---------------------------------------------

// checkUpdateRecency reads the last successful update-detection time from the
// registry as a coarse "is Windows Update functioning" signal. A machine that
// hasn't successfully contacted Windows Update in a long time is exactly the
// case the user flagged (WSUS/policy breakage, or an unmanaged chipset), where
// microcode/security fixes silently never arrive.
func checkUpdateRecency() string {
	const keyPath = `SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\Results\Detect`
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, keyPath, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "" // absence of the key is not itself evidence of a problem
	}
	defer k.Close()

	val, _, err := k.GetStringValue("LastSuccessTime")
	if err != nil {
		return ""
	}
	// Format: "YYYY-MM-DD HH:MM:SS" (UTC).
	t, err := time.Parse("2006-01-02 15:04:05", val)
	if err != nil {
		return ""
	}
	if age := time.Since(t); age > 60*24*time.Hour {
		return fmt.Sprintf("Windows Update last succeeded %d days ago - security and microcode updates may not be reaching this machine. Verify Windows Update / WSUS is working.", int(age.Hours()/24))
	}
	return ""
}
