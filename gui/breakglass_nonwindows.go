//go:build !windows

package main

// The break-glass flag is a Windows registry value; there is nothing to read
// on other platforms, so the override is never requested there.
func emergencyFileRestoreRequested() bool { return false }

func unmanagedBackupsOverrideRequested() bool { return false }

// readNimbusString has nothing to read off Windows; the level falls back to
// its default, which is what resolveLogLevel does with an empty string.
func readNimbusString(name string) string { return "" }
