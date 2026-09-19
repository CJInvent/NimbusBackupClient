//go:build !windows

package main

import (
	"controlplane"
	"errors"
)

func discoverStorageDevices() ([]controlplane.StorageDevice, error) {
	return nil, errors.New("windows storage identity unavailable on this platform")
}
