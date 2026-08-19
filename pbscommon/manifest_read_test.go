package pbscommon

// Tests for the manifest read path (phase G).
//
// THE STRONGEST ASSERTION IN THIS FILE is that VerifyManifestSignature accepts
// UPSTREAM'S OWN published vector — the same constant manifest_sign_test.go
// pins for the write side. That is what makes it a test rather than a mirror:
// a verifier checked only against our own signer proves the two agree with
// each other and nothing about whether either agrees with PBS, which is dev
// rule 25 exactly. A restore that rejects a snapshot stock
// proxmox-backup-client wrote, or accepts one it would reject, is a bug this
// suite has to be able to see.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// --------------------------------------------------------------- blob decode

// DecodePlainBlob must read what this client's own writer produces. Not a
// strong claim on its own (see the file comment) — it is here because the
// alternative, a decoder tested only against hand-built bytes, would keep
// passing on the day encodeBlob's header changes.
func TestDecodePlainBlobReadsWhatEncodeBlobWrites(t *testing.T) {
	pbs := &PBSClient{} // no key: encodeBlob takes the unencrypted path
	payload := []byte("a manifest would be JSON, but any bytes exercise the header")

	blob, mode, err := pbs.encodeBlob(ManifestBlobName, payload)
	if err != nil {
		t.Fatalf("encodeBlob: %v", err)
	}
	if mode != "none" {
		t.Fatalf("crypt-mode %q for an unkeyed client, want \"none\"", mode)
	}

	got, err := DecodePlainBlob(blob)
	if err != nil {
		t.Fatalf("DecodePlainBlob: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round trip changed the payload: got %q, want %q", got, payload)
	}
}

// The manifest is exempt from encryption, so a KEYED client must still produce
// a manifest blob this reader can open. If that exemption ever stops holding,
// a restore can no longer learn which key it needs — the ordering problem the
// whole design turns on — so it is asserted here and not only on the write
// side.
func TestManifestBlobStaysReadableFromAKeyedClient(t *testing.T) {
	pbs := &PBSClient{Manifest: upstreamTestManifest()}
	if _, err := pbs.SetCryptKey(upstreamTestKey(t)); err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}

	signed, err := pbs.EncodeManifest()
	if err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}
	blob, _, err := pbs.encodeBlob(ManifestBlobName, signed)
	if err != nil {
		t.Fatalf("encodeBlob: %v", err)
	}

	plain, err := DecodePlainBlob(blob)
	if err != nil {
		t.Fatalf("a keyed client's manifest blob was not plainly readable: %v", err)
	}
	m, err := ParseManifest(plain)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if ManifestKeyFingerprint(m) == "" {
		t.Fatal("the manifest of an encrypted snapshot named no key")
	}
}

// An encrypted blob handed to the plain decoder must be REFUSED, not decoded
// as garbage and not silently decrypted. The magic is the only thing that
// distinguishes them, so this pins that the switch actually reads it.
func TestDecodePlainBlobRefusesAnEncryptedBlob(t *testing.T) {
	cc, err := NewCryptConfig(upstreamTestKey(t))
	if err != nil {
		t.Fatalf("NewCryptConfig: %v", err)
	}
	blob, err := cc.EncodeEncryptedBlob([]byte("secret"))
	if err != nil {
		t.Fatalf("EncodeEncryptedBlob: %v", err)
	}

	got, err := DecodePlainBlob(blob)
	if err == nil {
		t.Fatalf("an encrypted blob decoded as plain, returning %q", got)
	}
	if !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

// A flipped byte must be caught by the CRC. Asserting only "it errored" would
// pass if the magic check happened to reject it instead, so the message is
// pinned — the same lesson the key store's AEAD sabotage taught.
func TestDecodePlainBlobCatchesADamagedPayload(t *testing.T) {
	pbs := &PBSClient{}
	blob, _, err := pbs.encodeBlob(ManifestBlobName, []byte("payload worth checking"))
	if err != nil {
		t.Fatalf("encodeBlob: %v", err)
	}
	blob[len(blob)-1] ^= 0x01

	_, err = DecodePlainBlob(blob)
	if err == nil {
		t.Fatal("a damaged blob decoded without complaint")
	}
	if !strings.Contains(err.Error(), "CRC mismatch") {
		t.Fatalf("a damaged payload was rejected by something other than the CRC: %v", err)
	}
}

// A truncated response must not panic. The header is 12 bytes and the slice
// arithmetic below it assumes they are there.
func TestDecodePlainBlobRefusesAShortBlob(t *testing.T) {
	for _, n := range []int{0, 1, 11} {
		if _, err := DecodePlainBlob(make([]byte, n)); err == nil {
			t.Fatalf("a %d-byte blob decoded without complaint", n)
		}
	}
}

// A compressed blob must decompress. This client does not write them, but PBS
// does, and a restore reads whatever is in the datastore rather than only what
// we put there.
func TestDecodePlainBlobDecompresses(t *testing.T) {
	payload := []byte(strings.Repeat("compressible ", 200))
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	compressed := enc.EncodeAll(payload, nil)
	enc.Close()

	blob := make([]byte, 0, 12+len(compressed))
	blob = append(blob, CompressedBlobMagic[:]...)
	blob = binary.LittleEndian.AppendUint32(blob, crc32.ChecksumIEEE(compressed))
	blob = append(blob, compressed...)

	got, err := DecodePlainBlob(blob)
	if err != nil {
		t.Fatalf("DecodePlainBlob (compressed): %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("compressed round trip changed the payload")
	}
}

// ---------------------------------------------------------- key fingerprint

// "" is a real answer meaning "unencrypted", and it has to survive a manifest
// that has no `unprotected` block at all — which is what an older snapshot
// looks like.
func TestManifestKeyFingerprintOnAnUnencryptedSnapshot(t *testing.T) {
	m, err := ParseManifest([]byte(`{"backup-id":"x","backup-time":1,"backup-type":"host","files":[]}`))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if fp := ManifestKeyFingerprint(m); fp != "" {
		t.Fatalf("an unencrypted snapshot named key %q", fp)
	}
	if fp := ManifestKeyFingerprint(nil); fp != "" {
		t.Fatalf("a nil manifest named key %q", fp)
	}
}

// ------------------------------------------------------ signature verification

// THE LOAD-BEARING TEST. The manifest is upstream's, the key is upstream's,
// and the signature this verifier must accept is upstream's published
// constant — so this passes only if our verification agrees with PBS's, not
// merely with our own signer.
func TestVerifyAcceptsUpstreamsOwnSignedManifest(t *testing.T) {
	m := upstreamTestManifest()
	m.Signature = upstreamManifestSignature

	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	pbs := &PBSClient{}
	if _, err := pbs.SetCryptKey(upstreamTestKey(t)); err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}
	if err := pbs.VerifyManifestSignature(raw); err != nil {
		t.Fatalf("upstream's own signed manifest was rejected: %v", err)
	}
}

// `unprotected` is excluded from the signature, so a manifest carrying a
// key-fingerprint (and anything else PBS puts there) must still verify. A
// verifier that forgot the delete would reject every real snapshot — and
// would still pass a test built from a manifest with an empty unprotected
// block, which is why this case is separate.
func TestVerifyIgnoresTheUnprotectedBlock(t *testing.T) {
	pbs := &PBSClient{Manifest: upstreamTestManifest()}
	fp, err := pbs.SetCryptKey(upstreamTestKey(t))
	if err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}
	signed, err := pbs.EncodeManifest()
	if err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}

	var m BackupManifest
	if err := json.Unmarshal(signed, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Unprotected.KeyFingerprint != FingerprintString(fp) {
		t.Fatal("the signed manifest did not carry the key fingerprint this test needs")
	}
	if err := pbs.VerifyManifestSignature(signed); err != nil {
		t.Fatalf("a manifest with a populated unprotected block was rejected: %v", err)
	}
}

// The wrong key must be told apart from a tampered manifest by nothing — both
// are a signature mismatch — but the mismatch itself must be reported, and
// must name both values so an operator can act on it.
func TestVerifyRejectsTheWrongKey(t *testing.T) {
	m := upstreamTestManifest()
	m.Signature = upstreamManifestSignature
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	other := make([]byte, KeySize)
	for i := range other {
		other[i] = byte(i + 1)
	}
	pbs := &PBSClient{}
	if _, err := pbs.SetCryptKey(other); err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}

	err = pbs.VerifyManifestSignature(raw)
	if err == nil {
		t.Fatal("a manifest verified under a key that did not sign it")
	}
	if !strings.Contains(err.Error(), "signature does not match") {
		t.Fatalf("wrong-key rejection came from somewhere else: %v", err)
	}
	if !strings.Contains(err.Error(), upstreamManifestSignature) {
		t.Fatalf("the rejection does not name the recorded signature: %v", err)
	}
}

// A SIGNED field changed after the backup must be caught. This is the only
// integrity check a restore has over the snapshot as a whole — every chunk
// authenticates itself, so a file list with an entry removed is invisible
// everywhere else.
func TestVerifyCatchesAnAlteredFileList(t *testing.T) {
	pbs := &PBSClient{Manifest: upstreamTestManifest()}
	if _, err := pbs.SetCryptKey(upstreamTestKey(t)); err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}
	signed, err := pbs.EncodeManifest()
	if err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}

	var obj map[string]any
	if err := json.Unmarshal(signed, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	files, ok := obj["files"].([]any)
	if !ok || len(files) < 2 {
		t.Fatalf("fixture manifest has %T with fewer than 2 files", obj["files"])
	}
	obj["files"] = files[:1] // drop one, as a datastore-side edit would
	altered, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if err := pbs.VerifyManifestSignature(altered); err == nil {
		t.Fatal("a manifest with a file removed verified successfully")
	}
}

// An unsigned manifest under a key must be refused rather than waved through.
// The tempting shape — "no signature, nothing to check, carry on" — accepts
// exactly the manifest an attacker would produce by deleting the field.
func TestVerifyRefusesAnUnsignedManifest(t *testing.T) {
	m := upstreamTestManifest()
	raw, err := json.Marshal(m) // Signature is nil
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	pbs := &PBSClient{}
	if _, err := pbs.SetCryptKey(upstreamTestKey(t)); err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}

	err = pbs.VerifyManifestSignature(raw)
	if err == nil {
		t.Fatal("an unsigned manifest verified under a key")
	}
	if !strings.Contains(err.Error(), "no signature") {
		t.Fatalf("the refusal does not say the signature was missing: %v", err)
	}
}

// Verification without a key must be an error, not a silent pass. A caller
// that reached here with no key has a bug, and returning nil would turn that
// bug into "every snapshot verifies".
func TestVerifyWithoutAKeyIsAnError(t *testing.T) {
	if err := (&PBSClient{}).VerifyManifestSignature([]byte(`{}`)); err == nil {
		t.Fatal("a keyless client verified a manifest signature")
	}
}
