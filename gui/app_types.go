package main

import (
	"context"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// App struct contains the application state
type App struct {
	ctx              context.Context
	config           *Config
	stopScheduler    chan struct{}
	apiClient        *api.Client
	mode             api.ExecutionMode
	isServiceProcess bool // True if running as Windows Service (never re-detect mode)

	// lastImageTruncated is the console's memory of whether the SERVICE's
	// last partition scan hit its entry cap, so LastImageListTruncated can
	// answer without a second round trip. The scan itself, its cache key and
	// its cancellation live in the service (imagebrowse_core.go) — they are
	// engine state, and the console holding a copy of them was an artifact of
	// the two once being one process.
	lastImageTruncated bool
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{
		config:        LoadConfig(),
		stopScheduler: make(chan struct{}),
		apiClient:     api.NewClient(getAPITokenPath()),
	}
}

// NewAppForService creates an App instance for Windows Service (no Wails runtime)
func NewAppForService(ctx context.Context) *App {
	return &App{
		ctx:              ctx,
		config:           LoadConfig(),
		stopScheduler:    make(chan struct{}),
		apiClient:        api.NewClient(getAPITokenPath()),
		mode:             api.ModeInProcess, // this process IS the service
		isServiceProcess: true,              // Prevent mode re-detection
	}
}
