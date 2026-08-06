package main

import (
	"context"
	"sync"

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

	lastImageTruncated bool               // legacy: kept for the LastImageListTruncated binding (always false now)
	lastImageKey       string             // cache key of the most recent partition scan
	ibRestoreMu        sync.Mutex         // guards ibRestoreCancel
	ibRestoreCancel    context.CancelFunc // set while an image restore runs; nil otherwise

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
