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
	callbacksMap     map[string]*progressCallbacks
	callbacksMutex   sync.RWMutex
	isServiceProcess bool // True if running as Windows Service (never re-detect mode)

	lastImageTruncated bool               // legacy: kept for the LastImageListTruncated binding (always false now)
	lastImageKey       string             // cache key of the most recent partition scan
	ibRestoreMu        sync.Mutex         // guards ibRestoreCancel
	ibRestoreCancel    context.CancelFunc // set while an image restore runs; nil otherwise

	backupMu     sync.Mutex         // guards backupCancel
	backupCancel context.CancelFunc // set while a backup runs in THIS process; nil otherwise
}

// setBackupCancel stores (or clears, with nil) the cancel func of the backup
// running in this process. Held under backupMu so a concurrent StopBackup sees
// a consistent value.
func (a *App) setBackupCancel(cancel context.CancelFunc) {
	a.backupMu.Lock()
	a.backupCancel = cancel
	a.backupMu.Unlock()
}

// CancelActiveBackup signals the backup running in THIS process to stop, if any,
// and reports whether one was running. Cancellation is cooperative: the engine's
// reader loop observes the cancelled context and returns an error BEFORE the PBS
// index is committed (so the incomplete backup is discarded server-side), and the
// deferred VSS Release then deletes the shadow copy and its symlink. A stop
// therefore leaves no scraps, wherever in the stream it lands.
func (a *App) CancelActiveBackup() bool {
	a.backupMu.Lock()
	cancel := a.backupCancel
	a.backupMu.Unlock()
	if cancel != nil {
		cancel()
		writeDebugLog("Backup: cancel requested for in-progress backup")
		return true
	}
	return false
}

// progressCallbacks stores the callback functions for a backup operation
type progressCallbacks struct {
	onProgress func(jobID string, percent float64, message string)
	onStats    func(jobID string, bytesDone, bytesTotal, newChunks, reusedChunks uint64)
	onComplete func(jobID string, success bool, message string)
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{
		config:        LoadConfig(),
		stopScheduler: make(chan struct{}),
		apiClient:     api.NewClient(getAPITokenPath()),
		callbacksMap:  make(map[string]*progressCallbacks),
	}
}

// NewAppForService creates an App instance for Windows Service (no Wails runtime)
func NewAppForService(ctx context.Context) *App {
	return &App{
		ctx:              ctx,
		config:           LoadConfig(),
		stopScheduler:    make(chan struct{}),
		apiClient:        api.NewClient(getAPITokenPath()),
		mode:             api.ModeStandalone, // Service executes directly
		callbacksMap:     make(map[string]*progressCallbacks),
		isServiceProcess: true, // Prevent mode re-detection
	}
}
