//go:build windows
// +build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DPAPI machine-scope wrap/unwrap for the master key. Machine scope
// (CRYPTPROTECT_LOCAL_MACHINE) is deliberate: the LocalSystem service and a
// user-context standalone GUI must both be able to unwrap the same DEK. The
// trade-off (any local process can call DPAPI machine-scope) is accepted for
// Phase 2 and is why Phase 3 upgrades the protector to the TPM.

var (
	modcrypt32           = windows.NewLazySystemDLL("crypt32.dll")
	procCryptProtectData = modcrypt32.NewProc("CryptProtectData")
	procCryptUnprotect   = modcrypt32.NewProc("CryptUnprotectData")
	modkernel32Secrets   = windows.NewLazySystemDLL("kernel32.dll")
	procLocalFreeSecrets = modkernel32Secrets.NewProc("LocalFree")
)

const (
	cryptprotectUIForbidden  = 0x1
	cryptprotectLocalMachine = 0x4
)

type dpapiBlob struct {
	cbData uint32
	pbData *byte
}

func dpapiProtect(data []byte) ([]byte, error) {
	return dpapiCall(procCryptProtectData, data)
}

func dpapiUnprotect(data []byte) ([]byte, error) {
	return dpapiCall(procCryptUnprotect, data)
}

func dpapiCall(proc *windows.LazyProc, data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty input")
	}
	in := dpapiBlob{cbData: uint32(len(data)), pbData: &data[0]}
	var out dpapiBlob
	flags := uintptr(cryptprotectUIForbidden | cryptprotectLocalMachine)
	// CryptProtectData / CryptUnprotectData share the same 7-arg shape:
	// (pDataIn, szDescr/ppszDescr, pOptionalEntropy, pvReserved, pPromptStruct, dwFlags, pDataOut)
	r, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0,
		flags,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return nil, fmt.Errorf("dpapi call failed: %v", callErr)
	}
	defer procLocalFreeSecrets.Call(uintptr(unsafe.Pointer(out.pbData))) //nolint:errcheck // LocalFree returns NULL on success
	buf := make([]byte, out.cbData)
	copy(buf, unsafe.Slice(out.pbData, out.cbData))
	return buf, nil
}
