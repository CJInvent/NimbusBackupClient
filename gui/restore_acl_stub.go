//go:build !windows

package main

// Non-Windows stubs so the Linux CI build compiles. NTFS security descriptors
// and alternate data streams are Win32 concepts; restoring them anywhere else
// is meaningless, and the UI never enables the options off-Windows.

import "fmt"

func applyNTFSSecurity(_ string, _ []byte) (string, error) {
	return "", fmt.Errorf("NTFS permissions can only be applied on Windows")
}

func writeADS(_, _ string, _ []byte) error {
	return fmt.Errorf("alternate data streams can only be written on Windows")
}
