package pbscommon

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
)

// captureAudit installs a collector on AuditLogFn for the duration of a test.
//
// The hook is a package-level var, so restore it — a leaked collector makes a
// later test's failure depend on which tests ran before it.
func captureAudit(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	lines := []string{}
	prev := AuditLogFn
	AuditLogFn = func(s string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, s)
	}
	t.Cleanup(func() { AuditLogFn = prev })
	return &lines
}

// The CRC of an encrypted blob must be real, and must cover the CIPHERTEXT
// only — from EncryptedHeaderLen, not from byte 12.
//
// Both halves are pinned because both were wrong in the shipped revision this
// replaces: the CRC was written as zero, and the obvious fix (copy the offset
// the unencrypted paths use) would have produced a checksum that verifies
// against itself and against no Proxmox server. Upstream's compute_crc starts
// at header_size(magic), which for an encrypted magic is 44.
func TestEncryptedBlobCRCCoversCiphertextOnly(t *testing.T) {
	cc, err := NewCryptConfig(upstreamTestKey(t))
	if err != nil {
		t.Fatalf("NewCryptConfig: %v", err)
	}
	blob, err := cc.EncodeEncryptedBlob([]byte("payload that needs a checksum"))
	if err != nil {
		t.Fatalf("EncodeEncryptedBlob: %v", err)
	}

	stored := binary.LittleEndian.Uint32(blob[8:12])
	if stored == 0 {
		t.Fatal("CRC written as zero — upstream sets a real one on every blob variant")
	}
	if want := BlobCRC(blob[EncryptedHeaderLen:]); stored != want {
		t.Fatalf("CRC %08x does not match a checksum over the ciphertext (%08x)", stored, want)
	}
	// The distinguishing assertion: a CRC taken from byte 12 would also be
	// non-zero and would also be stable, so "not zero" alone proves nothing.
	if from12 := BlobCRC(blob[12:]); stored == from12 {
		t.Fatal("CRC covers the IV and tag — it must start at EncryptedHeaderLen")
	}
}

func TestDecodeRejectsACorruptedCiphertextByCRC(t *testing.T) {
	cc, err := NewCryptConfig(upstreamTestKey(t))
	if err != nil {
		t.Fatalf("NewCryptConfig: %v", err)
	}
	blob, err := cc.EncodeEncryptedBlob([]byte("payload that needs a checksum"))
	if err != nil {
		t.Fatalf("EncodeEncryptedBlob: %v", err)
	}
	if _, err := cc.DecodeEncryptedBlob(blob); err != nil {
		t.Fatalf("round trip failed before corruption: %v", err)
	}

	damaged := append([]byte(nil), blob...)
	damaged[EncryptedHeaderLen] ^= 0xff
	_, err = cc.DecodeEncryptedBlob(damaged)
	if err == nil {
		t.Fatal("a corrupted blob decoded")
	}
	// The message must distinguish damage from a wrong key. Both are refusals;
	// they send an operator to completely different places.
	if !strings.Contains(err.Error(), "CRC") {
		t.Fatalf("corruption reported as %q — indistinguishable from a wrong key", err)
	}
}

// Every blob except the manifest is encrypted when a key is set, and the
// manifest entry's crypt-mode describes the bytes rather than the client.
func TestEncodeBlobEncryptsEverythingButTheManifest(t *testing.T) {
	pbs := &PBSClient{}

	// With no key, nothing is encrypted and nothing claims to be.
	out, mode, err := pbs.encodeBlob("nimbus-status.json.blob", []byte("status"))
	if err != nil {
		t.Fatalf("encodeBlob: %v", err)
	}
	if mode != "none" {
		t.Fatalf("unkeyed client reported crypt-mode %q", mode)
	}
	if !strings.Contains(string(out), "status") {
		t.Fatal("unkeyed blob is not stored as cleartext")
	}

	if _, err := pbs.SetCryptKey(upstreamTestKey(t)); err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}

	for _, name := range []string{
		"nimbus-status.json.blob",
		"nimbus-acls.json.gz.blob",
		"qemu-server.conf.blob",
	} {
		payload := []byte("secret contents of " + name)
		out, mode, err := pbs.encodeBlob(name, payload)
		if err != nil {
			t.Fatalf("encodeBlob %s: %v", name, err)
		}
		if mode != "encrypt" {
			t.Fatalf("%s: crypt-mode %q, want encrypt", name, mode)
		}
		// Assert on the BYTES, not on the mode we just read back from the same
		// function. A mode string is a claim; the absence of the plaintext is
		// the fact.
		if strings.Contains(string(out), "secret contents") {
			t.Fatalf("%s: plaintext is still present in the stored blob", name)
		}
		round, err := pbs.crypt.DecodeEncryptedBlob(out)
		if err != nil {
			t.Fatalf("%s: stored blob does not decrypt: %v", name, err)
		}
		if string(round) != string(payload) {
			t.Fatalf("%s: round trip changed the payload", name)
		}
	}

	// The manifest is the exemption, and it must survive being on an encrypted
	// client — a datastore has to parse it without a key.
	out, mode, err = pbs.encodeBlob(ManifestBlobName, []byte(`{"backup-id":"x"}`))
	if err != nil {
		t.Fatalf("encodeBlob manifest: %v", err)
	}
	if mode != "none" {
		t.Fatalf("manifest crypt-mode %q, want none", mode)
	}
	if !strings.Contains(string(out), `"backup-id"`) {
		t.Fatal("the manifest was encrypted — it must stay readable without a key")
	}
}

// The audit trail must exist, name the key by fingerprint, and never carry key
// material.
func TestAuditTrailRecordsCryptoDecisionsWithoutKeyMaterial(t *testing.T) {
	lines := captureAudit(t)

	key := upstreamTestKey(t)
	pbs := &PBSClient{Manifest: upstreamTestManifest()}
	fp, err := pbs.SetCryptKey(key)
	if err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}
	if _, _, err := pbs.encodeBlob("nimbus-status.json.blob", []byte("status")); err != nil {
		t.Fatalf("encodeBlob: %v", err)
	}
	if _, err := pbs.EncodeManifest(); err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}

	joined := strings.Join(*lines, "\n")
	if len(*lines) == 0 {
		t.Fatal("no audit lines were emitted")
	}
	for _, want := range []string{"ENABLED", FingerprintString(fp), "SIGNED"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("audit trail is missing %q:\n%s", want, joined)
		}
	}

	// Key material, in any encoding a careless format verb might produce.
	forbidden := map[string]string{
		"raw key (hex)": hex.EncodeToString(key),
		"raw key bytes": string(key),
	}
	idKey := pbs.crypt.idKey
	forbidden["id_key (hex)"] = hex.EncodeToString(idKey[:])
	forbidden["id_key bytes"] = string(idKey[:])
	for what, secret := range forbidden {
		if strings.Contains(joined, secret) {
			t.Fatalf("audit trail leaked %s:\n%s", what, joined)
		}
	}
}

// A nil hook must be silent, not a panic. The CLI tools and every test that
// does not install a collector run this way.
func TestAuditIsSilentWhenUnset(t *testing.T) {
	prev := AuditLogFn
	AuditLogFn = nil
	defer func() { AuditLogFn = prev }()

	pbs := &PBSClient{Manifest: upstreamTestManifest()}
	if _, err := pbs.SetCryptKey(upstreamTestKey(t)); err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}
	if _, err := pbs.EncodeManifest(); err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}
}
