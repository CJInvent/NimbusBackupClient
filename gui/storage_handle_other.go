//go:build !windows && service

package main

import (
	"controlplane"
	"errors"
)

func verifyOpenedStorageDevice(handle uintptr, expected controlplane.StorageDevice) error {
	return errors.New("windows disk identity unavailable")
}
