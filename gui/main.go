//go:build !service
// +build !service

package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	stdruntime "runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailswin "github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"pbscommon"
	"security"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

//go:embed all:frontend/dist
var assets embed.FS

const (
	appName = "Nimbus Backup"
)

var (
	crashReportPath string
)

func init() {

	// Get executable directory for crash reports
	exePath, _ := os.Executable()
	exeDir := filepath.Dir(exePath)
	crashReportPath = filepath.Join(exeDir, "crash_report.txt")

	// Setup panic recovery
	defer func() {
		if r := recover(); r != nil {
			crashMsg := fmt.Sprintf("PANIC during init: %v\n%s", r, debug.Stack())
			writeDebugLog(crashMsg)
			writeCrashReport(crashMsg)
		}
	}()
}

func main() {
	// Route stdlib log into the visible rotating log before anything can
	// log. Matters less here than in the service build (the GUI may have a
	// console) but keeps both entry points on one logging path.
	redirectStdlibLog()

	// Parse command line flags
	minimized := flag.Bool("minimized", false, "Start minimized to system tray")
	logcat := flag.String("logcat", "", "Enable detailed log categories (comma-separated): pbs,chunks,security,api or 'all'")
	flag.Parse()
	if *logcat != "" {
		SetLogCategories(*logcat)
		writeDebugLog("Detailed log categories enabled: " + *logcat)
	}

	// Check for single instance (GUI only)
	// If another instance exists, activate it and exit
	if !CheckSingleInstance() {
		fmt.Println("Another instance is already running. Activating existing window...")
		os.Exit(0)
	}

	// Setup panic recovery for main
	defer func() {
		if r := recover(); r != nil {
			crashMsg := fmt.Sprintf("PANIC in main: %v\n%s", r, debug.Stack())
			writeDebugLog(crashMsg)
			writeCrashReport(crashMsg)
			fmt.Fprint(os.Stderr, "\n!!! APPLICATION CRASHED !!!\nSee crash_report.txt for details\n")
			// Best-effort tray cleanup: an unhandled panic used to skip the
			// tray's own Shell_NotifyIcon(NIM_DELETE) entirely, leaking the
			// icon on every crash (see gui/tray.go's onExit doc comment).
			// No-op off Windows / when no tray was ever set up.
			attemptTrayCleanupBeforeCrash()
			os.Exit(1)
		}
	}()

	// Logging is now handled by RotatingLogger (initialized in logging_gui.go)
	writeDebugLog(fmt.Sprintf("=== %s v%s Starting ===", appName, appVersion))
	writeDebugLog(fmt.Sprintf("Time: %s", time.Now().Format(time.RFC3339)))
	writeDebugLog(fmt.Sprintf("Service log: %s", GetServiceLogPath()))
	writeDebugLog(fmt.Sprintf("Backup log: %s", GetBackupLogPath()))
	writeDebugLog(fmt.Sprintf("Crash report path: %s", crashReportPath))

	// Install SIGINT/SIGTERM handler so any live PBS backup session gets
	// closed before we exit. Without this, a forced kill (e.g. "update
	// and restart") leaves the HTTP/2 connection dangling — PBS keeps
	// the snapshot lock until TCP keepalive reaps it ~16 min later,
	// which blocks the next verify run on that group.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		writeDebugLog(fmt.Sprintf("Signal %v received — closing active PBS sessions", sig))
		pbscommon.CloseAllActive()
		os.Exit(1)
	}()

	// Clean up legacy auto-start from previous versions
	// (Task Scheduler or Registry entries before MSI service)
	CleanupLegacyAutoStart()

	// Create app instance
	app := NewApp()
	writeDebugLog("App instance created")

	// Resolve the WebView2 user-data folder. Prefer Local AppData: it's always
	// provisioned per profile and isn't subject to roaming-profile redirection,
	// which fails for domain accounts whose roaming profile isn't present on the
	// machine (the "couldn't create the data directory" error when elevating as
	// a different user). Pre-create it so WebView2 doesn't fail to initialize and
	// leave the window invisible.
	webviewBase := os.Getenv("LOCALAPPDATA")
	if webviewBase == "" {
		webviewBase = os.Getenv("APPDATA")
	}
	if webviewBase == "" {
		webviewBase = os.TempDir()
	}
	webviewDataDir := filepath.Join(webviewBase, "NimbusBackup", "WebView2")
	// #nosec G703 -- base is the OS-provided LOCALAPPDATA/APPDATA env var (the GUI runs as the user, not the elevated service); path suffix is constant, no user-controlled traversal
	if err := os.MkdirAll(webviewDataDir, 0o755); err != nil {
		writeDebugLog(fmt.Sprintf("WebView2 data dir %s not creatable: %v; falling back to temp", webviewDataDir, err))
		webviewDataDir = filepath.Join(os.TempDir(), "NimbusBackup", "WebView2")
		_ = os.MkdirAll(webviewDataDir, 0o755)
	}
	writeDebugLog(fmt.Sprintf("WebView2 user-data path: %s", webviewDataDir))

	// Create application options
	appOptions := &options.App{
		Title:     fmt.Sprintf("%s v%s", appName, appVersion),
		Width:     1000,
		Height:    700,
		MaxWidth:  1400, // Prevent window from being too large
		MaxHeight: 900,  // Prevent title bar from going off-screen
		MinWidth:  400,  // Allow very small windows for low-res screens
		MinHeight: 300,  // Allow very small windows for low-res screens
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		StartHidden:      *minimized, // Start hidden if --minimized flag is set
		OnStartup:        app.startup,
		OnDomReady:       app.domReady,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
		Windows: &wailswin.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
			WebviewUserDataPath:  webviewDataDir,
		},
	}

	if *minimized {
		writeDebugLog("Starting in minimized mode (hidden to tray)")
	}

	writeDebugLog("Application options configured")

	// Run application
	writeDebugLog("Starting Wails runtime...")
	err := wails.Run(appOptions)

	if err != nil {
		errMsg := fmt.Sprintf("ERROR: Wails.Run failed: %v\nStack trace:\n%s", err, debug.Stack())
		writeDebugLog(errMsg)
		writeCrashReport(errMsg)
		fmt.Fprint(os.Stderr, "\n!!! APPLICATION FAILED TO START !!!\n")
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Check crash_report.txt and %s\n", GetServiceLogPath())
		os.Exit(1)
	}

	writeDebugLog("Application shutdown normally")
}

func writeCrashReport(message string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")

	crashContent := fmt.Sprintf(`=== NIMBUS BACKUP CRASH REPORT ===
Time: %s
Version: %s

%s

=== SYSTEM INFO ===
Service Log: %s
Backup Log: %s

Please report this issue to RDEM Systems:
- Website: https://nimbus.rdem-systems.com
- Include this crash_report.txt file
`, timestamp, appVersion, message, GetServiceLogPath(), GetBackupLogPath())

	// Write to crash report file (overwrite each time)
	err := os.WriteFile(crashReportPath, []byte(crashContent), 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write crash report: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "Crash report written to: %s\n", crashReportPath)
	}
}

// SetProgressCallbacks moved to api_wrappers.go (shared) so the service build
// implements it too — see the comment there.

// startup is called when the app starts
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	writeDebugLog("App.startup() called")
	a.GetSecurityWarnings() // logs posture warnings once at startup

	// Detect execution mode (Service vs Standalone)
	detector := api.NewModeDetector(getAPITokenPath())
	a.mode = detector.DetectMode()
	writeDebugLog(fmt.Sprintf("Execution mode: %s", a.mode.String()))

	// The GUI never schedules, never checks in, and never cleans up after a
	// backup engine, because it no longer HAS one (docs/V4-PIPELINE.md
	// §3.1). All of that belongs to the service, which runs as LocalSystem
	// and is always on; a front end that only sometimes exists is the wrong
	// owner for work that must happen at 02:00 whether or not anyone is
	// logged in.
	//
	// This used to be conditional on standalone mode, and the condition was
	// "the service did not answer a probe" — so a service that was merely
	// slow to start produced a GUI running its own scheduler, its own
	// control-plane check-in loop, and its own VSS cleanup, in parallel with
	// the service doing the same.
	if a.mode != api.ModeService {
		writeDebugLog("Service unreachable at startup - the GUI will show status only until it comes back")
	}

	// Startup jobs are NOT run here. They belong to the service, which
	// starts at boot; running them from the GUI meant they fired at login,
	// or never at all on a server nobody logs into. See service.go.

	// Trim stale restore listing caches in the background (best-effort).
	go func() {
		trimSnapshotTreeCache(30 * 24 * time.Hour)
	}()

	// Setup system tray for background operation
	go a.SetupSystemTray()
}

// domReady is called after front-end resources have been loaded
func (a *App) domReady(ctx context.Context) {
	writeDebugLog("App.domReady() called - UI loaded successfully")
}

// beforeClose is called when the application is about to quit
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	writeDebugLog("App.beforeClose() called - minimizing to tray")
	// Instead of closing, minimize to tray
	a.MinimizeToTray()
	return true // Prevent actual close
}

// shutdown is called at application termination
func (a *App) shutdown(ctx context.Context) {
	writeDebugLog("App.shutdown() called — closing active PBS sessions")
	pbscommon.CloseAllActive()
}

// GetConfig returns the current configuration with secrets stripped (M-04). It is
// Wails-bound, so it must never expose tokens to the frontend; internal callers
// use a.config directly.
func (a *App) GetConfig() *Config {
	writeDebugLog("GetConfig() called from frontend")
	return a.config.sanitized()
}

// GetHostname returns the system hostname
func (a *App) GetHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		writeDebugLog(fmt.Sprintf("GetHostname() error: %v", err))
		return "unknown"
	}
	writeDebugLog(fmt.Sprintf("GetHostname() returned: %s", hostname))
	return hostname
}

// GetSystemInfo returns system information for UI (mode, admin status, etc.)
func (a *App) GetSystemInfo() map[string]interface{} {
	return map[string]interface{}{
		"mode":              a.mode.String(),
		"is_admin":          isAdmin(),
		"hostname":          a.GetHostname(),
		"service_available": a.mode == api.ModeService,
		// os = runtime.GOOS ("windows", "linux", "darwin") — used by the
		// restore UI to enable/disable the in-place mode when the snapshot
		// was taken on a different platform.
		"os": stdruntime.GOOS,
	}
}

// GetConnections backs the panel's connection tiles.
//
// An unreachable service returns an empty picture rather than an error: the
// caller is a poller, and the panel already has a place to say the service is
// not answering without every tile turning into an error toast.
func (a *App) GetConnections() (*api.ConnectionsResponse, error) {
	if a.apiClient == nil {
		return &api.ConnectionsResponse{PBS: []api.PBSConnection{}, ServerTime: time.Now()}, nil
	}
	resp, err := a.apiClient.GetConnections()
	if err != nil {
		return &api.ConnectionsResponse{PBS: []api.PBSConnection{}, ServerTime: time.Now()}, nil
	}
	return resp, nil
}

// IsReadOnly reports whether this console is a status panel.
//
// The front end uses it to OMIT controls rather than disable them, matching
// the portal's no-leak rule: a denied control is not in the markup.
//
// This is presentation only. The control is the service refusing every
// mutating call while locked (gui/api/readonly.go) -- so a front end that
// ignores this, or a modified one that never asks, changes nothing about what
// the agent will actually do.
func (a *App) IsReadOnly() bool {
	return ControlPolicy().GUIReadOnly
}

func (a *App) GetVersion() string {
	writeDebugLog(fmt.Sprintf("GetVersion() returned: %s", appVersion))
	return appVersion
}

// GetActiveRuns reports every backup in flight on this machine, whatever
// started it — the scheduler, a portal command, or this GUI.
//
// This is the binding that lets the GUI show a SCHEDULED backup. Live progress
// otherwise reaches the front end only through Wails events, which are emitted
// in the GUI's own process and so exist only for a run the GUI itself started
// (docs/V4-PIPELINE.md §2.4). Polling the service is the observation path that
// works for every trigger, which is the point: the Start button stops being
// special.
//
// An unreachable service returns an empty list rather than an error. The
// caller is a poller on a timer; surfacing a connection error every three
// seconds while the service restarts would fill the log and the UI with noise
// about a condition the status panel already shows.
// The whole response is returned, not just the list: it carries the SERVICE's
// clock, and elapsed time for a live run has to be measured against that
// rather than against the workstation's. A machine with bad NTP is common
// enough, and the difference shows up directly as a wrong rate and ETA.
func (a *App) GetActiveRuns() (*api.RunsResponse, error) {
	if a.apiClient == nil {
		return &api.RunsResponse{Runs: []api.Run{}, ServerTime: time.Now()}, nil
	}
	resp, err := a.apiClient.GetActiveRuns()
	if err != nil {
		return &api.RunsResponse{Runs: []api.Run{}, ServerTime: time.Now()}, nil
	}
	return resp, nil
}

// GetRecentRuns backs the seven-day panel. Live runs are included, so a
// backup in flight appears at the top of the history rather than only once it
// finishes — which is how the old job-history file behaved, and why a running
// scheduled backup showed up nowhere at all.
func (a *App) GetRecentRuns(days int) (*api.RunsResponse, error) {
	if a.apiClient == nil {
		return &api.RunsResponse{Runs: []api.Run{}, ServerTime: time.Now()}, nil
	}
	resp, err := a.apiClient.GetRecentRuns(days)
	if err != nil {
		return &api.RunsResponse{Runs: []api.Run{}, ServerTime: time.Now()}, nil
	}
	return resp, nil
}

// ListPhysicalDisks returns a list of available physical disks for full-volume
// (machine) backups. Bound to the frontend via Wails so the Backup tab can
// populate its disk picker. Returns an error on non-Windows builds.
func (a *App) ListPhysicalDisks() ([]PhysicalDiskInfo, error) {
	writeDebugLog("ListPhysicalDisks() called from frontend")
	disks, err := ListPhysicalDisks()
	if err != nil {
		writeDebugLog(fmt.Sprintf("ListPhysicalDisks() error: %v", err))
		return nil, err
	}
	writeDebugLog(fmt.Sprintf("Found %d physical disks", len(disks)))
	return disks, nil
}

// GetConfigWithHostname returns config with hostname pre-filled
func (a *App) GetConfigWithHostname() map[string]interface{} {
	hostname := a.GetHostname()
	cfg := a.config

	// Return config as map with hostname
	result := map[string]interface{}{
		"baseurl":         cfg.BaseURL,
		"certfingerprint": cfg.CertFingerprint,
		"authid":          cfg.AuthID,
		// M-04: never hand the PBS token to the webview/frontend. Expose only
		// whether one is stored; SaveConfig keeps the existing secret when the
		// frontend submits an empty value, and TestConnection falls back to it.
		"secret":                  "",
		"secret_set":              cfg.Secret != "",
		"datastore":               cfg.Datastore,
		"namespace":               cfg.Namespace,
		"backupdir":               cfg.BackupDir,
		"backup-id":               cfg.BackupID,
		"usevss":                  cfg.UseVSS,
		"upload_limit_mbps":       cfg.UploadLimitMbps,
		"control_server_url":      cfg.ControlServerURL,
		"control_enrolled":        cfg.ControlAgentID > 0,
		"exchange_aware":          cfg.ExchangeAware,
		"exchange_log_truncation": cfg.ExchangeLogTruncation,
		"hostname":                hostname,
	}

	// Pre-fill backup-id with hostname if empty
	if cfg.BackupID == "" {
		result["backup-id"] = hostname
	}

	return result
}

// DiagnoseConfig returns config validation status for debugging
func (a *App) DiagnoseConfig() map[string]interface{} {
	cfg := a.config

	var validationError string
	if err := cfg.Validate(); err != nil {
		validationError = err.Error()
	}

	configPath, _ := getConfigPath()

	return map[string]interface{}{
		"config_path":      configPath,
		"baseurl_set":      cfg.BaseURL != "",
		"baseurl_value":    security.SanitizeURL(cfg.BaseURL),
		"authid_set":       cfg.AuthID != "",
		"datastore_set":    cfg.Datastore != "",
		"validation_ok":    validationError == "",
		"validation_error": validationError,
		"mode":             a.mode.String(),
	}
}

// GetControlServerStatus returns the control-plane connectivity snapshot for
// the GUI status card. In service mode the service (which owns the check-in
// loop) is authoritative; standalone answers locally.
func (a *App) GetControlServerStatus() map[string]interface{} {
	if !a.isServiceProcess && a.mode == api.ModeService && a.apiClient != nil {
		if st, err := a.apiClient.GetControlPlaneStatus(); err == nil {
			return st
		}
		// Service unreachable: fall through to the local (config-only) view
		// so the card still shows what is configured.
	}
	return a.ControlPlaneStatusMap()
}

// SaveControlServerConfig applies control-server settings from the GUI.
// Empty enroll token keeps the stored one; changing the URL forgets the old
// identity (see SaveControlPlaneFromMap).
func (a *App) SaveControlServerConfig(serverURL, enrollToken, certFP string) error {
	m := map[string]interface{}{
		"control_server_url":   serverURL,
		"control_enroll_token": enrollToken,
		"control_cert_fp":      certFP,
	}
	if a.delegateConfigWrites() {
		if err := a.apiClient.SaveControlPlane(m); err != nil {
			return err
		}
		a.ReloadConfig()
		return nil
	}
	return a.SaveControlPlaneFromMap(m)
}

// SaveConfig saves the configuration
func (a *App) SaveConfig(config *Config) error {
	// Log sanitized config (no secrets)
	writeDebugLog(fmt.Sprintf("SaveConfig() called: URL=%s, AuthID=%s, Datastore=%s, BackupID=%s",
		security.SanitizeURL(config.BaseURL),
		config.AuthID,
		config.Datastore,
		config.BackupID))

	// Delegate to the privileged service when present so it stays the single
	// writer of config.json. The frontend sends empty secrets to mean "keep the
	// stored one"; the service preserves them, so secrets never leave the service.
	if a.delegateConfigWrites() {
		m, err := toMap(config)
		if err != nil {
			return err
		}
		if err := a.apiClient.SaveConfig(m); err != nil {
			writeDebugLog(fmt.Sprintf("SaveConfig: service-side save failed: %v", err))
			return err
		}
		a.ReloadConfig()
		return nil
	}

	// Standalone (no service): keep stored secrets, validate, and write directly.
	// M-04: an empty value means "keep the existing one", not "clear it".
	if a.config != nil {
		if config.Secret == "" {
			config.Secret = a.config.Secret
		}
	}

	// Validate before saving
	if err := config.Validate(); err != nil {
		writeDebugLog(fmt.Sprintf("Config validation failed: %v", err))
		return err
	}

	// Save to disk
	if err := config.Save(); err != nil {
		writeDebugLog(fmt.Sprintf("Config save to disk failed: %v", err))
		return err
	}

	// Update in-memory config
	a.config = config
	writeDebugLog("Config saved successfully and loaded into app")
	return nil
}

// TestConnection tests the PBS connection with the provided config (or current if nil)
func (a *App) TestConnection(config *Config) error {
	writeDebugLog("TestConnection() called")

	// Use provided config or fallback to current app config
	testConfig := config
	if testConfig == nil {
		testConfig = a.config
	}

	// M-04: the frontend no longer holds the secret, so an empty secret in the
	// submitted config means "use the stored one" (test the existing connection).
	if testConfig != nil && testConfig.Secret == "" && a.config != nil {
		testConfig.Secret = a.config.Secret
	}

	// Validate config first
	if err := testConfig.Validate(); err != nil {
		return err
	}

	// Create PBS client
	client := &pbscommon.PBSClient{
		BaseURL:          testConfig.BaseURL,
		CertFingerPrint:  testConfig.CertFingerprint,
		AuthID:           testConfig.AuthID,
		Secret:           testConfig.Secret,
		Datastore:        testConfig.Datastore,
		Namespace:        testConfig.Namespace,
		Insecure:         testConfig.CertFingerprint != "",
		CompressionLevel: pbscommon.CompressionFastest, // Default for test connections
		Manifest: pbscommon.BackupManifest{
			BackupID: testConfig.BackupID,
		},
	}

	// Debug log with sanitized credentials
	writeDebugLog(fmt.Sprintf("Testing connection: URL=%s, AuthID=%s, Secret=%s, Datastore=%s",
		security.SanitizeURL(testConfig.BaseURL),
		testConfig.AuthID,
		security.SanitizeSecret(testConfig.Secret),
		testConfig.Datastore))

	// Perform real HTTP test (checks DNS, connectivity, auth, datastore access)
	if err := client.TestConnection(); err != nil {
		writeDebugLog(fmt.Sprintf("Connection test failed: %v", err))
		return err
	}

	writeDebugLog("Connection test successful (authenticated + datastore accessible)")
	return nil
}

// GetLastBackupDirs returns the last used backup directories
func (a *App) GetLastBackupDirs() []string {
	writeDebugLog(fmt.Sprintf("GetLastBackupDirs() returned %d directories", len(a.config.LastBackupDirs)))
	return a.config.LastBackupDirs
}

// ReloadConfig reloads configuration from disk (for service when config changes)
func (a *App) ReloadConfig() {
	newConfig := LoadConfig()
	a.config = newConfig
	writeDebugLog("Config reloaded from disk")
}

// ==================== MULTI-PBS MANAGEMENT ====================

// ListPBSServers returns all configured PBS servers
func (a *App) ListPBSServers() []*PBSServer {
	servers := a.config.ListPBSServers()
	writeDebugLog(fmt.Sprintf("ListPBSServers() returned %d servers", len(servers)))
	// M-04: never hand PBS tokens to the frontend — return sanitized copies.
	out := make([]*PBSServer, 0, len(servers))
	for _, s := range servers {
		out = append(out, s.sanitized())
	}
	return out
}

// GetPBSServer returns a single PBS server by ID (secret stripped — M-04).
func (a *App) GetPBSServer(id string) (*PBSServer, error) {
	writeDebugLog(fmt.Sprintf("GetPBSServer(%s) called", id))
	s, err := a.config.GetPBSServer(id)
	if err != nil {
		return nil, err
	}
	return s.sanitized(), nil
}

// delegateConfigWrites reports whether this process should route config writes
// through the privileged service instead of writing config.json directly. True
// only in the GUI process when a service is available; the service process and
// standalone GUIs write locally. Config.json under ProgramData is owned by
// whichever process wrote it first, so an unprivileged GUI cannot overwrite a
// service-owned file — delegating keeps a single privileged writer.
func (a *App) delegateConfigWrites() bool {
	return !a.isServiceProcess && a.mode == api.ModeService && a.apiClient != nil
}

// toMap json-round-trips a value into a generic map for delegation over the local
// API (the api package cannot import the main package's config types).
func toMap(v interface{}) (map[string]interface{}, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("failed to encode for service delegation: %w", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("failed to encode for service delegation: %w", err)
	}
	return m, nil
}

// AddPBSServer adds a new PBS server to the configuration
func (a *App) AddPBSServer(pbs *PBSServer) error {
	writeDebugLog(fmt.Sprintf("AddPBSServer(%s) called", pbs.ID))
	if a.delegateConfigWrites() {
		m, err := toMap(pbs)
		if err != nil {
			return err
		}
		if err := a.apiClient.SavePBSServer(m); err != nil {
			writeDebugLog(fmt.Sprintf("AddPBSServer: service-side save failed: %v", err))
			return err
		}
		a.ReloadConfig()
		return nil
	}
	return a.config.AddPBSServer(pbs)
}

// UpdatePBSServer updates an existing PBS server
func (a *App) UpdatePBSServer(pbs *PBSServer) error {
	writeDebugLog(fmt.Sprintf("UpdatePBSServer(%s) called", pbs.ID))
	if a.delegateConfigWrites() {
		// The service performs the "empty secret means keep stored one" merge
		// against its authoritative config, so secrets stay service-side.
		m, err := toMap(pbs)
		if err != nil {
			return err
		}
		if err := a.apiClient.SavePBSServer(m); err != nil {
			writeDebugLog(fmt.Sprintf("UpdatePBSServer: service-side save failed: %v", err))
			return err
		}
		a.ReloadConfig()
		return nil
	}
	// M-04: the frontend never receives the token (sanitized), so an empty secret
	// on update means "keep the stored one", not "clear it".
	if pbs.Secret == "" {
		if existing, err := a.config.GetPBSServer(pbs.ID); err == nil && existing != nil {
			pbs.Secret = existing.Secret
		}
	}
	return a.config.UpdatePBSServer(pbs)
}

// DeletePBSServer removes a PBS server
func (a *App) DeletePBSServer(id string) error {
	writeDebugLog(fmt.Sprintf("DeletePBSServer(%s) called", id))
	if a.delegateConfigWrites() {
		if err := a.apiClient.DeletePBSServer(id); err != nil {
			writeDebugLog(fmt.Sprintf("DeletePBSServer: service-side delete failed: %v", err))
			return err
		}
		a.ReloadConfig()
		return nil
	}
	return a.config.DeletePBSServer(id)
}

// SetDefaultPBSServer sets the default PBS server
func (a *App) SetDefaultPBSServer(id string) error {
	writeDebugLog(fmt.Sprintf("SetDefaultPBSServer(%s) called", id))
	if a.delegateConfigWrites() {
		if err := a.apiClient.SetDefaultPBS(id); err != nil {
			writeDebugLog(fmt.Sprintf("SetDefaultPBSServer: service-side set failed: %v", err))
			return err
		}
		a.ReloadConfig()
		return nil
	}
	return a.config.SetDefaultPBS(id)
}

// GetDefaultPBSID returns the default PBS server ID
func (a *App) GetDefaultPBSID() string {
	return a.config.DefaultPBSID
}

// TestPBSConnection tests connection to a specific PBS server
func (a *App) TestPBSConnection(pbsID string) error {
	writeDebugLog(fmt.Sprintf("TestPBSConnection(%s) called", pbsID))

	pbs, err := a.config.GetPBSServer(pbsID)
	if err != nil {
		return err
	}

	// Convert to legacy Config format for existing TestConnection logic
	legacyConfig := pbs.ToConfig()
	return a.TestConnection(legacyConfig)
}

// GetServerFingerprint connects to baseURL and returns the server certificate's
// SHA-256 fingerprint (AA:BB:... uppercase) so the UI can offer trust-on-first-use
// pinning when a self-signed PBS rejects CA validation (audit H-02). Discovery
// only: no token is sent.
func (a *App) GetServerFingerprint(baseURL string) (string, error) {
	writeDebugLog(fmt.Sprintf("GetServerFingerprint(%s) called", security.SanitizeURL(baseURL)))
	fp, err := pbscommon.FetchServerFingerprint(baseURL)
	if err != nil {
		writeDebugLog(fmt.Sprintf("GetServerFingerprint failed: %v", err))
		return "", err
	}
	writeDebugLog(fmt.Sprintf("GetServerFingerprint discovered: %s", fp))
	return fp, nil
}

// PinPBSServerFingerprint stores fingerprint on the PBS server identified by id,
// resolving the secret server-side so the frontend (which never holds the token,
// M-04) can pin a discovered fingerprint without round-tripping credentials.
func (a *App) PinPBSServerFingerprint(id, fingerprint string) error {
	writeDebugLog(fmt.Sprintf("PinPBSServerFingerprint(%s) called", id))
	if err := security.ValidateFingerprint(fingerprint); err != nil {
		return fmt.Errorf("empreinte certificat invalide: %w", err)
	}
	// config.json lives under ProgramData and is owned by whichever process wrote it
	// first. When the privileged service is running it owns the file, so the
	// unprivileged GUI cannot overwrite it: its Save() rename fails and TOFU pinning
	// silently never persists (the connection test keeps reporting offline). Route the
	// write through the service in that case so a single privileged writer owns the
	// file; standalone GUIs (no service) write directly as before.
	if !a.isServiceProcess && a.mode == api.ModeService && a.apiClient != nil {
		writeDebugLog(fmt.Sprintf("PinPBSServerFingerprint(%s): delegating write to service", id))
		if err := a.apiClient.PinFingerprint(id, fingerprint); err != nil {
			writeDebugLog(fmt.Sprintf("PinPBSServerFingerprint: service-side pin failed: %v", err))
			return err
		}
		// Refresh our in-memory copy so a follow-up TestPBSConnection in this process
		// sees the fingerprint the service just wrote to disk.
		a.ReloadConfig()
		return nil
	}
	return a.pinFingerprintLocal(id, fingerprint)
}

// ==================== END MULTI-PBS MANAGEMENT ====================

// emitAnalysisProgress forwards split size-analysis progress to the GUI as an
// "analysis:progress" event (done/total folders sized + bytes so far). Used only
// on the explicit-split path so a multi-minute scan of a large volume shows
// movement instead of a frozen spinner.
func (a *App) emitAnalysisProgress(done, total int, scannedBytes uint64) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "analysis:progress", map[string]interface{}{
		"done":  done,
		"total": total,
		"bytes": scannedBytes,
	})
}

// StartBackup asks the SERVICE to run a backup. It is the only thing this
// binding does.
//
// There is no longer a direct path to fall back to: the GUI build links no
// backup engine (docs/V4-PIPELINE.md §3.1, CJ's decision of 2026-08-05). The
// escape hatch for a machine with no control plane is a service-side toggle,
// not a GUI capability, precisely so that a modified front end cannot reach
// one.
func (a *App) StartBackup(backupType string, backupDirs []string, driveLetters []string, excludeList []string, backupID string, useVSS bool, compression string) error {
	writeDebugLog(fmt.Sprintf("StartBackup() called - VSS: %v, compression: %s", useVSS, compression))

	// The service may have started after the GUI did. Re-probing here rather
	// than trusting startup detection is the difference between "press the
	// button again in a minute" and "restart the application".
	if a.mode != api.ModeService && a.apiClient.IsServiceAvailable() {
		writeDebugLog("[Mode] Service now reachable")
		a.mode = api.ModeService
	}
	if a.mode != api.ModeService {
		return errors.New(errServiceUnavailable)
	}

	return a.startBackupViaService(backupType, backupDirs, driveLetters, excludeList, backupID, useVSS, compression)
}

// StopBackup asks the service to cancel the running backup. The engine aborts
// before the PBS index is committed (so the partial backup is discarded) and
// its deferred VSS Release deletes the shadow copy and symlink — no scraps,
// wherever the stream was.
//
// Always over the local API: the backup is always in the service.
func (a *App) StopBackup() error {
	if a.mode != api.ModeService {
		return errors.New(errServiceUnavailable)
	}
	return a.apiClient.CancelBackup()
}

// startBackupViaService sends backup request to the service via HTTP API
func (a *App) startBackupViaService(backupType string, backupDirs []string, driveLetters []string, excludeList []string, backupID string, useVSS bool, compression string) error {
	writeDebugLog("[Service Mode] Sending backup request to service")

	req := &api.BackupRequest{
		BackupType:   backupType,
		BackupID:     backupID,
		BackupDirs:   backupDirs,
		DriveLetters: driveLetters,
		ExcludeList:  excludeList,
		UseVSS:       useVSS,
		Compression:  compression,
	}

	resp, err := a.apiClient.StartBackup(req)
	if err != nil {
		writeDebugLog(fmt.Sprintf("[Service Mode] Backup request failed: %v", err))
		return fmt.Errorf("%s :: %v", errServiceComm, err)
	}

	writeDebugLog(fmt.Sprintf("[Service Mode] Backup started: %s (JobID: %s)", resp.Message, resp.JobID))

	// No poller is started here. The front end polls /runs/active every
	// three seconds for EVERY run, whatever started it, so a second
	// per-job poller relaying the same numbers through Wails events would
	// be a parallel observation path to keep in step — the exact shape of
	// the problem docs/V4-PIPELINE.md exists to remove.

	return nil
}

// ==================== RESTORE ====================

// ListSnapshots lists available snapshots on a PBS server, optionally filtered
// by backup ID (partial match supports split backups).
//
// pbsID selects the PBS server. Empty means "use the default server" — kept
// for backward compatibility with the legacy single-PBS UI.
func (a *App) ListSnapshots(pbsID, backupID string) ([]map[string]interface{}, error) {
	writeDebugLog(fmt.Sprintf("ListSnapshots(pbs=%s, backupID=%s)", pbsID, backupID))

	cfg, err := a.resolveRestorePBS(pbsID)
	if err != nil {
		return nil, err
	}

	snaps, err := ListSnapshotsInline(cfg.BaseURL, cfg.AuthID, cfg.Secret,
		cfg.Datastore, cfg.Namespace, cfg.CertFingerprint, backupID)
	if err != nil {
		writeDebugLog(fmt.Sprintf("ListSnapshotsInline failed: %v", err))
		return nil, fmt.Errorf("%s :: %v", errSnapshotList, err)
	}

	result := make([]map[string]interface{}, 0, len(snaps))
	for _, s := range snaps {
		result = append(result, map[string]interface{}{
			"id":          s.BackupTime.UTC().Format("2006-01-02T15:04:05Z"),
			"backup_id":   s.BackupID,
			"backup_type": s.BackupType,
			"time":        s.BackupTime.Format("2006-01-02 15:04:05"),
			"unix":        s.BackupTime.Unix(),
			"files":       s.Files,
		})
	}
	writeDebugLog(fmt.Sprintf("Returning %d snapshots", len(result)))
	return result, nil
}

// ListSnapshotContents downloads a snapshot's PXAR archive and returns its
// flat tree of entries. The frontend turns this into a navigable view so the
// user can pick individual files or directories before restoring.
//
// snapshotUnix is the snapshot's backup-time as Unix seconds (the `unix` field
// returned by ListSnapshots). Set forceRefresh to bypass the local listing
// cache — useful for a manual "Reload" action.
func (a *App) ListSnapshotContents(pbsID, backupID string, snapshotUnix int64, forceRefresh bool) ([]SnapshotEntry, error) {
	writeDebugLog(fmt.Sprintf("ListSnapshotContents(pbs=%s, backupID=%s, unix=%d, force=%v)",
		pbsID, backupID, snapshotUnix, forceRefresh))

	cfg, err := a.resolveRestorePBS(pbsID)
	if err != nil {
		return nil, err
	}
	if backupID == "" {
		return nil, errors.New(errBackupIDRequired)
	}

	opts := RestoreOptions{
		BaseURL:         cfg.BaseURL,
		AuthID:          cfg.AuthID,
		Secret:          cfg.Secret,
		Datastore:       cfg.Datastore,
		Namespace:       cfg.Namespace,
		CertFingerprint: cfg.CertFingerprint,
		BackupID:        backupID,
		SnapshotTime:    time.Unix(snapshotUnix, 0),
	}
	return ListSnapshotContentsInline(opts, "", forceRefresh)
}

// GetSnapshotMeta returns the `.nimbus_backup_meta.json` sidecar from a
// snapshot. Returns nil (not an error) when the snapshot predates the sidecar
// — the frontend should fall back to a generic banner in that case.
//
// Cheap when the snapshot has already been listed: the meta is bundled in the
// same restore-cache envelope as the entries.
func (a *App) GetSnapshotMeta(pbsID, backupID string, snapshotUnix int64) (*BackupMeta, error) {
	writeDebugLog(fmt.Sprintf("GetSnapshotMeta(pbs=%s, backupID=%s, unix=%d)",
		pbsID, backupID, snapshotUnix))

	cfg, err := a.resolveRestorePBS(pbsID)
	if err != nil {
		return nil, err
	}
	if backupID == "" {
		return nil, errors.New(errBackupIDRequired)
	}

	opts := RestoreOptions{
		BaseURL:         cfg.BaseURL,
		AuthID:          cfg.AuthID,
		Secret:          cfg.Secret,
		Datastore:       cfg.Datastore,
		Namespace:       cfg.Namespace,
		CertFingerprint: cfg.CertFingerprint,
		BackupID:        backupID,
		SnapshotTime:    time.Unix(snapshotUnix, 0),
	}
	return ReadSnapshotMetaInline(opts, false)
}

// RestoreSnapshot extracts a snapshot (or selected files) according to mode.
//
//   - mode "original": restore in-place to the path captured in the snapshot's
//     .nimbus_backup_meta.json sidecar. destPath is ignored. Cross-host
//     attempts are refused unless allowCrossHost is true.
//   - mode "alternate_abs" (or empty): write to destPath, preserving the full
//     archive directory layout below it.
//   - mode "alternate_flat": write to destPath stripping the longest common
//     prefix of the selection — useful for restoring a single file as
//     destPath/<basename>.
//
// includePaths uses archive-style paths (forward slash). When empty the entire
// snapshot is restored. The ACL/ADS/timestamps flags are accepted today but
// only timestamps is effective — the per-file NTFS sidecar required for the
// other two is still on the roadmap.
//
// Progress is streamed to the frontend via the "restore:progress" event;
// completion via "restore:complete".
func (a *App) RestoreSnapshot(pbsID, backupID, snapshotID, destPath, mode string,
	includePaths []string, allowCrossHost, restoreACLs, restoreADS, restoreTimestamps, overwrite bool) error {
	writeDebugLog(fmt.Sprintf("RestoreSnapshot(pbs=%s, backupID=%s, snap=%s, mode=%s, dest=%s, includes=%d, crossHost=%v, acl=%v, ads=%v, ts=%v, overwrite=%v)",
		pbsID, backupID, snapshotID, mode, destPath, len(includePaths), allowCrossHost, restoreACLs, restoreADS, restoreTimestamps, overwrite))

	cfg, err := a.resolveRestorePBS(pbsID)
	if err != nil {
		return err
	}
	if backupID == "" {
		return errors.New(errBackupIDRequired)
	}
	if snapshotID == "" {
		return errors.New(errSnapshotIDRequired)
	}

	restoreMode := RestoreMode(mode)
	if restoreMode == "" {
		restoreMode = RestoreModeAlternateAbs
	}

	// Destination is only required + validated for alternate modes. In-place
	// derives the target from the backup metadata sidecar.
	if restoreMode != RestoreModeOriginal {
		if destPath == "" {
			return errors.New(errDestPathRequired)
		}
		if err := security.ValidatePath(destPath); err != nil {
			return fmt.Errorf("chemin de destination invalide: %w", err)
		}
	}

	timestamp, err := time.Parse("2006-01-02T15:04:05Z", snapshotID)
	if err != nil {
		return fmt.Errorf("ID de snapshot invalide: %v", err)
	}

	// The restore progress convention is 0-100 across the whole app (download
	// and image paths already emit 0-100). Directory restore's OnProgress is
	// historically 0-1, so scale it here at the single boundary rather than
	// touching every progress call inside restore_inline.go.
	emit := func(percent float64, message string) {
		if a.ctx == nil {
			return
		}
		runtime.EventsEmit(a.ctx, "restore:progress", map[string]interface{}{
			"percent": percent * 100,
			"message": message,
		})
	}

	opts := RestoreOptions{
		BaseURL:           cfg.BaseURL,
		AuthID:            cfg.AuthID,
		Secret:            cfg.Secret,
		Datastore:         cfg.Datastore,
		Namespace:         cfg.Namespace,
		CertFingerprint:   cfg.CertFingerprint,
		BackupID:          backupID,
		SnapshotTime:      timestamp,
		DestPath:          destPath,
		Mode:              restoreMode,
		AllowCrossHost:    allowCrossHost,
		IncludePaths:      includePaths,
		Overwrite:         overwrite,
		RestoreACLs:       restoreACLs,
		RestoreADS:        restoreADS,
		RestoreTimestamps: restoreTimestamps,
		OnProgress:        emit,
	}

	go func() {
		// A restore can fail in surprising ways (corrupt archive, disk full).
		// Recover so a panic surfaces as an error in the UI instead of taking
		// the whole GUI process down.
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("restore panic: %v", r)
					writeDebugLog(fmt.Sprintf("CRITICAL: restore panic: %v\n%s", r, debug.Stack()))
				}
			}()
			err = RestoreSnapshotInline(opts)
		}()
		success := err == nil
		msg := "Restore completed"
		if err != nil {
			msg = err.Error()
			writeDebugLog(fmt.Sprintf("Restore failed: %v", err))
		}
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "restore:complete", map[string]interface{}{
				"success": success,
				"message": msg,
			})
		}
	}()
	return nil
}

// OpenRestoreDestDialog opens a native folder picker so the user can choose
// where to restore files. Returns "" if the dialog was cancelled.
//
// Hardened against a reported crash on the client: the native Windows folder
// picker (IFileDialog) can fault when handed an empty/invalid initial folder,
// so we seed DefaultDirectory with a path we know exists. A recover() turns any
// Go-level panic into an error instead of taking the process down, and the
// surrounding logging makes the next failure diagnosable from the debug log.
// OpenRestoreDestDialog is RETIRED — see OpenSaveFileDialog in download.go.
// The native folder picker faults natively (uncatchable) and took the whole
// process down. The in-app picker (pathpicker.go) replaces it.
func (a *App) OpenRestoreDestDialog() (string, error) {
	return "", errors.New("[NB-3009] the native folder picker is disabled — use the in-app picker")
}

// SearchFiles scans every backup-id matching hostPrefix over the given period
// for entries matching query, and returns the matches. mode is one of "name"
// (substring on file name), "regex", or "path" (substring on full path).
//
// fromUnix/toUnix bound the snapshot period in Unix seconds; pass 0 for an
// open end. When assembleMissing is true, snapshots not already in the local
// listing cache are downloaded + assembled (slow, needs temp space); otherwise
// only cached snapshots are searched. Progress is streamed via "search:progress".
func (a *App) SearchFiles(pbsID, hostPrefix, query, mode string, fromUnix, toUnix int64, assembleMissing bool) (*SearchResult, error) {
	writeDebugLog(fmt.Sprintf("SearchFiles(pbs=%s, prefix=%s, query=%q, mode=%s, from=%d, to=%d, assemble=%v)",
		pbsID, hostPrefix, query, mode, fromUnix, toUnix, assembleMissing))

	cfg, err := a.resolveRestorePBS(pbsID)
	if err != nil {
		return nil, err
	}

	var from, to time.Time
	if fromUnix > 0 {
		from = time.Unix(fromUnix, 0)
	}
	if toUnix > 0 {
		to = time.Unix(toUnix, 0)
	}

	emit := func(percent float64, message string) {
		if a.ctx == nil {
			return
		}
		runtime.EventsEmit(a.ctx, "search:progress", map[string]interface{}{
			"percent": percent,
			"message": message,
		})
	}

	opts := SearchOptions{
		BaseURL:         cfg.BaseURL,
		AuthID:          cfg.AuthID,
		Secret:          cfg.Secret,
		Datastore:       cfg.Datastore,
		Namespace:       cfg.Namespace,
		CertFingerprint: cfg.CertFingerprint,
		HostPrefix:      hostPrefix,
		Query:           query,
		Mode:            SearchMatchMode(mode),
		From:            from,
		To:              to,
		AssembleMissing: assembleMissing,
		OnProgress:      emit,
	}
	return SearchFilesInline(opts)
}

// CancelSearch asks an in-flight SearchFiles to stop at the next snapshot
// boundary. The call returning does not mean the search has stopped yet — the
// search returns its partial result with Cancelled=true.
func (a *App) CancelSearch() {
	writeDebugLog("CancelSearch requested")
	CancelFileSearch()
}
