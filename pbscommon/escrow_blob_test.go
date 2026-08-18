package pbscommon

import (
	"strings"
	"testing"
)

// rsa-encrypted.key.blob — the recovery path that survives losing our server.
//
// A customer holding their exported private master key decrypts this blob with
// stock proxmox-backup-client and gets the key that reads the snapshot: no
// Nimbus server, no database, no vault code in the path. Until it is written,
// that recovery depends on the very server it is supposed to outlive, which is
// the dependency the whole vault design exists to remove.
//
// The tests below are mostly about what must NOT happen to this blob, because
// every plausible mistake here is invisible until a recovery.

func TestEscrowBlobIsNotEncrypted(t *testing.T) {
	pbs := &PBSClient{}
	if _, err := pbs.SetCryptKey(upstreamTestKey(t)); err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}

	// A recognisable payload: the assertion is that it survives to the wire.
	escrow := []byte("RSA-ESCROWED-KEY-MATERIAL-not-really-but-distinctive")
	out, mode, err := pbs.encodeBlob(EncryptedKeyBlobName, escrow)
	if err != nil {
		t.Fatalf("encodeBlob: %v", err)
	}

	// THE CIRCULARITY CHECK. Encrypting this blob under the key it contains
	// would round-trip perfectly on this client and destroy the only recovery
	// path that works without our server — you would need the key to obtain
	// the key. Nothing local would notice.
	if !strings.Contains(string(out), string(escrow)) {
		t.Fatal("the escrow blob was encrypted — it must be readable without the backup key, " +
			"because it is what GIVES you the backup key")
	}

	// Upstream records the BACKUP's mode here, not this file's. It reads as a
	// contradiction and is deliberate: the contents really are encrypted, just
	// with RSA to the master key. Pinned because our own rule elsewhere is
	// "crypt-mode describes the bytes", and a future reader applying that rule
	// consistently would change this and diverge from stock PBS.
	if mode != "encrypt" {
		t.Fatalf("crypt-mode %q, want encrypt — upstream records crypto.mode for this blob", mode)
	}
}

// The name is a wire constant. A typo produces a snapshot that looks complete
// and that proxmox-backup-client will not find a key blob in.
func TestEscrowBlobNameMatchesUpstream(t *testing.T) {
	if EncryptedKeyBlobName != "rsa-encrypted.key.blob" {
		t.Fatalf("name is %q; upstream ENCRYPTED_KEY_BLOB_NAME is rsa-encrypted.key.blob", EncryptedKeyBlobName)
	}
}

// Both exemptions, and nothing else. A third exempt blob added carelessly
// would ship customer data in the clear from a client advertising encryption.
func TestOnlyTwoBlobsAreExemptFromEncryption(t *testing.T) {
	if !blobExemptFromEncryption(ManifestBlobName) {
		t.Error("the manifest must stay readable without a key")
	}
	if !blobExemptFromEncryption(EncryptedKeyBlobName) {
		t.Error("the escrow blob must stay readable without a key")
	}
	for _, name := range []string{
		"nimbus-status.json.blob",
		"nimbus-acls.json.gz.blob",
		"qemu-server.conf.blob",
		"index.json.blob.bak",
		"rsa-encrypted.key.blob.old",
	} {
		if blobExemptFromEncryption(name) {
			t.Errorf("%s is exempt from encryption and should not be", name)
		}
	}
}

// The manifest keeps crypt-mode "none" even on an encrypted client; the escrow
// blob does not. Two exemptions from encryption, two different manifest
// answers — asserted together so neither is "simplified" into the other.
func TestTheTwoExemptionsRecordDifferentCryptModes(t *testing.T) {
	pbs := &PBSClient{}
	if _, err := pbs.SetCryptKey(upstreamTestKey(t)); err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}

	_, manifestMode, err := pbs.encodeBlob(ManifestBlobName, []byte(`{"backup-id":"x"}`))
	if err != nil {
		t.Fatalf("encodeBlob manifest: %v", err)
	}
	_, escrowMode, err := pbs.encodeBlob(EncryptedKeyBlobName, []byte("escrow"))
	if err != nil {
		t.Fatalf("encodeBlob escrow: %v", err)
	}

	if manifestMode != "none" {
		t.Errorf("manifest crypt-mode %q, want none", manifestMode)
	}
	if escrowMode != "encrypt" {
		t.Errorf("escrow crypt-mode %q, want encrypt", escrowMode)
	}
	if manifestMode == escrowMode {
		t.Error("both exemptions record the same crypt-mode; upstream distinguishes them")
	}
}

// Refusals, both of which prevent a snapshot that LOOKS recoverable.
func TestUploadEscrowBlobRefusesUselessInput(t *testing.T) {
	keyed := &PBSClient{}
	if _, err := keyed.SetCryptKey(upstreamTestKey(t)); err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}

	// An empty blob is worse than no blob: it looks like a recovery path is
	// present right up until someone needs it.
	err := keyed.UploadEscrowBlob(nil)
	if err == nil {
		t.Fatal("an empty escrow blob was accepted")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}

	// An escrow blob on an unencrypted snapshot describes a key nothing used.
	// Skipping silently would hide a caller bug.
	plain := &PBSClient{}
	err = plain.UploadEscrowBlob([]byte("escrow"))
	if err == nil {
		t.Fatal("an escrow blob was accepted for an unencrypted backup")
	}
}
