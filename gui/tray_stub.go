//go:build !windows && !service
// +build !windows,!service

package main

// The console's tray off Windows: there is none. These keep the console's
// methods the same on every platform the console compiles for.

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
