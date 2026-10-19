package main

import (
	"controlplane"
	"sync"
)

// App is the service's state. Nothing binds it: the console is its own module
// (console/) and reaches the service only through the local API.
type App struct {
	storageMu      sync.Mutex
	storageOpMu    sync.Mutex
	storageState   *controlplane.StorageState
	storageFailure string
	config         *Config
}
