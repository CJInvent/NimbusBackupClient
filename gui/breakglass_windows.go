//go:build windows

package main

// Reads the local break-glass flag. Deliberately HKLM and not HKCU: setting
// it must require Administrator, or the "emergency override" is just an
// override any logged-in user can flip. See controlplane/breakglass.go for
// when the flag is actually honoured — being set is necessary but not
// sufficient.
//
//	reg add HKLM\SOFTWARE\NimbusBackup /v EmergencyFileRestore /t REG_DWORD /d 1 /f

import "golang.org/x/sys/windows/registry"

const (
	breakGlassKeyPath   = `SOFTWARE\NimbusBackup`
	breakGlassValueName = "EmergencyFileRestore"

	// The service-side escape hatch (CJ, 2026-08-05): when org policy
	// restricts this machine to control-plane-sent work only, this lets an
	// administrator take a backup anyway WHILE THE SERVER IS UNREACHABLE.
	// Read by the service, never by the GUI — the GUI has no engine to
	// enable. See controlplane/unmanaged.go for when it is honoured.
	//
	//	reg add HKLM\SOFTWARE\NimbusBackup /v AllowUnmanagedBackups /t REG_DWORD /d 1 /f
	unmanagedBackupsValueName = "AllowUnmanagedBackups"
)

func unmanagedBackupsOverrideRequested() bool {
	return readNimbusFlag(unmanagedBackupsValueName)
}

func emergencyFileRestoreRequested() bool {
	return readNimbusFlag(breakGlassValueName)
}

// readNimbusFlag reads one admin-only DWORD under HKLM\SOFTWARE\NimbusBackup.
//
// WOW64_64KEY because the agent may be a 32-bit process on a 64-bit host, and
// without it the read silently lands in the WOW6432Node view — where the
// administrator who typed the documented `reg add` did not put the value, so
// the override would appear unset with no error to explain it.
// readNimbusString reads one string value under HKLM\SOFTWARE\NimbusBackup.
//
// Beside readNimbusFlag rather than in the logging module: one place knows
// where Nimbus keeps its machine-scope settings, and WOW64_64KEY is easy to
// forget in a second copy — without it a 32-bit agent on 64-bit Windows reads
// WOW6432Node, where the administrator who followed the documentation did not
// write the value.
func readNimbusString(name string) string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, breakGlassKeyPath,
		registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer func() { _ = k.Close() }()

	v, _, err := k.GetStringValue(name)
	if err != nil {
		return ""
	}
	return v
}

func readNimbusFlag(name string) bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, breakGlassKeyPath,
		registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return false // absent key is the normal case, not an error worth logging
	}
	defer func() { _ = k.Close() }()

	v, _, err := k.GetIntegerValue(name)
	if err != nil {
		return false
	}
	return v == 1
}
