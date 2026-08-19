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

// maxKeyAttempts bounds the fetch-then-decide loop. The pure decision already
// refuses on the second pass via alreadyFetched; this is the belt to that
// braces, so a decision bug cannot turn into an unbounded hammering of a
// rate-limited endpoint.
const maxKeyAttempts = 2

// applyBackupKeyFromCheckin is the Agent.OnBackupKey hook.
//
// ITS ONLY JOB IS THE NULL CASE. A key advertisement needs no action here —
// material is fetched at backup time, on a mismatch, which is what keeps it off
// the wire on the ~720 check-ins a machine makes each day. But `null` is the
// server saying "this org does not encrypt", and that answer has to be written
// down while we can still hear it, or a machine that reboots into an outage
// cannot distinguish it from having heard nothing at all.
func applyBackupKeyFromCheckin(ad *controlplane.BackupKeyAd) {
	if ad != nil {
		return
	}
	if err := recordEncryptionOff(); err != nil {
		writeWarnLog(fmt.Sprintf("[BackupKey] WARNING: could not record that encryption is disabled: %v", err))
	}
}

// currentBackupKeyAd reports the advertisement in force and whether anything is
// known at all. (nil, true) is "the org does not encrypt"; (nil, false) is "we
// have not been told".
func currentBackupKeyAd() (*controlplane.BackupKeyAd, bool) {
	cpMu.Lock()
	ag := cpAgent
	cpMu.Unlock()
	if ag == nil {
		// No control plane in this process: a standalone install. The project
		// ships and is supported without NimbusControl, so this is a product,
		// not a bypass — encryption is simply not configured.
		return nil, true
	}
	return ag.CurrentBackupKey()
}

// resolveBackupKeyForRun runs the gate for one backup and returns what the
// engine should encrypt with.
//
// (nil, nil, nil) means back up in the clear, and it is returned ONLY for a
// configured state — never because something failed. Every failure path returns
// an error, and an error here stops the backup before it starts.
//
// runUUID names the run this fetch belongs to. It is passed to the server so
// the release audit can match a released key against a backup that followed;
// omitting it is the trivial evasion the audit flags, so an honest agent sends
// it.
func (a *App) resolveBackupKeyForRun(runUUID string) (key []byte, escrow []byte, err error) {
	ad, known := currentBackupKeyAd()
	if !known {
		ad, err = adFromPersistedState()
		if err != nil {
			return nil, nil, err
		}
		if ad == nil {
			return nil, nil, nil // persisted: this org does not encrypt
		}
	}

	st := backupKeyStorage()
	fetched := false

	for attempt := 0; attempt < maxKeyAttempts; attempt++ {
		action, reason := controlplane.BackupKeyDecision(ad, st, fetched)
		switch action {
		case controlplane.BackupKeyProceedPlain:
			reportKeyStatus(ad, st)
			return nil, nil, nil

		case controlplane.BackupKeyProceedEncrypted:
			raw, blob, keyID, lerr := loadBackupKey()
			if lerr != nil {
				// The storage said it holds this key and then could not produce
				// it. Refuse: this is the one case where "storage is fine" and
				// "the key is usable" came apart, and proceeding would mean
				// backing up in the clear for an org that encrypts.
				return nil, nil, fmt.Errorf(
					"encrypted backup is required but the stored key could not be opened: %w", lerr)
			}
			writeBackupLog(fmt.Sprintf("[BackupKey] backup will be encrypted under key %s (stored)", shortKeyID(keyID)))
			reportKeyStatus(ad, st)
			return raw, blob, nil

		case controlplane.BackupKeyFetch:
			m, ferr := a.fetchBackupKey(controlplane.KeyModeDurable, runUUID)
			if ferr != nil {
				// Do NOT return here. The next pass through the decision, with
				// fetched=true, turns this into the correctly-worded refusal —
				// and would turn it into a legitimate proceed if the storage
				// turned out to hold the right key after all.
				writeWarnLog(fmt.Sprintf("[BackupKey] fetching the backup key failed: %v", ferr))
			} else if serr := storeBackupKey(m); serr != nil {
				writeWarnLog(fmt.Sprintf("[BackupKey] storing the fetched backup key failed: %v", serr))
			}
			fetched = true
			st = backupKeyStorage()

		case controlplane.BackupKeyFetchEphemeral:
			m, ferr := a.fetchBackupKey(controlplane.KeyModeEphemeral, runUUID)
			if ferr != nil {
				fetched = true
				writeWarnLog(fmt.Sprintf("[BackupKey] fetching the ephemeral backup key failed: %v", ferr))
				continue
			}
			raw, blob, herr := ephemeralKeyFromMaterial(m)
			if herr != nil {
				fetched = true
				writeWarnLog(fmt.Sprintf("[BackupKey] the delivered ephemeral key was unusable: %v", herr))
				continue
			}
			writeBackupLog(fmt.Sprintf(
				"[BackupKey] backup will be encrypted under key %s (ephemeral: held in memory for this run only)",
				shortKeyID(m.KeyID)))
			reportKeyStatus(ad, st)
			return raw, blob, nil

		case controlplane.BackupKeyRefuse:
			reportKeyStatus(ad, st)
			return nil, nil, errors.New(reason)
		}
	}

	// Reached only if the decision kept asking to fetch. Treat as a refusal
	// rather than looping: a gate that cannot converge must stop the backup,
	// not keep calling a rate-limited endpoint.
	reportKeyStatus(ad, st)
	return nil, nil, errors.New(
		"encrypted backup is required but this machine could not settle on a key to use")
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

// fetchBackupKey calls the control plane for material and verifies it before it
// goes anywhere.
func (a *App) fetchBackupKey(mode, runUUID string) (*controlplane.BackupKeyMaterial, error) {
	cpMu.Lock()
	c := cpClient
	cpMu.Unlock()
	if c == nil {
		return nil, errors.New("no control server is configured on this machine")
	}
	m, err := c.FetchBackupKey(controlplane.BackupKeyRequest{Mode: mode, RunUUID: runUUID})
	if err != nil {
		return nil, err
	}
	if m == nil || m.KeyID == "" {
		return nil, errors.New("the control server returned no backup key")
	}
	return m, nil
}

// reportKeyStatus tells the server what this machine found. Best-effort and
// never load-bearing: the decision has already been made by the time this runs,
// and a backup must not fail because a status report did.
func reportKeyStatus(ad *controlplane.BackupKeyAd, st controlplane.KeyStorage) {
	cpMu.Lock()
	c := cpClient
	cpMu.Unlock()
	if c == nil || ad == nil {
		return
	}
	rep := controlplane.KeyStatusFor(ad, st)
	if _, err := c.ReportKeyStatus(rep); err != nil {
		writeDebugLog(fmt.Sprintf("[BackupKey] key-status report failed: %v", err))
	}
}
