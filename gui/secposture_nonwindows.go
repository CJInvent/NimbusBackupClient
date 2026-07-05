//go:build !windows
// +build !windows

package main

// Speculation-control and Windows Update posture checks are Windows-specific.
func collectSecurityWarnings() []string { return nil }
