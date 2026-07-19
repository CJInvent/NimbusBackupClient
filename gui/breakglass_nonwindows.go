//go:build !windows

package main

// The break-glass flag is a Windows registry value; there is nothing to read
// on other platforms, so the override is never requested there.
func emergencyFileRestoreRequested() bool { return false }
