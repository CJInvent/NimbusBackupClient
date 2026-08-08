//go:build windows && service
// +build windows,service

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/kardianos/service"
	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
	"pbscommon"
	"snapshot"
)

// NimbusService wraps the application for Windows Service execution
type NimbusService struct {
	app       *App
	apiServer *api.Server
	stopChan  chan struct{}
}

// Start is called when the service starts
func (s *NimbusService) Start(svc service.Service) error {
	writeDebugLog("NimbusBackup service starting...")
	s.stopChan = make(chan struct{})
	go s.run()
	return nil
}

// run contains the main service loop
func (s *NimbusService) run() {
	writeDebugLog("NimbusBackup service running")

	// Initialize app with background context (service has no Wails runtime)
	// IMPORTANT: Service App must be in Standalone mode to execute backups directly
	s.app = &App{
		ctx:              context.Background(),
		config:           LoadConfig(),
		stopScheduler:    make(chan struct{}),
		apiClient:        api.NewClient(getAPITokenPath()),
		mode:             api.ModeInProcess, // this process IS the service
		isServiceProcess: true,              // Prevent mode re-detection (would cause infinite loop)
	}

	// Load configuration (service will read config from file when needed)
	configMap := s.app.GetConfigWithHostname()
	if hostname, ok := configMap["hostname"].(string); ok {
		writeDebugLog(fmt.Sprintf("Service: Running for %s", hostname))
	} else {
		writeDebugLog("Service: Running in background")
	}

	// Config will be loaded from file by each scheduled job when needed

	// Clean up any abandoned jobs from previous crash
	s.app.CleanupAbandonedJobs()

	// Clear any orphaned VSS shadow copies and reset the VSS service state
	// from a previously crashed Nimbus process. Without this, the next backup
	// can fail with "VSS_START - shadow copy creation is already in progress".
	if err := snapshot.VSSCleanup(); err != nil {
		writeErrorLog(fmt.Sprintf("VSS cleanup at startup reported error: %v", err))
	}

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

	// Start HTTP API server for GUI communication (token-authenticated — H-01).
	// If token init fails the server keeps an empty token and rejects every
	// request (fail closed) rather than exposing the privileged API unauthenticated.
	apiToken, tokErr := api.EnsureToken(getAPITokenPath())
	if tokErr != nil {
		writeErrorLog(fmt.Sprintf("API token init failed (API will reject all requests): %v", tokErr))
	}
	s.apiServer = api.NewServer("127.0.0.1:18765", s.app, apiToken)
	s.apiServer.SetVersion(appVersion)
	// One store for every trigger. Until this line the scheduler and the
	// API server each knew only about their own runs, which is why a
	// scheduled backup was invisible to the GUI.
	SetRunRegistry(s.apiServer.Runs())

	// The lockdown control. A predicate rather than a value because the
	// policy arrives on a check-in: an org that locks a machine expects that
	// on the next call, not on the next service restart. ControlPolicy()
	// here is the service-side evaluation, so it already has break-glass
	// folded in and does not need a round trip.
	s.apiServer.SetLockedFunc(func() bool { return ControlPolicy().GUIReadOnly })

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
	writeDebugLog("Starting HTTP API server on 127.0.0.1:18765")

	go func() {
		if err := s.apiServer.Start(); err != nil {
			writeErrorLog(fmt.Sprintf("API server error: %v", err))
		}
	}()

	// Keep the service running (scheduler and API server run in background goroutines)
	writeDebugLog("Service main loop started, waiting for stop signal")
	<-s.stopChan // Block until stop signal received
	writeDebugLog("Stop signal received, service main loop exiting")
}

// Stop is called when the service stops
func (s *NimbusService) Stop(svc service.Service) error {
	writeDebugLog("NimbusBackup service stopping...")

	// Close any live PBS backup session before we return, so the server
	// releases the writer / snapshot lock instead of waiting for TCP
	// keepalive to reap the abandoned connection.
	pbscommon.CloseAllActive()

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

	writeDebugLog("NimbusBackup service stopped")
	return nil
}

// RunAsService starts the application as a Windows Service
func RunAsService() {
	writeDebugLog("Attempting to run as Windows Service")

	svcConfig := &service.Config{
		Name:        "NimbusBackup",
		DisplayName: "Nimbus Backup Service",
		Description: "Executes scheduled backups to Proxmox Backup Server with VSS support",
	}

	nimbusSvc := &NimbusService{}
	s, err := service.New(nimbusSvc, svcConfig)
	if err != nil {
		log.Fatal(err)
	}

	logger, err := s.Logger(nil)
	if err != nil {
		log.Fatal(err)
	}

	err = s.Run()
	if err != nil {
		logger.Error(err)
	}
}

// IsServiceMode checks if running in service mode
func IsServiceMode() bool {
	for _, arg := range os.Args {
		if arg == "--service" {
			return true
		}
	}
	return false
}
