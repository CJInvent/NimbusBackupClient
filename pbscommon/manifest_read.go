package pbscommon

// Reading a snapshot's manifest — the one file a restore can read before it
// knows which key it needs (V4-SPEC §18, phase G).
//
// THE ORDERING PROBLEM THIS SOLVES. Every other file in an encrypted snapshot
// is unreadable without the key, and the key is identified by a fingerprint
// that lives inside the snapshot. If that fingerprint were in an encrypted
// file the restore would have to hold the key to learn which key to hold.
// Upstream breaks the cycle by keeping the manifest unencrypted and putting
// the fingerprint in its `unprotected` block — outside the signature, because
// a reader must be able to work out which key to fetch BEFORE it can verify
// anything. blobExemptFromEncryption is the write-side half of the same rule;
// this file is the read side, which did not exist until now.
//
// WHY THE CLIENT HAD NO MANIFEST READER AT ALL. Until phase E every snapshot
// was in the clear, so a restore could go straight to the indexes and chunks
// and never ask what wrote them. Encryption makes that impossible: the first
// question a restore has to answer is "which key", and the manifest is the
// only place that answer is legible.

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
)

// DecodePlainBlob decodes a blob stored WITHOUT AES encryption: the manifest
// and the escrow blob, which are the two exemptions (blobExemptFromEncryption).
//
// It REFUSES an encrypted blob rather than trying to decrypt one, and the two
// failures read differently on purpose. "This blob is encrypted" sent to a
// caller that expected the manifest means the datastore is not laid out the
// way this client believes, which is a different investigation from a key
// problem — and silently falling through to a decrypt attempt here would make
// the manifest's exemption a thing that is true only until someone changes it
// somewhere else.
func DecodePlainBlob(blob []byte) ([]byte, error) {
	const headerLen = 12 // magic(8) ‖ crc32(4)
	if len(blob) < headerLen {
		return nil, fmt.Errorf("blob too short: %d bytes, header alone is %d", len(blob), headerLen)
	}

	var magic [8]byte
	copy(magic[:], blob[:8])

	// THE MAGIC IS READ FIRST, BEFORE THE CRC, because the magic is what
	// defines the layout the CRC belongs to. An encrypted blob has a 44-byte
	// header and its CRC covers the ciphertext, so checking bytes 12.. against
	// blob[8:12] on one of those compares two unrelated numbers and reports a
	// checksum failure — sending an operator to look for damaged storage when
	// the real answer is "this is an encrypted blob and you asked for a plain
	// one". A test caught exactly that ordering.
	compressed := false
	switch magic {
	case UncompressedBlobMagic:
	case CompressedBlobMagic:
		compressed = true
	case EncryptedBlobMagic, EncrComprBlobMagic:
		return nil, errors.New("this blob is encrypted, and the caller asked for one that is not")
	default:
		return nil, fmt.Errorf("unrecognised blob magic %v", magic)
	}

	payload := blob[headerLen:]

	// The CRC covers the STORED payload — post-compression, pre-decompression —
	// which is the same rule DecodeEncryptedBlob follows for its ciphertext.
	// Checked before anything is decompressed: a zstd frame is not worth
	// feeding to a decoder until the bytes are known to be the ones written.
	if stored, computed := binary.LittleEndian.Uint32(blob[8:12]), crc32.ChecksumIEEE(payload); stored != computed {
		return nil, fmt.Errorf("blob CRC mismatch (stored %08x, computed %08x): the stored bytes differ from what was written",
			stored, computed)
	}

	if compressed {
		out, err := blobDecoder.DecodeAll(payload, nil)
		if err != nil {
			return nil, fmt.Errorf("blob decompression failed: %w", err)
		}
		return out, nil
	}
	return payload, nil
}

// FetchManifestBytes downloads index.json.blob for the snapshot this client's
// reader session is attached to and returns the manifest JSON.
//
// REQUIRES Connect(true, ...) — this reads through the reader session, like
// every index. No key is required and none is used: see DecodePlainBlob.
func (pbs *PBSClient) FetchManifestBytes() ([]byte, error) {
	raw, err := pbs.DownloadToBytes(ManifestBlobName)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", ManifestBlobName, err)
	}
	plain, err := DecodePlainBlob(raw)
	if err != nil {
		return nil, fmt.Errorf("decoding %s: %w", ManifestBlobName, err)
	}
	return plain, nil
}

// ParseManifest parses manifest JSON.
//
// Separate from the fetch so the signature check below can work on the SAME
// bytes the parse saw. Re-downloading to verify would leave a window in which
// the two answers came from different responses.
func ParseManifest(raw []byte) (*BackupManifest, error) {
	var m BackupManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("manifest is not valid JSON: %w", err)
	}
	return &m, nil
}

// ManifestKeyFingerprint reports which key a manifest says signed it, in PBS's
// colon-separated form, or "" for a snapshot written without a key.
//
// "" IS A REAL ANSWER, not a missing one: it means the snapshot is
// unencrypted, and a restore should proceed with no key rather than go looking
// for one. Collapsing it into an error is how a working restore of an old
// unencrypted snapshot turns into a support ticket.
func ManifestKeyFingerprint(m *BackupManifest) string {
	if m == nil {
		return ""
	}
	return m.Unprotected.KeyFingerprint
}

// VerifyManifestSignature checks that this client's key is the one that signed
// these manifest bytes.
//
// EXACT MIRROR OF EncodeManifest, deliberately: same strip, same canonical
// rendering, same auth tag. Anything computed a second way here would be a
// second implementation of the signature to keep in step, and the failure mode
// of drift is a restore that rejects its own backups.
//
// This is an INTEGRITY check, not a key-selection mechanism — the fingerprint
// picks the key, and by the time this runs the right key is already loaded.
// What it catches is a manifest whose signed fields were altered after the
// backup: a file list with an entry removed, a size rewritten, a backup-time
// moved. None of that is detectable from the chunks, because each chunk
// authenticates only itself.
func (pbs *PBSClient) VerifyManifestSignature(raw []byte) error {
	if pbs.crypt == nil {
		return errors.New("cannot verify a manifest signature without a key")
	}

	doc, err := DecodeJSONDocument(raw)
	if err != nil {
		return fmt.Errorf("manifest re-parse: %w", err)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return errors.New("manifest is not a JSON object")
	}

	stored, ok := obj["signature"].(string)
	if !ok || stored == "" {
		// Loud, and specifically worded. An unsigned manifest under a key is
		// not "probably fine": either the snapshot was written by a client
		// that did not encrypt (in which case nothing should have selected a
		// key for it) or the signature was stripped.
		return errors.New("this snapshot's manifest carries no signature, but a key was selected for it")
	}

	delete(obj, "signature")
	delete(obj, "unprotected")

	canonical, err := ToCanonicalJSON(obj)
	if err != nil {
		return fmt.Errorf("manifest canonicalisation: %w", err)
	}
	tag := pbs.crypt.ComputeAuthTag(canonical)
	if want := hex.EncodeToString(tag[:]); want != stored {
		return fmt.Errorf(
			"manifest signature does not match: this key signs it as %s, the snapshot records %s — "+
				"the manifest was altered after the backup, or it was written by a different key",
			want, stored)
	}
	return nil
}
