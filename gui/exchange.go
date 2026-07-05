package main

import "fmt"

// Application-aware Exchange support.
//
// Detection is decoupled from action so the state is a clean signal the future
// control server can read and toggle server-side:
//
//	config.ExchangeAware  - bool the operator/control server sets to enable the
//	                        post-backup Exchange tasks.
//	GetExchangeStatus()   - reports {installed, version, aware}; the GUI uses it
//	                        to highlight the setting when Exchange is present but
//	                        not yet enabled ("detected but off").
//
// When enabled, runExchangePostBackup runs AFTER a successful backup. Its job
// is NOT to back Exchange up (the VSS snapshot already captured the databases
// application-consistently via the Exchange writer) but to run the post-backup
// housekeeping an app-aware agent is expected to do - primarily transaction-log
// truncation confirmation and cleanup - and to log every command outcome.
//
// All Exchange versions are supported: detection enumerates the version-keyed
// registry hive (2010=14, 2013=15, 2016=15, 2019=15) and the action layer uses
// the Exchange Management Shell, which is version-independent.

// ExchangeStatus is the detection result surfaced to the GUI/control server.
type ExchangeStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"` // e.g. "2019", "2016", or "" when absent
	Aware     bool   `json:"aware"`   // config.ExchangeAware
	// HighlightSetting is true when Exchange is present but app-aware mode is
	// off - the GUI uses it to draw attention to the toggle.
	HighlightSetting bool `json:"highlight_setting"`
}

// GetExchangeStatus is bound to the frontend and consumable by the control
// server. Detection itself is platform-specific (see exchange_windows.go).
func (a *App) GetExchangeStatus() ExchangeStatus {
	installed, version := detectExchange()
	aware := a.config != nil && a.config.ExchangeAware
	return ExchangeStatus{
		Installed:        installed,
		Version:          version,
		Aware:            aware,
		HighlightSetting: installed && !aware,
	}
}

// maybeRunExchangePostBackup runs the app-aware Exchange tasks after a
// successful backup, but only when Exchange is present AND the operator enabled
// awareness. Best-effort and fully logged: an Exchange task failure is recorded
// but never retroactively fails an already-successful backup.
func (a *App) maybeRunExchangePostBackup() {
	if a.config == nil || !a.config.ExchangeAware {
		return
	}
	installed, version := detectExchange()
	if !installed {
		writeDebugLog("[Exchange] app-aware mode enabled but no Exchange installation detected - skipping post-backup tasks")
		return
	}
	writeDebugLog(fmt.Sprintf("[Exchange] Running post-backup tasks for Exchange %s", version))
	runExchangePostBackup(version)
}
