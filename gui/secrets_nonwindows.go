//go:build !windows
// +build !windows

package main

import "errors"

// DPAPI is Windows-only; on other platforms the protector chain falls through
// to the (loudly logged) plaintext protector. Phase 3's TPM protector will
// slot in above this the same way.

func dpapiProtect(data []byte) ([]byte, error) {
	return nil, errors.New("dpapi is not available on this platform")
}

func dpapiUnprotect(data []byte) ([]byte, error) {
	return nil, errors.New("dpapi is not available on this platform")
}
