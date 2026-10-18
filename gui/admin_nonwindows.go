//go:build !windows && service
// +build !windows,service

package main

// isAdmin returns true on non-Windows systems (assume we have necessary privileges)
func isAdmin() bool {
	return true
}
