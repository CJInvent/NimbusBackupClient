//go:build !windows && service
// +build !windows,service

package main

import "errors"

var errUpdateUnsupported = errors.New("self-update installs only on Windows")

// Self-update installs an MSI; there is nothing to run elsewhere.
func runUpdateTask(name, cmdPath string) error { return errUpdateUnsupported }

func deleteUpdateTask(name string) {}
