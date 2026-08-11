package pbscommon

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// PBS chunk encryption (V4-SPEC §9).
//
// Before this file the client could not encrypt at all: pbsapi.go answered
// "encrypted chunks not supported". This is the wire layer, not a flag — the
// bytes PBS stores have a different shape when encrypted, and getting any part
// of that shape wrong produces chunks a real Proxmox Backup Server will accept
// and then fail to read, which is the worst possible failure for a backup
// product because it is invisible until a restore.
//
// EVERY CONSTANT HERE WAS READ OUT OF THE UPSTREAM SOURCE, not remembered:
// pbs-datastore/src/file_formats.rs and pbs-tools/src/crypt_config.rs in
// github.com/proxmox/proxmox-backup. The test file pins them against values
// transcribed from those same files, because dev rule 25 is explicit that
// checking our encoder against our own decoder proves only self-consistency.
//
// Two of those facts are ones a careful implementer would get wrong:
//
//   - THE IV IS 16 BYTES. Go's cipher.NewGCM gives a 12-byte nonce, which is
//     the near-universal default and is what any example you find will use.
//     PBS's EncryptedDataBlobHeader declares `iv: [u8; 16]`, so this uses
//     NewGCMWithNonceSize(16). A 12-byte IV produces a blob PBS cannot read.
//
//   - THE DIGEST APPENDS THE KEY. `sha256(data || id_key)`, with the key at
//     the END — upstream's own comment says "to avoid length extension
//     attacks". Prepending is the more common construction and would give
//     every chunk a different digest from the one PBS computes, breaking
//     dedup silently rather than loudly.

// Blob magics, transcribed from pbs-datastore/src/file_formats.rs, whose first
// line is "WARNING: PLEASE DO NOT MODIFY THOSE MAGIC VALUES".
var (
	UncompressedBlobMagic = [8]byte{66, 171, 56, 7, 190, 131, 112, 161}
	CompressedBlobMagic   = [8]byte{49, 185, 88, 66, 111, 182, 163, 127}
	EncryptedBlobMagic    = [8]byte{123, 103, 133, 190, 34, 45, 76, 240}
	EncrComprBlobMagic    = [8]byte{230, 89, 27, 191, 11, 191, 216, 11}
)

const (
	// IVSize is 16, not the 12 Go's GCM defaults to. See the header.
	IVSize = 16
	// TagSize is GCM's authentication tag.
	TagSize = 16
	// KeySize is AES-256.
	KeySize = 32

	// idKeyIterations and idKeySalt reproduce
	// pbkdf2_hmac(enc_key, b"_id_key", 10, sha256) exactly. Ten iterations is
	// NOT a password KDF and is not meant to be: the input is already a
	// 256-bit random key, so this is domain separation, not stretching.
	// "Hardening" it to a realistic PBKDF2 count would make every digest this
	// client computes differ from every digest PBS computes.
	idKeyIterations = 10
)

var idKeySalt = []byte("_id_key")

// fingerprintInput is the fixed 32-byte input PBS hashes with the derived key
// to produce a key fingerprint. Transcribed from crypt_config.rs.
//
// Worth having even though the client does not strictly need it: it is the
// identifier PBS itself uses for a key, so an agent and a datastore can be
// compared against each other rather than against something only we compute.
var fingerprintInput = [32]byte{
	110, 208, 239, 119, 71, 31, 255, 77, 85, 199, 168, 254, 74, 157, 182, 33,
	97, 64, 127, 19, 76, 114, 93, 223, 48, 153, 45, 37, 236, 69, 237, 38,
}

// CryptConfig holds a backup key and everything derived from it.
//
// Mirrors upstream's CryptConfig deliberately, including the name, so the two
// can be read side by side. The derived id_key is computed once at
// construction because it is used on EVERY chunk — a 931 GB disk is roughly
// 240,000 of them, and re-deriving per chunk would be 2.4 million pointless
// PBKDF2 rounds.
type CryptConfig struct {
	encKey [KeySize]byte
	idKey  [KeySize]byte
	aead   cipher.AEAD
}

// NewCryptConfig derives everything from a 32-byte backup key.
func NewCryptConfig(encKey []byte) (*CryptConfig, error) {
	if len(encKey) != KeySize {
		return nil, fmt.Errorf("backup key must be %d bytes, got %d", KeySize, len(encKey))
	}

	c := &CryptConfig{}
	copy(c.encKey[:], encKey)

	// pbkdf2_hmac(enc_key, "_id_key", 10, sha256) — the KEY is the password
	// and "_id_key" is the salt, which is the opposite of how these arguments
	// usually read. Swapping them compiles, runs, and produces a different
	// id_key for every chunk digest in the fleet.
	derived := pbkdf2.Key(c.encKey[:], idKeySalt, idKeyIterations, KeySize, sha256.New)
	copy(c.idKey[:], derived)

	block, err := aes.NewCipher(c.encKey[:])
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	// NewGCMWithNonceSize, not NewGCM. See the header.
	aead, err := cipher.NewGCMWithNonceSize(block, IVSize)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	c.aead = aead

	return c, nil
}

// ComputeDigest returns the chunk digest PBS will index this data under:
// sha256(data || id_key), key APPENDED.
//
// This is the value dedup happens on, which is why two machines dedup against
// each other exactly when they share a key (V4-SPEC §9) — and why encrypted
// and unencrypted copies of identical data never dedup together.
func (c *CryptConfig) ComputeDigest(data []byte) [32]byte {
	h := sha256.New()
	h.Write(data)
	h.Write(c.idKey[:]) // at the END — upstream: "to avoid length extensions attacks"
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// ComputeAuthTag is HMAC-SHA256 over data, keyed with the derived id_key.
//
// Upstream builds an openssl PKey::hmac from id_key at construction and signs
// with SHA256 (crypt_config.rs, `data_signer` / `compute_auth_tag`). This is
// the same primitive; Go's hmac.New(sha256.New, key) is what PKey::hmac gives.
//
// NOTE THAT THIS IS NOT ComputeDigest. Both take the id_key and some bytes and
// give 32 bytes back, and they are different constructions for different jobs:
// chunk digests are a plain SHA-256 with the key APPENDED (a namespace, so two
// keys never collide in one datastore), while this is a real HMAC used to
// AUTHENTICATE a manifest. Using either where the other belongs produces a
// stable, plausible, wrong value.
func (c *CryptConfig) ComputeAuthTag(data []byte) [32]byte {
	mac := hmac.New(sha256.New, c.idKey[:])
	mac.Write(data)
	var out [32]byte
	copy(out[:], mac.Sum(nil))
	return out
}

// Fingerprint is PBS's own identifier for this key.
func (c *CryptConfig) Fingerprint() [32]byte {
	return c.ComputeDigest(fingerprintInput[:])
}

// FingerprintString renders a fingerprint the way PBS writes it into a
// manifest: lowercase hex with a colon between every byte pair.
//
// Transcribed from pbs-api-types/src/crypto.rs `as_fingerprint`. The colons are
// not decoration — `Fingerprint::from_str` strips them before hex-decoding, so
// a bare hex string is accepted on read, but writing one makes our manifests
// visibly different from every other client's for no reason.
func FingerprintString(fp [32]byte) string {
	var b strings.Builder
	b.Grow(32*2 + 31)
	for i, v := range fp {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%02x", v)
	}
	return b.String()
}

// EncodeEncryptedBlob produces the on-disk form of one encrypted chunk:
//
//	MAGIC(8) || CRC32(4) || IV(16) || TAG(16) || ciphertext
//
// The CRC is written as ZERO. Upstream's own encode() builds the header with
// `crc: [0; 4]` and its comment explains why: the server computes and verifies
// the CRC on upload, "so there is usually no need to compute it on the client
// side". Writing a value we invented would be worse than writing none, because
// it would be checked.
//
// Compression is NOT applied here. PBS picks between the plain and compressed
// magics based on whether zstd actually shrank the data, and this client's
// chunker does not compress — so emitting ENCRYPTED_BLOB_MAGIC_1_0
// unconditionally is correct rather than a simplification. If compression is
// added later it must switch the magic, not just the payload.
func (c *CryptConfig) EncodeEncryptedBlob(plaintext []byte) ([]byte, error) {
	iv := make([]byte, IVSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, fmt.Errorf("iv: %w", err)
	}

	// Go's Seal appends the tag to the ciphertext; PBS wants it in the HEADER,
	// before the ciphertext. Split rather than reorder bytes blindly, so the
	// intent survives a reader who has not met this format.
	sealed := c.aead.Seal(nil, iv, plaintext, nil) // empty AAD, matching upstream
	if len(sealed) < TagSize {
		return nil, errors.New("sealed output shorter than the tag")
	}
	ciphertext := sealed[:len(sealed)-TagSize]
	tag := sealed[len(sealed)-TagSize:]

	out := make([]byte, 0, 8+4+IVSize+TagSize+len(ciphertext))
	out = append(out, EncryptedBlobMagic[:]...)
	out = append(out, 0, 0, 0, 0) // CRC — the server's to compute
	out = append(out, iv...)
	out = append(out, tag...)
	out = append(out, ciphertext...)
	return out, nil
}

// DecodeEncryptedBlob reverses EncodeEncryptedBlob.
//
// Present so the client can VERIFY what it just wrote, and so a restore path
// exists at all. A recovery path nobody has exercised is a recovery path
// nobody has.
func (c *CryptConfig) DecodeEncryptedBlob(blob []byte) ([]byte, error) {
	const headerLen = 8 + 4 + IVSize + TagSize
	if len(blob) < headerLen {
		return nil, fmt.Errorf("blob too short: %d bytes, header alone is %d", len(blob), headerLen)
	}

	var magic [8]byte
	copy(magic[:], blob[:8])
	switch magic {
	case EncryptedBlobMagic:
		// the only form this client writes
	case EncrComprBlobMagic:
		// Named specifically rather than lumped into "unknown magic": a
		// reader meeting this has a blob written by a client that compressed,
		// and needs to know it is a missing zstd step and not corruption.
		return nil, errors.New("compressed encrypted blob: zstd decompression is not implemented")
	case UncompressedBlobMagic, CompressedBlobMagic:
		return nil, errors.New("blob is not encrypted")
	default:
		return nil, fmt.Errorf("unrecognised blob magic %v", magic)
	}

	iv := blob[12 : 12+IVSize]
	tag := blob[12+IVSize : headerLen]
	ciphertext := blob[headerLen:]

	// Reassemble in Go's order: ciphertext then tag.
	sealed := make([]byte, 0, len(ciphertext)+TagSize)
	sealed = append(sealed, ciphertext...)
	sealed = append(sealed, tag...)

	plaintext, err := c.aead.Open(nil, iv, sealed, nil)
	if err != nil {
		// One message for a wrong key and for tampering: distinguishing them
		// tells an attacker which of the two they achieved.
		return nil, errors.New("chunk could not be decrypted: wrong key or damaged data")
	}
	return plaintext, nil
}

// BlobCRC computes the CRC32 PBS uses over a blob's payload.
//
// Provided for verification tooling rather than for the write path — see
// EncodeEncryptedBlob on why the client writes zero. IEEE polynomial, which is
// what Rust's crc32fast implements.
func BlobCRC(payload []byte) uint32 {
	return crc32.ChecksumIEEE(payload)
}

// PutCRC writes a CRC into a blob header in little-endian, matching upstream's
// write_le_value.
func PutCRC(blob []byte, crc uint32) error {
	if len(blob) < 12 {
		return errors.New("blob too short to hold a header")
	}
	binary.LittleEndian.PutUint32(blob[8:12], crc)
	return nil
}
