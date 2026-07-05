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
	// LogTruncation mirrors config.ExchangeLogTruncation; HighlightTruncation is
	// true when Exchange is present but post-backup log truncation is off.
	LogTruncation       bool `json:"log_truncation"`
	HighlightTruncation bool `json:"highlight_truncation"`
}

// ExchangeLogMode is the (lazily queried) transaction-log posture, surfaced so
// the GUI can recommend truncation only when logs actually accumulate.
type ExchangeLogMode struct {
	Queried        bool   `json:"queried"`
	LogsAccumulate bool   `json:"logs_accumulate"`
	Detail         string `json:"detail"`
}

// GetExchangeStatus is bound to the frontend and consumable by the control
// server. Detection itself is platform-specific (see exchange_windows.go).
func (a *App) GetExchangeStatus() ExchangeStatus {
	installed, version := detectExchange()
	aware := a.config != nil && a.config.ExchangeAware
	truncate := a.config != nil && a.config.ExchangeLogTruncation
	return ExchangeStatus{
		Installed:           installed,
		Version:             version,
		Aware:               aware,
		HighlightSetting:    installed && !aware,
		LogTruncation:       truncate,
		HighlightTruncation: installed && !truncate,
	}
}

// QueryExchangeLogMode runs the (slow) EMS query for circular-logging state.
// Bound to the frontend and callable by the control server; invoked lazily so
// it never blocks status refresh.
func (a *App) QueryExchangeLogMode() ExchangeLogMode {
	installed, _ := detectExchange()
	if !installed {
		return ExchangeLogMode{}
	}
	queried, accumulate, detail := getExchangeCircularLogging()
	return ExchangeLogMode{Queried: queried, LogsAccumulate: accumulate, Detail: detail}
}

// maybeRunExchangePostBackup runs the app-aware Exchange tasks after a
// successful backup, but only when Exchange is present AND the operator enabled
// awareness. Best-effort and fully logged: an Exchange task failure is recorded
// but never retroactively fails an already-successful backup.
func (a *App) maybeRunExchangePostBackup() {
	if a.config == nil || (!a.config.ExchangeAware && !a.config.ExchangeLogTruncation) {
		return
	}
	installed, version := detectExchange()
	if !installed {
		writeDebugLog("[Exchange] app-aware tasks enabled but no Exchange installation detected - skipping")
		return
	}
	writeDebugLog(fmt.Sprintf("[Exchange] Running post-backup tasks for Exchange %s (health=%v, truncateLogs=%v)",
		version, a.config.ExchangeAware, a.config.ExchangeLogTruncation))
	runExchangePostBackup(version, a.config.ExchangeAware, a.config.ExchangeLogTruncation)
}
