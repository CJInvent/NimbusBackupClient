//go:build windows
// +build windows

package main

// restrictToServiceOnly locks the backup-key file's DACL to the two principals
// that legitimately need it:
//
//	S-1-5-18     LocalSystem (the service — the only thing that runs backups)
//	S-1-5-32-544 local Administrators (recovery, and they own the box anyway)
//
// DELIBERATELY NARROWER THAN gui/api's token ACL, which also grants
// S-1-5-4 INTERACTIVE. That grant exists because the logged-on user's GUI must
// read the local-API token. The GUI links no backup engine (asserted in CI), so
// it has no reason to read the key that decrypts every snapshot — and a console
// user is exactly who this file should not be readable by.
//
// /inheritance:r drops inherited ACEs so ProgramData's permissive ACL cannot
// re-widen access. icacls rather than the x/sys security APIs, matching the
// existing pattern in gui/api/acl_windows.go.

import (
	"fmt"
	"os/exec"
)

func restrictToServiceOnly(path string) error {
	cmd := exec.Command("icacls", path,
		"/inheritance:r",
		"/grant:r", "*S-1-5-18:F",
		"/grant:r", "*S-1-5-32-544:F",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("icacls failed: %v: %s", err, string(out))
	}
	return nil
}
