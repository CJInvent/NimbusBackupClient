//go:build !windows
// +build !windows

package main

// SetupSystemTray is not supported on non-Windows platforms yet
func (a *App) SetupSystemTray() {
	writeInfoLog("System tray is only supported on Windows")
}

// MinimizeToTray is not supported on non-Windows platforms yet
func (a *App) MinimizeToTray() {
	writeInfoLog("MinimizeToTray is only supported on Windows")
}

// ShowFromTray is not supported on non-Windows platforms yet
func (a *App) ShowFromTray() {
	writeInfoLog("ShowFromTray is only supported on Windows")
}

// UpdateTrayTooltip is not supported on non-Windows platforms yet
func (a *App) UpdateTrayTooltip(message string) {
	writeInfoLog("UpdateTrayTooltip is only supported on Windows")
}

// SetTrayLanguage is a no-op off Windows (no system tray). Kept so the
// Wails binding surface is identical across platforms.
func (a *App) SetTrayLanguage(lang string) {}

// attemptTrayCleanupBeforeCrash is a no-op off Windows — there is no tray
// icon to clean up. Paired with the real implementation in tray.go.
func attemptTrayCleanupBeforeCrash() {}

func (a *App) updateStorageTray(status map[string]interface{}) {}
