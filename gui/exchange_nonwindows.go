//go:build !windows
// +build !windows

package main

// Exchange detection and tasks are Windows-only.
func detectExchange() (bool, string) { return false, "" }

func getExchangeCircularLogging() (bool, bool, string) { return false, false, "" }
