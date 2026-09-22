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
	"sync"
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
// Persist policy separately from material. Both "off" and affirmative
// assignments must survive restart; material is still fetched only at run time.
func applyBackupKeyFromCheckin(ad *controlplane.BackupKeyAd) {
	warnIfProvisioningWasWrong(ad)
	defer reportKeyStatusOnChange(ad)
	if ad != nil {
		if err := recordEncryptionRequired(ad); err != nil {
			writeErrorLog(fmt.Sprintf("[BackupKey] could not persist required encryption policy: %v", err))
		}
		return
	}
	if err := recordEncryptionOff(); err != nil {
		writeWarnLog(fmt.Sprintf("[BackupKey] WARNING: could not record that encryption is disabled: %v", err))
	}
}

// reportKeyStatus tells the server what this machine found. Best-effort and
// never load-bearing: the decision has already been made by the time this runs,
// and a backup must not fail because a status report did. Returns whether the
// server received it.
//
// ad == nil IS REPORTED. It used to return early, so a machine moved out of an
// encrypted scope never told the server anything again and its last verdict
// ('mismatch', say) stood forever -- the machine showed red for a key it was no
// longer supposed to hold. controlplane.KeyStatusFor answers ok for it.
func reportKeyStatus(ad *controlplane.BackupKeyAd, st controlplane.KeyStorage) bool {
	cpMu.Lock()
	c := cpClient
	cpMu.Unlock()
	if c == nil {
		return false
	}
	rep := controlplane.KeyStatusFor(ad, st)
	if _, err := c.ReportKeyStatus(rep); err != nil {
		writeWarnLog(fmt.Sprintf("[BackupKey] key-status report failed: %v", err))
		return false
	}
	return true
}

// adReport remembers which assignment this process last reported a status
// for, so the check-in hook reports on CHANGE rather than every two minutes
// (V4-RUN-AUDIT §5 rule 1, and the server's 20/hour ceiling on this endpoint).
var adReport struct {
	sync.Mutex
	sent bool
	sig  string
}

func adSignature(ad *controlplane.BackupKeyAd) string {
	switch {
	case ad == nil:
		return "none"
	case ad.Unavailable:
		return "unavailable:" + ad.KeyID
	default:
		return "key:" + ad.KeyID
	}
}

// reportKeyStatusOnChange refreshes the server's verdict when the assignment
// changes -- including to "no key" -- and once per process start, without
// waiting for a backup to run. Before this the only reports came from the
// backup gate, so a policy change took effect in the fleet view only after
// the machine's next backup, and never for a move to no-key (see above).
//
// Recorded only when the report lands, so a failed one is retried on the
// next check-in. Off the check-in goroutine: the report has its own retries,
// and the loop must not wait on them.
func reportKeyStatusOnChange(ad *controlplane.BackupKeyAd) {
	sig := adSignature(ad)
	adReport.Lock()
	if adReport.sent && adReport.sig == sig {
		adReport.Unlock()
		return
	}
	adReport.Unlock()
	cpMu.Lock()
	c := cpClient
	cpMu.Unlock()
	if c == nil {
		return // nobody to tell; nothing is recorded, so a later client will
	}
	// Storage is read HERE, on the check-in goroutine, and only the network
	// call is detached: the storage read touches process-wide protector state
	// that must not be raced.
	st := backupKeyStorage()
	go func() {
		if reportKeyStatus(ad, st) {
			adReport.Lock()
			adReport.sent, adReport.sig = true, sig
			adReport.Unlock()
		}
	}()
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
		"[BackupKey] WARNING: this machine was provisioned with encryption=off and may have " +
			"backed up in the clear, but the control server says this org DOES encrypt. " +
			"Backups taken before this check-in are unencrypted; new backups must pass the encryption gate. " +
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
		return &controlplane.BackupKeyAd{KeyID: keyID, Unavailable: keyID == ""}, nil
	default:
		return nil, ErrBackupKeyUnknown
	}
}
