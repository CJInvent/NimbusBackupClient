//go:build windows
// +build windows

package api

// restrictToOwners locks the API token file's DACL to the well-known local
// SIDs that legitimately need it, via icacls with SID strings:
//
//	S-1-5-18   LocalSystem (the service)
//	S-1-5-32-544 local Administrators
//	S-1-5-4    INTERACTIVE (the logged-on user running the GUI)
//
// These SIDs are machine-local and identical on domain and workgroup machines,
// so the ACL is domain-independent - the correct answer given Windows has no
// per-executable ACL primitive (DACLs bind to security principals, not
// binaries). /inheritance:r drops inherited ACEs so a permissive parent (e.g.
// ProgramData) cannot re-widen access. icacls is used rather than the x/sys
// security APIs deliberately: it mirrors the repo's existing subprocess
// pattern and has no fragile struct/symbol surface.

import (
	"fmt"
	"os/exec"
)

func restrictToOwners(path string) error {
	cmd := exec.Command("icacls", path,
		"/inheritance:r",
		"/grant:r", "*S-1-5-18:F",
		"/grant:r", "*S-1-5-32-544:F",
		"/grant:r", "*S-1-5-4:F",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("icacls failed: %v: %s", err, string(out))
	}
	return nil
}
