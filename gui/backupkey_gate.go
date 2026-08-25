package main

// The verification gate, driven (V4-SPEC §11, phase F item 3).
//
// controlplane/backupkey.go holds the DECISION and is pure — no server, no
// registry, no disk. This file is the part that has to touch all three: it
// gathers the inputs, performs whatever the decision asks for, and hands the
// backup pipeline either key material or a refusal.
//
// ONE ENTRY POINT, resolveBackupKeyForRun, called from exactly one place
// (runBackupPipeline). That is deliberate. A gate with two callers is a gate
// with one caller that someone will forget, and the CLI already bypasses all
// policy for the same reason — a second path that assembles its own backup.
// That entry point and everything only it reaches live in
// backupkey_gate_service.go, tagged to match their caller; this file holds the
// parts the GUI build also needs.
//
// THE STATE THAT IS NOT ON THE WIRE. The server advertises the key on every
// check-in, so the obvious implementation reads the last advertisement and
// treats "no advertisement" as "no encryption". That is wrong in a way that
// takes years to notice: an agent that has never reached the server, or a
// service that restarted during an outage, has no advertisement either — and
// backing up in the clear because we have not been told otherwise is the exact
// silent downgrade the three-state design exists to prevent. So the org's
// answer is PERSISTED (gui/backupkey_store.go, the `encryption` field), the
// advertisement is remembered per process (Agent.CurrentBackupKey returns a
// `known` flag), and a machine that genuinely cannot tell REFUSES rather than
// guesses.

import (
	"controlplane"
	"errors"
	"fmt"
)

// ErrBackupKeyUnknown is the refusal for a managed machine that has never
// learned whether its org encrypts. Self-healing: the first successful check-in
// records the answer either way.
//
// It reads the way it does because an operator meets it in a failed-run
// message, not in a stack trace.
var ErrBackupKeyUnknown = errors.New(
	"this machine has not completed a check-in with the management server yet, " +
		"so it cannot tell whether backups for this organization must be encrypted; " +
		"it will back up normally once the server has been reached once")

// applyBackupKeyFromCheckin is the Agent.OnBackupKey hook.
//
// ITS ONLY JOB IS THE NULL CASE. A key advertisement needs no action here —
// material is fetched at backup time, on a mismatch, which is what keeps it off
// the wire on the ~720 check-ins a machine makes each day. But `null` is the
// server saying "this org does not encrypt", and that answer has to be written
// down while we can still hear it, or a machine that reboots into an outage
// cannot distinguish it from having heard nothing at all.
func applyBackupKeyFromCheckin(ad *controlplane.BackupKeyAd) {
	warnIfProvisioningWasWrong(ad)
	if ad != nil {
		return
	}
	if err := recordEncryptionOff(); err != nil {
		writeWarnLog(fmt.Sprintf("[BackupKey] WARNING: could not record that encryption is disabled: %v", err))
	}
}

// warnIfProvisioningWasWrong reports the one case a provisioning-seeded answer
// can actually cost something: the profile said the org does not encrypt, the
// machine acted on that, and the server now says it does.
//
// Backups taken in that window are in the clear and will stay that way. They
// are not silently wrong — encryption is per-snapshot and the older ones are
// readable exactly as written — but a customer who believes they are encrypted
// has a gap, and somebody has to be able to find out. The reverse direction
// (profile said "on", org does not encrypt) costs nothing: the machine refused
// or fetched, and now proceeds.
//
// A WARNING RATHER THAN A REFUSAL. By the time this runs the machine has heard
// from the server, so the next backup is already correct; refusing now would
// stop backups over something that has just fixed itself.
func warnIfProvisioningWasWrong(ad *controlplane.BackupKeyAd) {
	if ad == nil {
		return // the server agrees there is no key; nothing was downgraded
	}
	state, _, source, err := persistedEncryptionWithSource()
	if err != nil || source != encSourceProvisioning || state != encStateOff {
		return
	}
	writeWarnLog(
		"[BackupKey] WARNING: this machine was provisioned with encryption=off and has been " +
			"backing up in the clear, but the control server says this org DOES encrypt. " +
			"Backups taken before this check-in are unencrypted; new ones will be encrypted. " +
			"Check the provisioning profile that installed this machine.")
}

// adFromPersistedState reconstructs an advertisement from what was last written
// to disk, for a process that has not yet had a successful check-in.
//
// Returns (nil, nil) when the org is recorded as not encrypting, and an ERROR
// when nothing has ever been recorded. The error is the narrow, self-healing
// case described in ErrBackupKeyUnknown: a machine that enrolled but has never
// once reached the server. Guessing "off" there is the silent downgrade;
// guessing "on" would refuse every backup on a machine whose org never
// encrypts at all.
func adFromPersistedState() (*controlplane.BackupKeyAd, error) {
	state, keyID, err := persistedEncryption()
	if err != nil {
		return nil, fmt.Errorf(
			"encryption status for this machine could not be read from its own storage: %w", err)
	}
	switch state {
	case encStateOff:
		return nil, nil
	case encStateOn:
		// Only the identifier is reconstructed. That is all the decision needs:
		// it compares key_id against storage, and the master fingerprint /
		// public PEM matter only to a fetch, which cannot happen while the
		// server is unreachable anyway.
		return &controlplane.BackupKeyAd{KeyID: keyID}, nil
	default:
		return nil, ErrBackupKeyUnknown
	}
}
