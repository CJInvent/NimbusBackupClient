package main

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

import "github.com/tizbac/proxmoxbackupclient_go/gui/api"

// ExchangeStatus and ExchangeLogMode are the wire shapes in gui/api
// (machine.go), aliased so the service's answer and the console's reading of
// it are one type.
type (
	ExchangeStatus  = api.ExchangeStatus
	ExchangeLogMode = api.ExchangeLogMode
)

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
