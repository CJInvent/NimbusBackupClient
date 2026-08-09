package pbscommon

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// PBS chunk encryption, checked against UPSTREAM, not against ourselves.
//
// Dev rule 25: a test that validates our encoder with our own decoder proves
// only that we are self-consistent. Every constant below is transcribed by
// hand from github.com/proxmox/proxmox-backup —
// pbs-datastore/src/file_formats.rs and pbs-tools/src/crypt_config.rs — and
// every derivation is recomputed here from its specification rather than
// called through the code under test.
//
// The cost of getting this wrong is not a crash. It is chunks a real PBS
// accepts and later cannot read, or digests that silently never dedup. Both
// surface for the first time during a restore or on a storage bill.

// Transcribed from file_formats.rs, whose first line reads
// "WARNING: PLEASE DO NOT MODIFY THOSE MAGIC VALUES".
func TestBlobMagicsMatchUpstream(t *testing.T) {
	cases := []struct {
		name string
		got  [8]byte
		want [8]byte
	}{
		{"UNCOMPRESSED_BLOB_MAGIC_1_0", UncompressedBlobMagic, [8]byte{66, 171, 56, 7, 190, 131, 112, 161}},
		{"COMPRESSED_BLOB_MAGIC_1_0", CompressedBlobMagic, [8]byte{49, 185, 88, 66, 111, 182, 163, 127}},
		{"ENCRYPTED_BLOB_MAGIC_1_0", EncryptedBlobMagic, [8]byte{123, 103, 133, 190, 34, 45, 76, 240}},
		{"ENCR_COMPR_BLOB_MAGIC_1_0", EncrComprBlobMagic, [8]byte{230, 89, 27, 191, 11, 191, 216, 11}},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, upstream says %v", c.name, c.got, c.want)
		}
	}
}

func TestHeaderSizesMatchUpstream(t *testing.T) {
	// EncryptedDataBlobHeader { head: { magic: [u8;8], crc: [u8;4] },
	//                           iv: [u8;16], tag: [u8;16] }
	if IVSize != 16 {
		t.Errorf("IVSize = %d; upstream declares iv: [u8; 16]. Go's default GCM "+
			"nonce is 12, and a 12-byte IV produces a blob PBS cannot read.", IVSize)
	}
	if TagSize != 16 {
		t.Errorf("TagSize = %d, upstream declares tag: [u8; 16]", TagSize)
	}
}

// The id_key derivation, recomputed here from its SPECIFICATION —
// pbkdf2_hmac(enc_key, b"_id_key", 10, sha256) — with a PBKDF2 written out
// longhand rather than by calling the same library the implementation uses.
// Calling x/crypto on both sides would only prove x/crypto is deterministic.
func pbkdf2SHA256Longhand(password, salt []byte, iter, keyLen int) []byte {
	var out []byte
	for block := 1; len(out) < keyLen; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iter; i++ {
			mac := hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func TestIDKeyDerivationMatchesUpstreamSpec(t *testing.T) {
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = byte(i * 7)
	}
	c, err := NewCryptConfig(key)
	if err != nil {
		t.Fatal(err)
	}

	want := pbkdf2SHA256Longhand(key, []byte("_id_key"), 10, 32)
	if !bytes.Equal(c.idKey[:], want) {
		t.Errorf("id_key = %x, spec gives %x", c.idKey, want)
	}

	// The argument order is the trap: the KEY is the password and "_id_key"
	// is the SALT, which is the opposite of how these read. Swapping them
	// compiles and runs, and changes every chunk digest in the fleet.
	swapped := pbkdf2SHA256Longhand([]byte("_id_key"), key, 10, 32)
	if bytes.Equal(c.idKey[:], swapped) {
		t.Error("id_key matches the SWAPPED derivation — password and salt are the wrong way round")
	}
}

func TestChunkDigestAppendsTheKey(t *testing.T) {
	key := bytes.Repeat([]byte{0xA5}, KeySize)
	c, err := NewCryptConfig(key)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("the quick brown fox")

	// sha256(data || id_key), recomputed here.
	h := sha256.New()
	h.Write(data)
	h.Write(c.idKey[:])
	var want [32]byte
	copy(want[:], h.Sum(nil))

	if got := c.ComputeDigest(data); got != want {
		t.Errorf("digest = %x, want %x", got, want)
	}

	// PREPENDING is the more common construction and is what a careless port
	// produces. Upstream appends deliberately — its comment says "to avoid
	// length extensions attacks" — and prepending would give every chunk a
	// different digest from PBS's, breaking dedup silently.
	h2 := sha256.New()
	h2.Write(c.idKey[:])
	h2.Write(data)
	var prepended [32]byte
	copy(prepended[:], h2.Sum(nil))
	if c.ComputeDigest(data) == prepended {
		t.Error("digest matches the PREPENDED construction — the key must go at the END")
	}
}

func TestDigestIsKeyScopedWhichIsTheDedupBoundary(t *testing.T) {
	// V4-SPEC §9: dedup scope IS key scope, and this is the mechanism. Two
	// agents dedup against each other exactly when they share a key.
	a, _ := NewCryptConfig(bytes.Repeat([]byte{1}, KeySize))
	b, _ := NewCryptConfig(bytes.Repeat([]byte{2}, KeySize))
	same, _ := NewCryptConfig(bytes.Repeat([]byte{1}, KeySize))
	data := []byte("identical payload on two machines")

	if a.ComputeDigest(data) != same.ComputeDigest(data) {
		t.Error("the same key gives different digests — cross-machine dedup would not work")
	}
	if a.ComputeDigest(data) == b.ComputeDigest(data) {
		t.Error("different keys give the SAME digest — key scope would not bound dedup")
	}

	// And an UNENCRYPTED digest is a plain sha256, so encrypted and
	// unencrypted copies never dedup together. That is why turning encryption
	// on re-uploads an org once (§9) — a bandwidth and billing event, not a
	// surprise to discover in production.
	if a.ComputeDigest(data) == sha256.Sum256(data) {
		t.Error("keyed digest equals the plain sha256")
	}
}

func TestFingerprintMatchesUpstreamInput(t *testing.T) {
	// FINGERPRINT_INPUT transcribed from crypt_config.rs.
	upstreamInput := []byte{
		110, 208, 239, 119, 71, 31, 255, 77, 85, 199, 168, 254, 74, 157, 182, 33,
		97, 64, 127, 19, 76, 114, 93, 223, 48, 153, 45, 37, 236, 69, 237, 38,
	}
	c, _ := NewCryptConfig(bytes.Repeat([]byte{0x5A}, KeySize))
	if got, want := c.Fingerprint(), c.ComputeDigest(upstreamInput); got != want {
		t.Errorf("fingerprint = %x, want digest of the upstream input %x", got, want)
	}
}

func TestEncryptedBlobLayout(t *testing.T) {
	c, _ := NewCryptConfig(bytes.Repeat([]byte{0x33}, KeySize))
	plaintext := []byte("chunk contents")

	blob, err := c.EncodeEncryptedBlob(plaintext)
	if err != nil {
		t.Fatal(err)
	}

	// MAGIC(8) || CRC32(4) || IV(16) || TAG(16) || ciphertext
	if want := 8 + 4 + 16 + 16 + len(plaintext); len(blob) != want {
		t.Errorf("blob is %d bytes, want %d — GCM ciphertext is the same length as its plaintext", len(blob), want)
	}
	if !bytes.Equal(blob[:8], EncryptedBlobMagic[:]) {
		t.Errorf("magic = %v, want ENCRYPTED_BLOB_MAGIC_1_0", blob[:8])
	}
	// Upstream builds the header with crc: [0; 4] and says the server
	// computes it on upload. Writing a value we invented would be worse than
	// writing none, because it would be checked.
	if !bytes.Equal(blob[8:12], []byte{0, 0, 0, 0}) {
		t.Errorf("crc = %v, want zero — the server computes it", blob[8:12])
	}
	// The plaintext must not be sitting in the blob.
	if bytes.Contains(blob, plaintext) {
		t.Error("the plaintext appears verbatim in the encrypted blob")
	}
}

func TestBlobRoundTrip(t *testing.T) {
	c, _ := NewCryptConfig(bytes.Repeat([]byte{0x77}, KeySize))
	for _, size := range []int{0, 1, 15, 16, 17, 4096, 65536} {
		plaintext := make([]byte, size)
		for i := range plaintext {
			plaintext[i] = byte(i)
		}
		blob, err := c.EncodeEncryptedBlob(plaintext)
		if err != nil {
			t.Fatalf("%d bytes: %v", size, err)
		}
		got, err := c.DecodeEncryptedBlob(blob)
		if err != nil {
			t.Fatalf("%d bytes: %v", size, err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Errorf("%d bytes did not round-trip", size)
		}
	}
}

func TestEncryptionIsRandomisedButTheDigestIsNot(t *testing.T) {
	// V4-SPEC §9: ciphertext differs per upload because the IV is fresh, the
	// DIGEST does not, and PBS dedups on the digest. If the IV were fixed,
	// identical chunks would produce identical ciphertext — which leaks
	// equality — and if the digest varied, dedup would stop working.
	c, _ := NewCryptConfig(bytes.Repeat([]byte{0x11}, KeySize))
	data := []byte("same chunk, uploaded twice")

	b1, _ := c.EncodeEncryptedBlob(data)
	b2, _ := c.EncodeEncryptedBlob(data)

	if bytes.Equal(b1, b2) {
		t.Error("two encryptions are identical — the IV is not fresh")
	}
	if bytes.Equal(b1[12:12+IVSize], b2[12:12+IVSize]) {
		t.Error("the IV repeated across two encryptions")
	}
	if c.ComputeDigest(data) != c.ComputeDigest(data) {
		t.Error("the digest is not stable")
	}
}

func TestDecodeRejectsWhatItShould(t *testing.T) {
	c, _ := NewCryptConfig(bytes.Repeat([]byte{0x22}, KeySize))
	blob, _ := c.EncodeEncryptedBlob([]byte("payload"))

	// A wrong key must fail closed, not return plausible garbage.
	other, _ := NewCryptConfig(bytes.Repeat([]byte{0x99}, KeySize))
	if _, err := other.DecodeEncryptedBlob(blob); err == nil {
		t.Error("a blob decrypted under the wrong key")
	}

	// GCM's tag is the point of using GCM: a flipped bit in the ciphertext
	// must be caught rather than yielding corrupted restore data.
	tampered := append([]byte(nil), blob...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := c.DecodeEncryptedBlob(tampered); err == nil {
		t.Error("a flipped ciphertext bit was not caught by the tag")
	}

	// And a flipped bit in the IV, which is in the header rather than the
	// authenticated payload.
	tampered2 := append([]byte(nil), blob...)
	tampered2[12] ^= 0x01
	if _, err := c.DecodeEncryptedBlob(tampered2); err == nil {
		t.Error("a flipped IV bit was not caught")
	}

	// A reader meeting a compressed blob needs to know it is a missing zstd
	// step, not corruption.
	compressed := append([]byte(nil), blob...)
	copy(compressed[:8], EncrComprBlobMagic[:])
	_, err := c.DecodeEncryptedBlob(compressed)
	if err == nil || !contains(err.Error(), "zstd") {
		t.Errorf("compressed magic error = %v, want it to name zstd", err)
	}

	// An UNENCRYPTED blob must say so rather than "unrecognised".
	plain := append([]byte(nil), blob...)
	copy(plain[:8], UncompressedBlobMagic[:])
	_, err = c.DecodeEncryptedBlob(plain)
	if err == nil || !contains(err.Error(), "not encrypted") {
		t.Errorf("uncompressed magic error = %v, want it to say the blob is not encrypted", err)
	}

	for _, short := range [][]byte{nil, {}, blob[:8], blob[:43]} {
		if _, err := c.DecodeEncryptedBlob(short); err == nil {
			t.Errorf("a %d-byte blob was accepted", len(short))
		}
	}
}

func TestKeyLengthIsEnforced(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		if _, err := NewCryptConfig(make([]byte, n)); err == nil {
			t.Errorf("a %d-byte key was accepted; AES-256 needs exactly %d", n, KeySize)
		}
	}
}

func TestCRCIsLittleEndian(t *testing.T) {
	// Upstream writes the header with write_le_value.
	blob := make([]byte, 12)
	if err := PutCRC(blob, 0x01020304); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(blob[8:12]); got != "04030201" {
		t.Errorf("crc bytes = %s, want little-endian 04030201", got)
	}
}

func contains(hay, needle string) bool {
	return bytes.Contains([]byte(hay), []byte(needle))
}
