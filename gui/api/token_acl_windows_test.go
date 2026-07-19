//go:build windows

package api

// S8 (token half) — the local API's credential on real Windows.
//
// EnsureToken + restrictToOwners are the only thing standing between a hostile
// local process and the privileged service API, and their behavior is pure
// Windows: icacls, DACL inheritance, file ACLs. The Linux jobs compile
// acl_nonwindows.go, which is a no-op, so none of this is exercised anywhere
// else in the tree.
//
// Assertions are deliberately locale-independent: icacls prints ACE principals
// in the system language, but the inherited-ACE marker "(I)" is a flag, not a
// translated word. Its absence is what proves /inheritance:r took effect and a
// permissive ProgramData parent cannot re-widen the token.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureTokenCreatesAndReusesWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-token")

	tok, err := EnsureToken(path)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}
	if len(tok) != 64 {
		t.Errorf("token length = %d, want 64 hex chars (32 random bytes)", len(tok))
	}
	if strings.TrimSpace(tok) != tok {
		t.Error("token has surrounding whitespace")
	}
	for _, r := range tok {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("token is not lowercase hex: %q", tok)
		}
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("token file unreadable: %v", err)
	}
	if strings.TrimSpace(string(onDisk)) != tok {
		t.Error("returned token does not match the file contents")
	}

	// Idempotent: a restarting service must not invalidate the running GUI.
	again, err := EnsureToken(path)
	if err != nil {
		t.Fatalf("second EnsureToken: %v", err)
	}
	if again != tok {
		t.Error("EnsureToken minted a new token for an existing file")
	}

	// A blank file is treated as absent and replaced.
	if err := os.WriteFile(path, []byte("   \r\n"), 0o600); err != nil {
		t.Fatalf("blank the token file: %v", err)
	}
	replaced, err := EnsureToken(path)
	if err != nil {
		t.Fatalf("EnsureToken over blank file: %v", err)
	}
	if replaced == "" || replaced == tok {
		t.Errorf("blank token file was not replaced with a fresh token (got %q)", replaced)
	}
}

func TestTokenFileDACLIsRestrictedWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-token")
	if _, err := EnsureToken(path); err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}

	out, err := exec.Command("icacls", path).CombinedOutput()
	if err != nil {
		t.Fatalf("icacls %s: %v: %s", path, err, out)
	}
	acl := string(out)
	t.Logf("icacls output:\n%s", acl)

	// /inheritance:r must have dropped every inherited ACE. "(I)" marks an
	// inherited entry and is not localized.
	for _, line := range strings.Split(acl, "\n") {
		if strings.Contains(line, "(I)") {
			t.Errorf("token file still carries an inherited ACE, so a permissive "+
				"parent directory can widen access: %s", strings.TrimSpace(line))
		}
	}

	// restrictToOwners must be callable directly and stay idempotent — it runs
	// on every service start.
	if err := restrictToOwners(path); err != nil {
		t.Errorf("restrictToOwners on an already-restricted file: %v", err)
	}
}
