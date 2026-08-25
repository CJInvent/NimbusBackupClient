//go:build service
// +build service

package main

// SOURCE-LEVEL PINS for the three restore reader entry points (phase G).
//
// SAME HONEST LIMITATION AS backupkey_wiring_test.go, and for the same reason:
// these call sites need a live PBS and a real snapshot to execute, so no test
// here reaches them. What they pin is the regression that costs the most and
// shows the least — a reader session that stops asking which key the snapshot
// needs. That failure does not break a build, does not fail a unit test, and
// surfaces as "chunk is encrypted but this client has no encryption key
// configured" from deep inside a reader, months later, to a customer in the
// middle of a restore.
//
// This is exactly the gap phase G was opened to close: SetCryptKey had two
// call sites, both in backup engines, and nothing anywhere noticed that no
// restore path had one. A test that had read the source would have.
//
// Replace with an end-to-end restore of an encrypted snapshot when a live PBS
// is available — that closes phase E's caveat and these pins together.

import (
	"strings"
	"testing"
)

// mustContain asserts a fact the compiler cannot: that a specific call is
// still present in a specific file.
func mustContain(t *testing.T, src, file, needle, why string) {
	t.Helper()
	if !strings.Contains(src, needle) {
		t.Errorf("%s: %q is gone. %s", file, needle, why)
	}
}

// The archive reader — every file restore, every content listing, every search
// goes through withSnapshotReader.
func TestSnapshotReaderAttachesTheKeyBeforeReading(t *testing.T) {
	src := sourceOf(t, "restore_inline.go")
	const why = "withSnapshotReader is the single door to every file restore; without the key " +
		"an encrypted snapshot fails at the first chunk with a message about chunks, not keys"

	mustContain(t, src, "restore_inline.go", "attachRestoreKey(client, logTag)", why)
	mustPrecede(t, src, "restore_inline.go",
		"attachRestoreKey(client, logTag)", "client.NewDIDXReaderAt(archiveName",
		"the key must be attached BEFORE the reader opens, or the first chunk fetch fails.")
}

// The catalog reader — the fast listing path. It has its own client, so it
// needs its own attach; sharing withSnapshotReader's would have been a
// different design and is not the one here.
func TestCatalogReaderAttachesTheKey(t *testing.T) {
	src := sourceOf(t, "restore_inline.go")
	mustContain(t, src, "restore_inline.go", `attachRestoreKey(client, "catalog")`,
		"listSnapshotViaCatalog opens its own PBS client; an unkeyed one cannot read an encrypted catalog "+
			"and would silently fall back to walking the whole data archive on every listing")
	mustPrecede(t, src, "restore_inline.go",
		`attachRestoreKey(client, "catalog")`, `client.NewDIDXReaderAt("catalog.pcat1.didx"`,
		"the key must be attached before the catalog reader opens.")
}

// The image reader — disk-image browse, download and restore, plus the
// server-driven browse commands that reach it through controlplane_browse.go.
func TestImageReaderAttachesTheKey(t *testing.T) {
	src := sourceOf(t, "imagebrowse_core.go")
	mustContain(t, src, "imagebrowse_core.go", `attachRestoreKey(client, "image-browse")`,
		"openVolumeReader is the single door to every volume browse and file extraction, including the ones "+
			"NimbusControl drives remotely, where nobody is watching a screen for a warning")
	mustPrecede(t, src, "imagebrowse_core.go",
		`attachRestoreKey(client, "image-browse")`, "client.NewFIDXReaderAt(diskArchive",
		"the key must be attached before the image reader opens.")
}

// The manifest signature check must stay INSIDE attachRestoreKey and after the
// key is set. Moved earlier it cannot work (no key yet); removed, the only
// whole-snapshot integrity check a restore has disappears without failing
// anything — every chunk still authenticates itself, so nothing else notices.
func TestAttachVerifiesTheManifestSignature(t *testing.T) {
	src := sourceOf(t, "restorekey_attach.go")
	mustContain(t, src, "restorekey_attach.go", "client.VerifyManifestSignature(manifestBytes)",
		"without it, a manifest edited after the backup — a file removed, a size rewritten — restores cleanly")
	mustPrecede(t, src, "restorekey_attach.go",
		"client.SetCryptKey(raw)", "client.VerifyManifestSignature(manifestBytes)",
		"the signature cannot be checked before the key that signs it is loaded.")
}

// A restore must decide encryption from the SNAPSHOT, never from what this
// machine currently believes about its org. A machine that turned encryption
// on last week still has older snapshots written in the clear, and one that
// turned it off still has to read the encrypted ones it already took.
func TestAttachReadsTheSnapshotNotTheMachineSetting(t *testing.T) {
	src := sourceOf(t, "restorekey_attach.go")
	mustContain(t, src, "restorekey_attach.go", "pbscommon.ManifestKeyFingerprint(manifest)",
		"the snapshot's own manifest is the only correct source for which key it needs")
	for _, forbidden := range []string{"persistedEncryption()", "currentBackupKeyAd()", "backupKeyStorage()"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("restorekey_attach.go consults %s — that is this machine's CURRENT state, "+
				"not the state of the snapshot being restored", forbidden)
		}
	}
}
