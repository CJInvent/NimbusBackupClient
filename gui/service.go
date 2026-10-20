package main

import (
	"fmt"
	"net"
	"time"

	"controlplane"
	"github.com/kardianos/service"
	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
	"pbscommon"
	"snapshot"
)

// NimbusService wraps the application for service execution (the Windows SCM
// in production; kardianos/service is portable, so the Linux view is a whole
// program too, which is what lets the lint see every caller)
type NimbusService struct {
	app       *App
	apiServer *api.Server
	// apiListener is the local API's pipe, opened by Start so that a pipe
	// name already held by another process is a failed start.
	apiListener net.Listener
	stopChan    chan struct{}
}

// listenLocalAPI opens the local API's named pipe (docs/V4-LOCAL-API.md §3).
// A variable so a test can stand in for the pipe; the pipe's own behavior is
// gui/api's to test.
var listenLocalAPI = func() (net.Listener, error) { return api.ListenPipe(api.PipeName) }

// Start is called when the service starts.
//
// The local API's pipe is created HERE, before Start returns, and failing to
// create it fails the start. The first instance is created with
// FILE_FLAG_FIRST_PIPE_INSTANCE, so this fails when another process already
// holds the name; a service that started anyway would be unreachable at best,
// and at worst every console would be talking to that process. The SCM
// reports the failure, and the reason is in the log.
func (s *NimbusService) Start(svc service.Service) error {
	writeInfoLog("NimbusBackup service starting...")
	ln, err := listenLocalAPI()
	if err != nil {
		writeErrorLog(fmt.Sprintf("NimbusBackup service NOT started: the local API pipe could not be created: %v", err))
		return fmt.Errorf("the local API pipe %s could not be created: %w", api.PipeName, err)
	}
	s.apiListener = ln
	s.stopChan = make(chan struct{})
	go s.run()
	return nil
}

// run contains the main service loop
func (s *NimbusService) run() {
	writeInfoLog("NimbusBackup service running")

	// This process IS the service: it executes backups and restores itself.
	s.app = &App{
		config: LoadConfig(),
	}

	// Load configuration (service will read config from file when needed)
	configMap := s.app.GetConfigWithHostname()
	if hostname, ok := configMap["hostname"].(string); ok {
		writeInfoLog(fmt.Sprintf("Service: Running for %s", hostname))
	} else {
		writeInfoLog("Service: Running in background")
	}

	// Config will be loaded from file by each scheduled job when needed

	// A run the last process died during is closed as interrupted when the
	// run history is loaded below (loadRunHistory), which replaced
	// CleanupAbandonedJobs and job_history.json (docs/V4-CONSOLE.md §4.2).

	// Clear any orphaned VSS shadow copies and reset the VSS service state
	// from a previously crashed Nimbus process. Without this, the next backup
	// can fail with "VSS_START - shadow copy creation is already in progress".
	if err := snapshot.VSSCleanup(); err != nil {
		writeErrorLog(fmt.Sprintf("VSS cleanup at startup reported error: %v", err))
	}

	// Trim stale restore listing caches in the background (best-effort).
	// Moved here from the GUI's startup with the restore engine: the service
	// writes this cache, so the service ages it out.
	go func() {
		trimSnapshotTreeCache(30 * 24 * time.Hour)
	}()

	// Recalculate stale nextRun values (e.g. after service restart or missed window)
	s.app.RecalculateNextRuns()

	// Start the scheduler
	s.app.StartScheduler()

	// Control plane (NimbusControl): the service is the brain, so the
	// enroll/check-in loop lives here. No-op unless control_server_url is
	// configured. The GUI reads status via /controlplane/status.
	//
	// A preconfigured MSI leaves a provisioning profile beside config.json;
	// consume it FIRST so a freshly imaged machine enrolls itself on this
	// start rather than waiting for a technician. Consuming it is also what
	// destroys the one-time org token it carries.
	s.app.ApplyProvisioningProfile()
	s.app.StartControlPlane()

	// The local API, for the console. Who may call it is read from Windows
	// per connection on the pipe Start opened (docs/V4-LOCAL-API.md §3); the
	// shared token file it replaced is removed if an older version left one.
	removeRetiredAPIToken()
	s.apiServer = api.NewServer(s.app)
	s.apiServer.SetVersion(appVersion)
	// The run history survives a restart (docs/V4-CONSOLE.md §4.2). Before
	// the registry is shared below, so no run begins while the file is read;
	// a run the last process died during is closed here as interrupted.
	loadRunHistory(s.apiServer.Runs())
	// One store for every trigger. Until this line the scheduler and the
	// API server each knew only about their own runs, which is why a
	// scheduled backup was invisible to the GUI.
	SetRunRegistry(s.apiServer.Runs())
	// A self-update waits for console restores too.
	activeRestores = s.apiServer.ActiveRestoreCount

	// The lockdown control. A predicate rather than a value because the
	// policy arrives on a check-in: an org that locks a machine expects that
	// on the next call, not on the next service restart. ControlPolicy()
	// here is the service-side evaluation, so it already has break-glass
	// folded in and does not need a round trip.
	s.apiServer.SetLockedFunc(func() bool { return ControlPolicy().GUIReadOnly })

	// The restore control. Same shape and the same reasoning as the lockdown
	// predicate above, for the capability that decides whether this machine
	// may restore at all.
	//
	// THIS IS THE GATE. Before the restore rewire the console evaluated
	// `file_restore` itself, in the process the policy exists to restrain;
	// now it asks the service to restore and the service decides. The api
	// package refuses when no predicate is installed, so a future edit that
	// drops this line fails closed — restore stops working, loudly, instead
	// of quietly becoming ungated.
	s.apiServer.SetRestoreAllowedFunc(func(right string) bool {
		switch right {
		case api.RightFileRestore:
			return ControlPolicy().FileRestore
		}
		// An op declaring a right this build has never heard of is a version
		// skew, and the safe reading of "I do not know what you are asking
		// for" is no.
		return false
	})

	// The panel's connection tiles. The provider serves from a cache that a
	// background sweep refreshes, so a console left open cannot generate
	// steady traffic against every datastore the org owns.
	s.apiServer.SetConnectionsFunc(s.app.connectionsSnapshot)
	s.app.startConnectionProbe()

	// Startup jobs run HERE now, not in the GUI.
	//
	// BEHAVIOUR CHANGE, deliberate: runAtStartup used to fire when a user
	// LOGGED IN and opened the front end, because that is where the call
	// lived. On a server nobody logs into, it therefore never fired at all.
	// The service starts at boot, so this is what the setting has always
	// meant to whoever ticked the box. It runs after the registry is
	// installed so the run is visible to /runs/active from its first
	// moment, and in a goroutine so a long startup backup does not hold up
	// the API server coming online.
	go s.app.HandleStartupRun()

	// The console needs the WebView2 runtime and no longer installs it
	// itself (V4-CONSOLE D2): the service does, machine-wide, if it is
	// missing. In the background: it is a download, and nothing above
	// waits on it.
	go ensureWebView2Runtime()

	writeInfoLog(fmt.Sprintf("Serving the local API on %s", api.PipeName))
	go func() {
		if err := s.apiServer.Serve(s.apiListener); err != nil {
			writeErrorLog(fmt.Sprintf("API server error: %v", err))
		}
	}()

	// Keep the service running (scheduler and API server run in background goroutines)
	writeInfoLog("Service main loop started, waiting for stop signal")
	<-s.stopChan // Block until stop signal received
	writeInfoLog("Stop signal received, service main loop exiting")
}

// Stop is called when the service stops
func (s *NimbusService) Stop(svc service.Service) error {
	writeInfoLog("NimbusBackup service stopping...")

	// Close the active runs as interrupted by this stop FIRST (F-75 R7):
	// cutting the PBS sessions below makes the engine fail with a socket
	// error, and that is not what happened.
	if n := interruptActiveRunsForStop(); n > 0 {
		writeWarnLog(fmt.Sprintf("Service stopping: %d backup run(s) in progress closed as interrupted", n))
	}

	// Close any live PBS backup session before we return, so the server
	// releases the writer / snapshot lock instead of waiting for TCP
	// keepalive to reap the abandoned connection.
	pbscommon.CloseAllActive()

	// No new console requests once stopping.
	if s.apiListener != nil {
		_ = s.apiListener.Close()
	}

	// Signal the main loop to exit
	if s.stopChan != nil {
		close(s.stopChan)
	}

	// Stop the scheduler gracefully
	if s.app != nil {
		s.app.StopScheduler()
	}

	// Give it a moment to finish current operations
	time.Sleep(2 * time.Second)

	writeInfoLog("NimbusBackup service stopped")
	return nil
}

// interruptActiveRunsForStop is R7 (F-75) for the service's stop path: the
// active runs are closed as interrupted by the stop before their PBS
// sessions are cut, so the portal says what happened instead of a socket
// error. Returns how many runs it closed.
func interruptActiveRunsForStop() int {
	cpMu.Lock()
	c := cpClient
	cpMu.Unlock()
	if c == nil {
		return 0
	}
	return c.InterruptActiveRuns(controlplane.InterruptedByStopText)
}
