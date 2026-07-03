//go:build windows
// +build windows

package main

// Phase 3 protector: wrap the master DEK with an RSA key held by the TPM via
// Windows' Platform Crypto Provider (ncrypt.dll). Chosen over a Go TPM library
// on purpose: zero new dependencies, and the PCP is the OS-supported path to
// TPM-backed keys (it handles TPM ownership, SRK hierarchy, and hardware
// quirks). The wrapped DEK can only be unwrapped by THIS machine's TPM.
//
// Failback: any error here (no TPM, vTPM absent, permission denied for the
// machine key as a non-admin, unsupported padding) simply makes the protector
// chain fall through to DPAPI. loadOrCreateDEK additionally verifies a full
// protect->unprotect round-trip before committing to the tpm protector, so a
// TPM that encrypts but cannot decrypt can never strand the key material.

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modncrypt                     = windows.NewLazySystemDLL("ncrypt.dll")
	procNCryptOpenStorageProvider = modncrypt.NewProc("NCryptOpenStorageProvider")
	procNCryptOpenKey             = modncrypt.NewProc("NCryptOpenKey")
	procNCryptCreatePersistedKey  = modncrypt.NewProc("NCryptCreatePersistedKey")
	procNCryptSetProperty         = modncrypt.NewProc("NCryptSetProperty")
	procNCryptFinalizeKey         = modncrypt.NewProc("NCryptFinalizeKey")
	procNCryptEncrypt             = modncrypt.NewProc("NCryptEncrypt")
	procNCryptDecrypt             = modncrypt.NewProc("NCryptDecrypt")
	procNCryptFreeObject          = modncrypt.NewProc("NCryptFreeObject")
)

const (
	ncryptMachineKeyFlag = 0x00000020
	ncryptPadOAEPFlag    = 0x00000004
	tpmKeyName           = "NimbusBackupMasterKey"
	tpmProviderName      = "Microsoft Platform Crypto Provider"
)

type bcryptOAEPPaddingInfo struct {
	pszAlgID *uint16
	pbLabel  *byte
	cbLabel  uint32
}

func tpmStatusErr(op string, r uintptr) error {
	return fmt.Errorf("%s failed: SECURITY_STATUS 0x%08x", op, uint32(r))
}

// tpmOpenOrCreateKey opens the persisted machine-wide RSA key in the platform
// (TPM) provider, creating and finalizing it on first use.
func tpmOpenOrCreateKey(create bool) (hProv, hKey uintptr, err error) {
	provName, err := windows.UTF16PtrFromString(tpmProviderName)
	if err != nil {
		return 0, 0, err
	}
	if r, _, _ := procNCryptOpenStorageProvider.Call(
		uintptr(unsafe.Pointer(&hProv)),
		uintptr(unsafe.Pointer(provName)),
		0,
	); r != 0 {
		return 0, 0, tpmStatusErr("NCryptOpenStorageProvider(PCP)", r)
	}

	keyName, err := windows.UTF16PtrFromString(tpmKeyName)
	if err != nil {
		tpmFree(hProv)
		return 0, 0, err
	}

	r, _, _ := procNCryptOpenKey.Call(
		hProv,
		uintptr(unsafe.Pointer(&hKey)),
		uintptr(unsafe.Pointer(keyName)),
		0,
		ncryptMachineKeyFlag,
	)
	if r == 0 {
		return hProv, hKey, nil
	}
	if !create {
		tpmFree(hProv)
		return 0, 0, tpmStatusErr("NCryptOpenKey", r)
	}

	// Create the persisted key (RSA 2048, machine-wide) and finalize it.
	algRSA, err := windows.UTF16PtrFromString("RSA")
	if err != nil {
		tpmFree(hProv)
		return 0, 0, err
	}
	if r, _, _ := procNCryptCreatePersistedKey.Call(
		hProv,
		uintptr(unsafe.Pointer(&hKey)),
		uintptr(unsafe.Pointer(algRSA)),
		uintptr(unsafe.Pointer(keyName)),
		0,
		ncryptMachineKeyFlag,
	); r != 0 {
		tpmFree(hProv)
		return 0, 0, tpmStatusErr("NCryptCreatePersistedKey", r)
	}

	lengthProp, err := windows.UTF16PtrFromString("Length")
	if err != nil {
		tpmFree(hKey)
		tpmFree(hProv)
		return 0, 0, err
	}
	keyLen := make([]byte, 4)
	binary.LittleEndian.PutUint32(keyLen, 2048)
	if r, _, _ := procNCryptSetProperty.Call(
		hKey,
		uintptr(unsafe.Pointer(lengthProp)),
		uintptr(unsafe.Pointer(&keyLen[0])),
		4,
		0,
	); r != 0 {
		tpmFree(hKey)
		tpmFree(hProv)
		return 0, 0, tpmStatusErr("NCryptSetProperty(Length)", r)
	}

	if r, _, _ := procNCryptFinalizeKey.Call(hKey, 0); r != 0 {
		tpmFree(hKey)
		tpmFree(hProv)
		return 0, 0, tpmStatusErr("NCryptFinalizeKey", r)
	}
	return hProv, hKey, nil
}

func tpmFree(h uintptr) {
	if h != 0 {
		procNCryptFreeObject.Call(h) //nolint:errcheck // freeing on cleanup path
	}
}

// tpmCrypt runs NCryptEncrypt/NCryptDecrypt with RSA-OAEP(SHA-256) using the
// standard two-call (size, then data) pattern.
func tpmCrypt(proc *windows.LazyProc, opName string, hKey uintptr, in []byte) ([]byte, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("empty input")
	}
	algSHA256, err := windows.UTF16PtrFromString("SHA256")
	if err != nil {
		return nil, err
	}
	pad := bcryptOAEPPaddingInfo{pszAlgID: algSHA256}

	var needed uint32
	if r, _, _ := proc.Call(
		hKey,
		uintptr(unsafe.Pointer(&in[0])),
		uintptr(uint32(len(in))),
		uintptr(unsafe.Pointer(&pad)),
		0,
		0,
		uintptr(unsafe.Pointer(&needed)),
		ncryptPadOAEPFlag,
	); r != 0 {
		return nil, tpmStatusErr(opName+" (size)", r)
	}

	out := make([]byte, needed)
	var written uint32
	if r, _, _ := proc.Call(
		hKey,
		uintptr(unsafe.Pointer(&in[0])),
		uintptr(uint32(len(in))),
		uintptr(unsafe.Pointer(&pad)),
		uintptr(unsafe.Pointer(&out[0])),
		uintptr(needed),
		uintptr(unsafe.Pointer(&written)),
		ncryptPadOAEPFlag,
	); r != 0 {
		return nil, tpmStatusErr(opName, r)
	}
	return out[:written], nil
}

// tpmProtect wraps the DEK with the TPM-held RSA key (creating it on first use).
func tpmProtect(dek []byte) ([]byte, error) {
	hProv, hKey, err := tpmOpenOrCreateKey(true)
	if err != nil {
		return nil, err
	}
	defer tpmFree(hKey)
	defer tpmFree(hProv)
	return tpmCrypt(procNCryptEncrypt, "NCryptEncrypt", hKey, dek)
}

// tpmUnprotect unwraps the DEK inside the TPM.
func tpmUnprotect(blob []byte) ([]byte, error) {
	hProv, hKey, err := tpmOpenOrCreateKey(false)
	if err != nil {
		return nil, err
	}
	defer tpmFree(hKey)
	defer tpmFree(hProv)
	return tpmCrypt(procNCryptDecrypt, "NCryptDecrypt", hKey, blob)
}
