//go:build service

package main

// The run-time half of the verification gate (V4-SPEC §11, phase F item 3).
//
// WHY THIS IS A SEPARATE FILE AND NOT THE REST OF backupkey_gate.go: everything
// here is reachable only from runBackupPipeline, which is itself `service`-only.
// Left untagged, these symbols have no caller in the default (GUI) build, and
// the default golangci-lint pass — the one that still runs `unused`, see
// .github/workflows/build-and-release.yml — reports every one of them. Tagging
// them to match their single caller keeps that check meaningful in both passes
// instead of teaching people to ignore it.
//
// fetchKeyMaterial is the exception that proves the rule and now lives in
// backupkey_gate.go, untagged: restore needs it too (phase G), and restore is
// in both builds.

import (
	"controlplane"
	"errors"
	"fmt"
)

// maxKeyAttempts bounds the fetch-then-decide loop. The pure decision already
// refuses on the second pass via alreadyFetched; this is the belt to that
// braces, so a decision bug cannot turn into an unbounded hammering of a
// rate-limited endpoint.
const maxKeyAttempts = 2

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
			m, ferr := fetchKeyMaterial(controlplane.KeyModeDurable, runUUID)
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
			m, ferr := fetchKeyMaterial(controlplane.KeyModeEphemeral, runUUID)
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

// fetchKeyMaterial calls the control plane for key material and checks it is
// material at all before it goes anywhere.
//
// UNTAGGED, unlike the rest of the gate's run-time half, because RESTORE needs
// it too and restore lives in both builds. It was a method on *App and did not
// use the receiver — the control-plane client is package state — so nothing was
// lost by making it a function, and keeping it as a method would have meant a
// second copy for the restore path to call.
//
// `mode` decides how the server rate-limits and what the release audit records;
// see controlplane.KeyMode* for what each one means.
func fetchKeyMaterial(mode, runUUID string) (*controlplane.BackupKeyMaterial, error) {
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
