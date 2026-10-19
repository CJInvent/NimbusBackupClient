//go:build !windows

package main

// The console's tray off Windows: there is none. The console ships for
// Windows only; this exists so its tests build and run on Linux CI, and keeps
// the bound surface (SetTrayLanguage) the same there.

func (c *Console) setupSystemTray() {
	writeInfoLog("System tray is only supported on Windows")
}

func (c *Console) minimizeToTray() {
	writeInfoLog("MinimizeToTray is only supported on Windows")
}

// SetTrayLanguage is a no-op off Windows (no system tray). Kept so the
// Wails binding surface is identical across platforms.
func (c *Console) SetTrayLanguage(lang string) {}

// attemptTrayCleanupBeforeCrash is a no-op off Windows — there is no tray
// icon to clean up. Paired with the real implementation in tray.go.
func attemptTrayCleanupBeforeCrash() {}

func (c *Console) updateStorageTray(status map[string]interface{}) {}
