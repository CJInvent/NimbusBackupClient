//go:build windows

package main

// restore_acl_windows.go — applying restored NTFS metadata on Windows.
//
// The imagebrowse side hands us exactly what the source volume stored: a
// SELF-RELATIVE security descriptor (bytes) per file, and named alternate
// data streams. This file is the other half: writing them onto the restored
// files with Win32 semantics.
//
// Design choices, stated plainly:
//   - DACL is applied always. Owner and group are ATTEMPTED; setting an
//     arbitrary owner needs SeRestorePrivilege, which a normal GUI session
//     does not hold, so owner/group failures downgrade to a warning rather
//     than failing the file. The data is already restored at that point.
//   - SACL (auditing rules) is skipped entirely: it needs SeSecurityPrivilege
//     and auditing config rarely belongs on restored copies.
//   - SIDs from another machine may not resolve here. That is fine and
//     expected — NTFS stores raw SIDs, Windows shows them unresolved, and an
//     admin can re-ACL. We restore what was stored, we do not translate.

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// applyNTFSSecurity applies a self-relative security descriptor to path.
// Returns (warning, error): a non-empty warning means the file is restored
// and its DACL applied, but owner/group could not be set.
func applyNTFSSecurity(path string, sd []byte) (string, error) {
	if len(sd) < 20 {
		return "", fmt.Errorf("security descriptor too short (%d bytes)", len(sd))
	}
	psd := (*windows.SECURITY_DESCRIPTOR)(unsafe.Pointer(&sd[0]))

	dacl, _, err := psd.DACL()
	if err != nil && err != windows.ERROR_OBJECT_NOT_FOUND {
		return "", fmt.Errorf("parse DACL: %w", err)
	}
	owner, _, _ := psd.Owner()
	group, _, _ := psd.Group()

	// Full attempt first: owner + group + DACL in one call.
	var info windows.SECURITY_INFORMATION = windows.DACL_SECURITY_INFORMATION
	if owner != nil {
		info |= windows.OWNER_SECURITY_INFORMATION
	}
	if group != nil {
		info |= windows.GROUP_SECURITY_INFORMATION
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, owner, group, dacl, nil)
	if err == nil {
		return "", nil
	}

	// Owner/group need SeRestorePrivilege; retry with the DACL alone so the
	// permissions that matter day-to-day still land.
	derr := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if derr != nil {
		return "", fmt.Errorf("apply DACL: %w", derr)
	}
	return fmt.Sprintf("owner/group not applied (needs elevation): %v", err), nil
}

// writeADS writes one alternate data stream onto an existing file. On NTFS
// this is simply the "path:stream" filename syntax. Fails cleanly on
// non-NTFS destinations — the caller surfaces that as a per-file warning.
func writeADS(path, stream string, data []byte) error {
	if stream == "" {
		return fmt.Errorf("empty stream name")
	}
	f, err := os.Create(path + ":" + stream)
	if err != nil {
		return fmt.Errorf("create stream %s:%s: %w", path, stream, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write stream %s:%s: %w", path, stream, err)
	}
	return f.Close()
}
