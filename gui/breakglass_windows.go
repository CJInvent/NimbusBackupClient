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
)

func emergencyFileRestoreRequested() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, breakGlassKeyPath,
		registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return false // absent key is the normal case, not an error worth logging
	}
	defer func() { _ = k.Close() }()

	v, _, err := k.GetIntegerValue(breakGlassValueName)
	if err != nil {
		return false
	}
	return v == 1
}
