package pbscommon

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/scrypt"
)

// Manifest signing, pinned to upstream's own test vector.
//
// Dev rule 25 says a test that validates a format against an encoder we also
// own proves only self-consistency. This suite exists because
// pbs-datastore/src/manifest.rs carries `test_manifest_signature`, which
// asserts one specific hex string for one specific manifest under one specific
// key — a value computed by Proxmox's code, not ours.
//
// It is worth being explicit about how much that single assertion covers,
// because it is more than it looks. To reproduce the string we must agree with
// upstream on ALL of:
//
//   - the id_key derivation (pbkdf2 with the key as password, "_id_key" as
//     salt, exactly 10 iterations)
//   - that the tag is an HMAC, not the append-the-key digest used for chunks
//   - canonical JSON: sorted keys, no whitespace, no trailing anything
//   - that `signature` AND `unprotected` are both removed before signing
//   - the kebab-case field names (backup-id, backup-time, backup-type,
//     crypt-mode)
//   - the crypt-mode spellings "encrypt" and "none"
//   - hex-encoded csums, lowercase
//
// Any one of those wrong and the hex differs. That is the whole reason to
// transcribe a fixture from upstream rather than assert a round trip.

// upstreamManifestSignature is the constant asserted by
// pbs-datastore/src/manifest.rs::test_manifest_signature.
const upstreamManifestSignature = "d7b446fb7db081662081d4b40fedd858a1d6307a5aff4ecff7d5bf4fd35679e9"

// upstreamTestKey reproduces the key that test derives:
//
//	KeyDerivationConfig::Scrypt { n: 65536, r: 8, p: 1, salt: Vec::new() }
//	    .derive_key(b"test")
//
// A literal 32-byte key would have been simpler and would have proved less: the
// derivation is part of what makes the fixture a fixture. Note the EMPTY salt —
// that is upstream's, and it is fine for a test vector and would be indefensible
// anywhere near a real passphrase.
func upstreamTestKey(t *testing.T) []byte {
	t.Helper()
	key, err := scrypt.Key([]byte("test"), []byte{}, 65536, 8, 1, KeySize)
	if err != nil {
		t.Fatalf("scrypt: %v", err)
	}
	return key
}

// upstreamTestManifest is the manifest that test builds:
//
//	BackupManifest::new("host/elsa/2020-06-26T13:56:05Z")
//	  add_file("test1.img.fidx", 200, [1u8; 32], CryptMode::Encrypt)
//	  add_file("abc.blob",       200, [2u8; 32], CryptMode::None)
//	  unprotected["note"] = "This is not protected by the signature."
//
// The note is what proves `unprotected` is excluded: it is present, it is
// arbitrary, and the signature must not move when it changes.
func upstreamTestManifest() BackupManifest {
	return BackupManifest{
		BackupID:   "elsa",
		BackupTime: 1593179765, // 2020-06-26T13:56:05Z
		BackupType: "host",
		Files: []File{
			{CryptMode: "encrypt", Csum: strings.Repeat("01", 32), Filename: "test1.img.fidx", Size: 200},
			{CryptMode: "none", Csum: strings.Repeat("02", 32), Filename: "abc.blob", Size: 200},
		},
	}
}

func TestManifestSignatureMatchesUpstreamVector(t *testing.T) {
	cc, err := NewCryptConfig(upstreamTestKey(t))
	if err != nil {
		t.Fatalf("NewCryptConfig: %v", err)
	}

	plain, err := json.Marshal(upstreamTestManifest())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	doc, err := DecodeJSONDocument(plain)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	obj := doc.(map[string]any)
	delete(obj, "signature")
	delete(obj, "unprotected")

	canonical, err := ToCanonicalJSON(obj)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	tag := cc.ComputeAuthTag(canonical)
	got := hex.EncodeToString(tag[:])

	if got != upstreamManifestSignature {
		t.Fatalf("signature does not match upstream's vector\n got: %s\nwant: %s\ncanonical form was: %s",
			got, upstreamManifestSignature, canonical)
	}
}

// The auth tag and the chunk digest must not be the same function.
//
// Both take the id_key and some bytes and return 32 bytes, and a plausible
// "simplification" is to have one call the other. This pins them apart, because
// the failure would be silent: chunks would still dedup and manifests would
// still carry a stable-looking signature that no other client accepts.
func TestAuthTagIsNotTheChunkDigest(t *testing.T) {
	cc, err := NewCryptConfig(upstreamTestKey(t))
	if err != nil {
		t.Fatalf("NewCryptConfig: %v", err)
	}
	data := []byte("the same input to both constructions")
	if cc.ComputeAuthTag(data) == cc.ComputeDigest(data) {
		t.Fatal("ComputeAuthTag and ComputeDigest returned the same value")
	}
}

// EncodeManifest must sign, name the key, and leave an unkeyed client alone.
func TestEncodeManifestSignsAndFingerprints(t *testing.T) {
	pbs := &PBSClient{Manifest: upstreamTestManifest()}

	unsigned, err := pbs.EncodeManifest()
	if err != nil {
		t.Fatalf("EncodeManifest (no key): %v", err)
	}
	var before BackupManifest
	if err := json.Unmarshal(unsigned, &before); err != nil {
		t.Fatalf("unmarshal unsigned: %v", err)
	}
	if before.Signature != nil {
		t.Fatalf("a client with no key signed the manifest: %v", before.Signature)
	}
	if before.Unprotected.KeyFingerprint != "" {
		t.Fatalf("a client with no key named a key: %q", before.Unprotected.KeyFingerprint)
	}

	fp, err := pbs.SetCryptKey(upstreamTestKey(t))
	if err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}

	signedBytes, err := pbs.EncodeManifest()
	if err != nil {
		t.Fatalf("EncodeManifest (keyed): %v", err)
	}
	var after BackupManifest
	if err := json.Unmarshal(signedBytes, &after); err != nil {
		t.Fatalf("unmarshal signed: %v", err)
	}

	sig, ok := after.Signature.(string)
	if !ok {
		t.Fatalf("signature is %T, want a hex string", after.Signature)
	}
	// The stored signature must be the SAME value the upstream vector pins —
	// asserting only "not empty" would pass over a signature computed the wrong
	// way, which is precisely the mistake this file exists to catch.
	if sig != upstreamManifestSignature {
		t.Fatalf("stored signature %s, want %s", sig, upstreamManifestSignature)
	}
	if want := FingerprintString(fp); after.Unprotected.KeyFingerprint != want {
		t.Fatalf("key-fingerprint %q, want %q", after.Unprotected.KeyFingerprint, want)
	}
}

// Changing `unprotected` must not move the signature; changing a signed field
// must. Two directions, because a signature over everything and a signature
// over nothing both satisfy a one-sided test.
func TestSignatureCoversTheRightFields(t *testing.T) {
	key := upstreamTestKey(t)

	sign := func(m BackupManifest) string {
		pbs := &PBSClient{Manifest: m}
		if _, err := pbs.SetCryptKey(key); err != nil {
			t.Fatalf("SetCryptKey: %v", err)
		}
		out, err := pbs.EncodeManifest()
		if err != nil {
			t.Fatalf("EncodeManifest: %v", err)
		}
		var got BackupManifest
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return got.Signature.(string)
	}

	base := sign(upstreamTestManifest())

	withStats := upstreamTestManifest()
	withStats.Unprotected.ChunkUploadStats = ChunkUploadStats{Count: 17, Size: 4096}
	if sign(withStats) != base {
		t.Fatal("signature moved when only `unprotected` changed — it is not being excluded")
	}

	withOtherID := upstreamTestManifest()
	withOtherID.BackupID = "not-elsa"
	if sign(withOtherID) == base {
		t.Fatal("signature did not move when backup-id changed — signed fields are not covered")
	}

	withOtherCsum := upstreamTestManifest()
	withOtherCsum.Files[0].Csum = strings.Repeat("03", 32)
	if sign(withOtherCsum) == base {
		t.Fatal("signature did not move when a file csum changed")
	}
}

// indexCryptMode must describe the chunks, and blobs must stay honest.
func TestIndexCryptModeFollowsTheKey(t *testing.T) {
	plain := &PBSClient{}
	if got := plain.indexCryptMode(); got != "none" {
		t.Fatalf("unkeyed client index crypt-mode %q, want none", got)
	}

	keyed := &PBSClient{}
	if _, err := keyed.SetCryptKey(upstreamTestKey(t)); err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}
	if got := keyed.indexCryptMode(); got != "encrypt" {
		t.Fatalf("keyed client index crypt-mode %q, want encrypt", got)
	}
}

// FingerprintString must produce PBS's colon-separated form.
func TestFingerprintStringFormat(t *testing.T) {
	var fp [32]byte
	for i := range fp {
		fp[i] = byte(i)
	}
	got := FingerprintString(fp)
	if want := "00:01:02"; !strings.HasPrefix(got, want) {
		t.Fatalf("fingerprint %q does not start with %q", got, want)
	}
	if len(got) != 32*2+31 {
		t.Fatalf("fingerprint length %d, want %d", len(got), 32*2+31)
	}
	if strings.Count(got, ":") != 31 {
		t.Fatalf("fingerprint has %d colons, want 31", strings.Count(got, ":"))
	}
}
