package main

// Provisioning ingestion — the client half of docs/MSI-PROVISIONING.md.
//
// A preconfigured MSI drops provisioning.json beside config.json. The SERVICE
// consumes it on startup, before StartControlPlane, and destroys it. The GUI
// never touches it: config writes are the service's job (dev rule 2), and this
// is a config write carrying a credential.
//
// Two rules govern the whole flow, and both exist because getting them wrong
// is silent:
//
//  1. An already-enrolled agent is NEVER re-pointed. On an in-place upgrade
//     the preconfigured MSI drops its profile again — a machine that has been
//     backing up for a year must not be silently moved to a different org or
//     have its identity reset. The profile is consumed and discarded instead.
//  2. The token is destroyed whether or not enrollment succeeds. It is a
//     one-time bearer credential; leaving it on disk to retry later turns a
//     provisioning artifact into a permanent one (dev rule 10).

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"controlplane"
)

// provisioningFileName sits beside config.json in the ProgramData directory.
const provisioningFileName = "provisioning.json"

func provisioningPath() (string, error) {
	dir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, provisioningFileName), nil
}

// ApplyProvisioningProfile consumes a provisioning profile if one is present.
// Safe to call on every service start; a no-op when there is no profile.
//
// Returns true when settings were applied and the caller should proceed to
// enrollment with the new configuration.
func (a *App) ApplyProvisioningProfile() bool {
	path, err := provisioningPath()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed path under ProgramData
	if err != nil {
		return false // absent is the normal case for a hand-installed agent
	}

	// From here on the file is consumed no matter what happens: a profile that
	// cannot be applied must not sit on disk holding a live org token.
	defer destroyProvisioningFile(path)

	profile, err := controlplane.ParseProfile(raw)
	if err != nil {
		writeDebugLog(fmt.Sprintf("[provisioning] REFUSED profile: %v", err))
		return false
	}

	if a.config.ControlAgentID != 0 {
		// Rule 1. An upgrade re-delivers the profile; that is not a request to
		// change allegiance.
		writeDebugLog(fmt.Sprintf(
			"[provisioning] already enrolled as agent %d — profile discarded without applying (%s)",
			a.config.ControlAgentID, profile.Redacted()))
		return false
	}
	if a.config.ControlServerURL != "" && a.config.ControlServerURL != profile.ControlURL {
		writeDebugLog(fmt.Sprintf(
			"[provisioning] a different control server is already configured (%s) — profile discarded (%s)",
			a.config.ControlServerURL, profile.Redacted()))
		return false
	}

	if age, ok := profile.Age(time.Now()); ok && age > 0 {
		writeDebugLog(fmt.Sprintf("[provisioning] profile issued %s ago", age.Truncate(time.Hour)))
	}
	writeDebugLog("[provisioning] applying profile: " + profile.Redacted())

	a.config.ControlServerURL = profile.ControlURL
	a.config.ControlCertFP = controlplane.NormalizeFingerprint(profile.CertFingerprint)
	a.config.ControlEnrollToken = profile.EnrollToken
	if profile.DefaultMode != "" && a.config.DefaultBackupMode == "" {
		// Only seed a default the machine has not already chosen for itself.
		a.config.DefaultBackupMode = profile.DefaultMode
	}
	if err := a.config.Save(); err != nil {
		writeDebugLog(fmt.Sprintf("[provisioning] WARNING: profile applied but config save failed: %v", err))
		return false
	}
	writeDebugLog("[provisioning] profile applied; enrollment will run on this start")
	return true
}

// destroyProvisioningFile overwrites the profile before unlinking it.
//
// The file holds a live org enrollment token. Unlinking alone leaves those
// bytes recoverable on the volume, and this file's whole purpose is to stop
// existing once it has been read. Best effort by design: on any failure the
// removal is still attempted, and a failure to remove is reported loudly
// because a provisioning token left on an endpoint is a standing risk.
func destroyProvisioningFile(path string) {
	if f, err := os.OpenFile(path, os.O_WRONLY, 0o600); err == nil { // #nosec G304 -- fixed path
		if st, err := f.Stat(); err == nil && st.Size() > 0 {
			zeros := make([]byte, st.Size())
			_, _ = f.Write(zeros)
			_ = f.Sync()
		}
		_ = f.Close()
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		writeDebugLog(fmt.Sprintf(
			"[provisioning] WARNING: could not remove %s: %v — it still contains an enrollment token, delete it manually",
			path, err))
		return
	}
	writeDebugLog("[provisioning] profile consumed and removed")
}
