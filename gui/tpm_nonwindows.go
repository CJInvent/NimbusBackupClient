//go:build !windows
// +build !windows

package main

import "errors"

// The TPM protector uses Windows' Platform Crypto Provider; on other platforms
// the protector chain falls through to DPAPI (also absent) and then plaintext.

func tpmProtect(dek []byte) ([]byte, error) {
	return nil, errors.New("tpm protector is not available on this platform")
}

func tpmUnprotect(blob []byte) ([]byte, error) {
	return nil, errors.New("tpm protector is not available on this platform")
}
