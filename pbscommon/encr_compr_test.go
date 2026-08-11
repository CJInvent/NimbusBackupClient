package pbscommon

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func testZstd(t *testing.T, data []byte) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	defer func() { _ = enc.Close() }()
	return enc.EncodeAll(data, nil)
}

// compressible is data zstd will shrink a long way; incompressible is data it
// cannot shrink at all. Both cases have to be exercised, because the format
// rule is a CHOICE between two magics and a test that only ever hits one side
// of the branch proves half of it.
func compressible(n int) []byte {
	return bytes.Repeat([]byte("the same sentence over and over. "), n)
}

func incompressible(t *testing.T, n int) []byte {
	t.Helper()
	// Deterministic but high-entropy: a counter run through the AEAD would do,
	// but a simple LCG keeps this test free of the code it is testing.
	out := make([]byte, n)
	x := uint64(0x2545F4914F6CDD1D)
	for i := range out {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		out[i] = byte(x)
	}
	return out
}

func TestEncryptedCompressedRoundTrip(t *testing.T) {
	cc, err := NewCryptConfig(upstreamTestKey(t))
	if err != nil {
		t.Fatalf("NewCryptConfig: %v", err)
	}

	plain := compressible(400)
	blob, err := cc.EncodeEncryptedBlobCompressed(plain, testZstd(t, plain))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	if !bytes.Equal(blob[:8], EncrComprBlobMagic[:]) {
		t.Fatalf("magic %v, want ENCR_COMPR_BLOB_MAGIC_1_0 — compressible data must switch the magic", blob[:8])
	}
	if len(blob) >= len(plain) {
		t.Fatalf("compressed blob is %d bytes for %d of input — it did not compress", len(blob), len(plain))
	}

	back, err := cc.DecodeEncryptedBlob(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(back, plain) {
		t.Fatal("round trip changed the payload")
	}
}

// When zstd does not pay, the magic must fall back — and the payload with it.
// Emitting ENCR_COMPR over uncompressed bytes would decode as a broken frame.
func TestIncompressibleDataFallsBackToThePlainMagic(t *testing.T) {
	cc, err := NewCryptConfig(upstreamTestKey(t))
	if err != nil {
		t.Fatalf("NewCryptConfig: %v", err)
	}

	plain := incompressible(t, 8192)
	z := testZstd(t, plain)
	if len(z) < len(plain) {
		t.Skip("test data unexpectedly compressed; the branch under test needs data zstd cannot shrink")
	}

	blob, err := cc.EncodeEncryptedBlobCompressed(plain, z)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.Equal(blob[:8], EncryptedBlobMagic[:]) {
		t.Fatalf("magic %v, want ENCRYPTED_BLOB_MAGIC_1_0 for data zstd could not shrink", blob[:8])
	}
	back, err := cc.DecodeEncryptedBlob(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(back, plain) {
		t.Fatal("round trip changed the payload")
	}
}

// The CRC rule is the same for both encrypted magics: over the ciphertext,
// from EncryptedHeaderLen. header_size() returns 44 for ENCR_COMPR too.
func TestCompressedBlobCRCFollowsTheSameRule(t *testing.T) {
	cc, err := NewCryptConfig(upstreamTestKey(t))
	if err != nil {
		t.Fatalf("NewCryptConfig: %v", err)
	}
	plain := compressible(400)
	blob, err := cc.EncodeEncryptedBlobCompressed(plain, testZstd(t, plain))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	stored := binary.LittleEndian.Uint32(blob[8:12])
	if want := BlobCRC(blob[EncryptedHeaderLen:]); stored != want {
		t.Fatalf("CRC %08x, want %08x over the ciphertext", stored, want)
	}
	if stored == BlobCRC(blob[12:]) {
		t.Fatal("CRC covers the IV and tag")
	}
}

// COMPRESS THEN ENCRYPT. If the order were reversed the blob would still be
// valid and would still round trip — it would just be as large as the
// plaintext, silently costing every encrypted customer their compression
// ratio. Size is the only observable that catches it, so size is asserted.
func TestCompressionHappensBeforeEncryption(t *testing.T) {
	cc, err := NewCryptConfig(upstreamTestKey(t))
	if err != nil {
		t.Fatalf("NewCryptConfig: %v", err)
	}
	plain := compressible(1000) // ~33 KB of near-perfectly compressible text

	compressedBlob, err := cc.EncodeEncryptedBlobCompressed(plain, testZstd(t, plain))
	if err != nil {
		t.Fatalf("encode compressed: %v", err)
	}
	plainBlob, err := cc.EncodeEncryptedBlob(plain)
	if err != nil {
		t.Fatalf("encode plain: %v", err)
	}

	// A tenth is far inside what zstd achieves on repeated text, and far
	// outside anything encrypt-then-compress could reach.
	if len(compressedBlob)*10 > len(plainBlob) {
		t.Fatalf("compressed blob %d bytes vs uncompressed %d — compression is not being applied to the plaintext",
			len(compressedBlob), len(plainBlob))
	}
}

// A damaged compressed blob must report the zstd failure, not a key failure.
// Both are refusals; they send an operator to different places.
func TestCompressedBlobReportsFrameDamageDistinctly(t *testing.T) {
	cc, err := NewCryptConfig(upstreamTestKey(t))
	if err != nil {
		t.Fatalf("NewCryptConfig: %v", err)
	}
	plain := compressible(400)
	z := testZstd(t, plain)

	// Corrupt the zstd frame BEFORE encryption, so the AEAD tag is valid over
	// the damaged frame — this is the only way to reach the decompress error
	// without also breaking the tag, and it is what a bug in a writer, rather
	// than storage damage, would look like.
	z[len(z)/2] ^= 0xff
	blob, err := cc.EncodeEncryptedBlobCompressed(plain, z)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.Equal(blob[:8], EncrComprBlobMagic[:]) {
		t.Skip("damaged frame no longer shorter than the plaintext")
	}

	_, err = cc.DecodeEncryptedBlob(blob)
	if err == nil {
		t.Fatal("a damaged zstd frame decoded")
	}
	if strings.Contains(err.Error(), "wrong key") {
		t.Fatalf("frame damage reported as a key problem: %v", err)
	}
	if !strings.Contains(err.Error(), "zstd") {
		t.Fatalf("error does not name the frame: %v", err)
	}
}

// Every encrypted chunk must still hash to the same digest whether or not it
// compressed — the digest is over PLAINTEXT, so dedup cannot depend on
// compressibility, and a snapshot taken before this change must dedup against
// one taken after.
func TestCompressionDoesNotChangeTheChunkDigest(t *testing.T) {
	pbs := &PBSClient{}
	if _, err := pbs.SetCryptKey(upstreamTestKey(t)); err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}
	data := compressible(200)
	if pbs.ChunkDigest(data) != pbs.crypt.ComputeDigest(data) {
		t.Fatal("the chunk digest is not being taken over the plaintext")
	}
}

// The UPLOAD PATH must actually use the compressed form. This is separate from
// the tests above on purpose: they cover EncodeEncryptedBlobCompressed, and a
// sabotage that reverted UploadChunk to encrypt-only passed every one of them,
// because nothing checked that the caller still called it.
func TestEncryptedChunkUploadPathCompresses(t *testing.T) {
	pbs := &PBSClient{}
	if _, err := pbs.SetCryptKey(upstreamTestKey(t)); err != nil {
		t.Fatalf("SetCryptKey: %v", err)
	}
	data := compressible(1000)

	withCompression, err := pbs.encodeEncryptedChunk(data, true)
	if err != nil {
		t.Fatalf("encode (compress): %v", err)
	}
	if !bytes.Equal(withCompression[:8], EncrComprBlobMagic[:]) {
		t.Fatalf("magic %v — the upload path is not compressing encrypted chunks", withCompression[:8])
	}

	// And a caller that asked for no compression still gets none.
	without, err := pbs.encodeEncryptedChunk(data, false)
	if err != nil {
		t.Fatalf("encode (no compress): %v", err)
	}
	if !bytes.Equal(without[:8], EncryptedBlobMagic[:]) {
		t.Fatalf("magic %v — compression was applied to a caller that declined it", without[:8])
	}
	if len(withCompression) >= len(without) {
		t.Fatalf("compressed chunk %d bytes vs uncompressed %d", len(withCompression), len(without))
	}

	// Both forms must decrypt back to the same plaintext, or dedup and restore
	// disagree depending on which branch a chunk happened to take.
	for name, blob := range map[string][]byte{"compressed": withCompression, "plain": without} {
		back, err := pbs.crypt.DecodeEncryptedBlob(blob)
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if !bytes.Equal(back, data) {
			t.Fatalf("%s: round trip changed the payload", name)
		}
	}
}
