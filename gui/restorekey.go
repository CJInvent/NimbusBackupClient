package main

// Which key opens THIS snapshot (V4-SPEC §18, phase G).
//
// Phase E made every new snapshot encrypted and phase F delivered the key that
// encrypts them. Neither touched restore, and the gap that left is the reason
// this file exists: `SetCryptKey` was called from exactly two places, both
// backup engines, so a snapshot this product wrote was a snapshot this product
// could not read back. A backup you cannot restore is not a backup.
//
// THE LOOKUP IS BY FINGERPRINT, NOT BY "THE KEY WE HAPPEN TO HOLD."
// A machine holds one key today, so "use it" would work today and would be the
// wrong shape tomorrow: the moment a key is rotated, a snapshot older than the
// rotation needs a key this machine no longer has, and code that never asked
// which key it needed has nowhere to put that question. So every restore reads
// the fingerprint out of the manifest first and asks for THAT key by name. The
// sources it asks are a list (keySources below) — adding the server's
// historical-key endpoint, or an operator-supplied recovery bundle, is a new
// entry in that list and no change to any caller.
//
// A WRONG KEY IS REFUSED BEFORE IT IS USED. The fingerprint is checked against
// the candidate's own derived fingerprint, so a mismatch is caught here, once,
// with both values named — instead of surfacing as an AEAD failure on some
// chunk halfway through a restore, which reads like data corruption and sends
// an operator to entirely the wrong place.

import (
	"errors"
	"fmt"

	"controlplane"
	"pbscommon"
)

// ErrNoKeyForSnapshot is the refusal when a snapshot names a key and no source
// could produce it. Distinct from every "the restore failed" error because the
// operator's next action is completely different: this one is answered by
// reaching the management server, or by a recovery bundle, not by retrying.
var ErrNoKeyForSnapshot = errors.New("no source could supply the key this snapshot was encrypted with")

// keySource is one place a key can come from. It reports the raw key material
// for a fingerprint, or ok=false if it does not have that key.
//
// AN ERROR AND ok=false ARE DIFFERENT ANSWERS. ok=false means "not mine, ask
// the next one" — an ordinary outcome. An error means the source could not be
// CONSULTED, which is worth telling the operator about even when a later source
// succeeds, because a silently skipped key store is how a machine ends up
// depending on the network for something it has on disk.
type keySource struct {
	name string
	get  func(fingerprint string) (raw []byte, ok bool, err error)
}

// keySources is the search order. It is a variable so tests can supply a
// source without a DPAPI protector or a control plane, and so the historical
// and recovery-bundle sources can be appended when they are built.
//
// ORDER IS DELIBERATE: local before remote. A machine that already holds the
// right key must not need the management server to restore with it — that is
// the same outage tolerance durable storage was chosen for in phase F, and
// reversing it here would quietly undo that decision on the one path where it
// matters most.
var keySources = []keySource{
	{name: "this machine's key store", get: storedKeyForFingerprint},
	{name: "the management server", get: activeKeyForFingerprint},
}

// keyForFingerprint finds the raw key material a snapshot needs.
//
// A fingerprint of "" means the snapshot names no key: it is unencrypted, and
// (nil, nil) is the correct, successful answer. Restores of snapshots written
// before phase E take this path and must not be made to look for a key.
//
// Returns RAW MATERIAL rather than a built CryptConfig so the caller hands it
// to PBSClient.SetCryptKey, which is the same door the backup engines use. Two
// ways to put a key into a client would be two things to keep in step, and the
// one that already exists writes the audit line naming the key.
func keyForFingerprint(fingerprint string) ([]byte, error) {
	if fingerprint == "" {
		return nil, nil
	}

	var consulted []error
	for _, src := range keySources {
		raw, ok, err := src.get(fingerprint)
		if err != nil {
			// Recorded, not returned: a later source may still hold the key,
			// and an operator reading a final refusal needs to know that a
			// store was unreadable rather than merely empty.
			consulted = append(consulted, fmt.Errorf("%s: %w", src.name, err))
			continue
		}
		if !ok {
			continue
		}
		cc, err := pbscommon.NewCryptConfig(raw)
		if err != nil {
			consulted = append(consulted, fmt.Errorf("%s: %w", src.name, err))
			continue
		}
		if got := pbscommon.FingerprintString(cc.Fingerprint()); got != fingerprint {
			// A source claiming a fingerprint it does not actually derive to
			// is a bug in that source, not a missing key, and it must never
			// be quietly treated as "try the next one".
			return nil, fmt.Errorf(
				"%s offered a key for %s but the material derives to %s", src.name, fingerprint, got)
		}
		writeBackupLog(fmt.Sprintf("[Restore] snapshot key %s supplied by %s", fingerprint, src.name))
		return raw, nil
	}

	return nil, fmt.Errorf("%w (fingerprint %s): %v", ErrNoKeyForSnapshot, fingerprint, errors.Join(consulted...))
}

// storedKeyForFingerprint answers from this machine's own key store.
//
// THREE OUTCOMES, and the difference between them is the whole point of asking
// this source first:
//
//   - nothing stored, or stored-and-not-this-key → ok=false, no error. An
//     ordinary state; the next source gets a turn.
//   - the store cannot be CONSULTED → an error, reported even though a later
//     source may succeed. The case that matters is a GUI on a machine where
//     the key file belongs to the service (gui/keyacl_windows.go grants only
//     SYSTEM and Administrators), and an operator has to see that verbatim —
//     it is a permissions answer, not a missing-key answer, and the two send
//     you to different places.
//   - the right key → ok=true.
//
// A machine with no durable protector at all is the first case, not the
// second: an ephemeral machine is SUPPOSED to hold nothing, so reporting its
// empty store as a fault would put a scary line in front of every restore on a
// machine that is working exactly as designed.
func storedKeyForFingerprint(fingerprint string) ([]byte, bool, error) {
	state, _, err := persistedEncryption()
	if err != nil {
		return nil, false, err
	}
	if state != encStateOn {
		// Either nothing has ever been recorded, or this org does not encrypt.
		// Neither is a key, and neither is a fault.
		return nil, false, nil
	}

	raw, _, _, err := loadBackupKey()
	if err != nil {
		if errors.Is(err, errNoDurableStorage) {
			return nil, false, nil // ephemeral machine: nothing is stored here by design
		}
		return nil, false, err
	}
	cc, err := pbscommon.NewCryptConfig(raw)
	if err != nil {
		return nil, false, err
	}
	if pbscommon.FingerprintString(cc.Fingerprint()) != fingerprint {
		// Held a key, but not this one. The overwhelmingly likely cause is a
		// rotation, and saying so costs nothing and saves an operator a
		// wrong-turn investigation into the datastore.
		writeBackupLog(fmt.Sprintf(
			"[Restore] this machine holds a key, but not the one snapshot %s needs — the key was probably rotated after this backup",
			fingerprint))
		return nil, false, nil
	}
	return raw, true, nil
}

// activeKeyForFingerprint asks the management server for the key it currently
// issues, and accepts it only if it is the one this snapshot needs.
//
// THIS IS THE CURRENT KEY, NOT AN ARBITRARY ONE. The agent API serves the
// active key and nothing else, which is right for backup and only half of what
// restore needs. Asking for a historical key is a server change (an endpoint
// that will serve a RETIRED key for restore while still refusing to issue one
// for backup) and belongs in the same list as this, not inside it.
//
// The path matters most for ephemeral machines, which by design hold no key at
// rest and would otherwise be unable to restore even their own latest backup.
func activeKeyForFingerprint(fingerprint string) ([]byte, bool, error) {
	m, err := fetchKeyMaterial(controlplane.KeyModeRestore, "")
	if err != nil {
		return nil, false, err
	}
	raw, _, err := ephemeralKeyFromMaterial(m)
	if err != nil {
		return nil, false, err
	}
	cc, err := pbscommon.NewCryptConfig(raw)
	if err != nil {
		return nil, false, err
	}
	if pbscommon.FingerprintString(cc.Fingerprint()) != fingerprint {
		return nil, false, nil
	}
	return raw, true, nil
}
