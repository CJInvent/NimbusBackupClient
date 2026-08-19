//go:build service
// +build service

package main

// Attaching the right key to a restore-side PBS client (V4-SPEC §18, phase G).
//
// ONE FUNCTION, CALLED IMMEDIATELY AFTER Connect, at every site that reads a
// snapshot. That shape is deliberate and it is the same argument the backup
// gate makes: a step that has to happen on several paths and is easy to leave
// off one of them should be one line that is obviously present or obviously
// absent, not a policy each caller re-implements. There are three such sites —
// withSnapshotReader (archive), listSnapshotViaCatalog (catalog) and
// openImageReader (image, including the browses NimbusControl drives remotely)
// — and gui/restorekey_wiring_test.go pins each one.
//
// IT IS ALSO THE ONLY PLACE THAT DECIDES A SNAPSHOT IS UNENCRYPTED, and it
// decides that from the snapshot's own manifest rather than from anything this
// machine believes about itself. A machine whose org turned encryption on last
// week still has older snapshots that were written in the clear, and a restore
// that consulted the machine's current setting instead of the snapshot's would
// fail on every one of them.

import (
	"fmt"

	"pbscommon"
)

// attachRestoreKey works out which key a snapshot needs, obtains it, and
// configures the client to decrypt with it.
//
// The client must already be Connected as a reader — the manifest is read
// through that session.
//
// Returns nil for an unencrypted snapshot, having set no key. Every other
// outcome is an error, and a restore must NOT proceed on one: continuing
// without a key against an encrypted snapshot produces "chunk is encrypted but
// this client has no encryption key configured" from somewhere deep in a
// reader, at which point the operator is reading a message about chunks when
// the answer is about keys.
func attachRestoreKey(client *pbscommon.PBSClient, logTag string) error {
	manifestBytes, err := client.FetchManifestBytes()
	if err != nil {
		return fmt.Errorf("could not read the snapshot manifest, so the key it needs is unknown: %w", err)
	}
	manifest, err := pbscommon.ParseManifest(manifestBytes)
	if err != nil {
		return err
	}

	fingerprint := pbscommon.ManifestKeyFingerprint(manifest)
	if fingerprint == "" {
		writeBackupLog(fmt.Sprintf("%s: snapshot is not encrypted", logTag))
		return nil
	}

	raw, err := keyForFingerprint(fingerprint)
	if err != nil {
		return err
	}
	if _, err := client.SetCryptKey(raw); err != nil {
		return err
	}

	// AFTER the key is attached, because it cannot be checked before. This is
	// the one integrity check a restore gets over the snapshot as a WHOLE:
	// every chunk authenticates itself, so a chunk substituted from another
	// snapshot under the same key would verify, and only the manifest ties the
	// set of files together.
	//
	// A failure here stops the restore. That is a deliberate choice against
	// the softer "warn and continue": the value of the check is entirely in
	// what it refuses, and a warning on a screen nobody is watching during an
	// unattended server-driven restore refuses nothing.
	if err := client.VerifyManifestSignature(manifestBytes); err != nil {
		return fmt.Errorf("%s: refusing to restore from this snapshot: %w", logTag, err)
	}
	writeBackupLog(fmt.Sprintf("%s: snapshot is encrypted under key %s; manifest signature verified", logTag, fingerprint))
	return nil
}
