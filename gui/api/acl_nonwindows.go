//go:build !windows
// +build !windows

package api

// On non-Windows the 0600 file mode already restricts the token to its owner.
func restrictToOwners(path string) error { return nil }
