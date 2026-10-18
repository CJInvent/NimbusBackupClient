//go:build !windows
// +build !windows

package main

import "fmt"

// ListPhysicalDisks is not supported on non-Windows platforms
func ListPhysicalDisks() ([]PhysicalDiskInfo, error) {
	return nil, fmt.Errorf("machine backup is only supported on Windows")
}

// RunMachineBackup is not supported on non-Windows platforms
func RunMachineBackup(opts BackupOptions) error {
	return fmt.Errorf("machine backup is only supported on Windows")
}
