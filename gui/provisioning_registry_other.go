//go:build !windows

package main

// The MSI property is a Windows registry value; elsewhere there is none.
func readProvisioningRegistry() (string, bool) { return "", false }

func deleteProvisioningRegistry() error { return nil }
