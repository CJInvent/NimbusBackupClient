//go:build !windows
// +build !windows

package main

// No DACLs off Windows; the 0600 mode atomicWriteFile applies is the whole
// protection. Kept as a no-op rather than build-tagging every call site.
func restrictToServiceOnly(path string) error { return nil }
