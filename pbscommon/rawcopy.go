package pbscommon

// Raw copy: moving a snapshot from one PBS to another exactly as stored.
//
// NimbusControl's namespace copier (nimbus-ns-sync, NimbusControl
// docs/V4-PBS-MIGRATION.md §3.6) reads a snapshot through a reader session on
// one server and writes it through a backup session on another, WITHOUT
// decoding anything: chunks and blobs cross as the bytes PBS stored, already
// compressed and, for an encrypted organization, already encrypted. No key is
// needed and none is ever loaded, so an encrypted organization moves exactly
// like a plaintext one.
//
// The agent never calls anything in this file. Every function here exists for
// that copier, and each is the smallest addition to what the backup and restore
// paths already use:
//
//   - GetChunkRaw         beside GetChunkData, without the decode;
//   - PutEncodedChunk     the existing putChunk, exported;
//   - UploadEncodedBlob   UploadBlob without the encode;
//   - ConnectWriterAt     Connect(false, ...) at a chosen backup-time;
//   - ParseIndexFile      the existing .didx/.fidx parsers, with the header
//     checksum a writer needs to close the copy.
//
// DownloadToBytes already returns an index or a blob raw, with PBS's status
// checked, so it is the raw download and nothing new is added for it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// rawBlobHeaderLen reports the header length a stored blob's magic implies,
// and whether it is encrypted. The CRC in every header covers the bytes after
// the header: the payload for a plain blob, the ciphertext for an encrypted one.
func rawBlobHeaderLen(magic [8]byte) (headerLen int, encrypted bool, err error) {
	switch magic {
	case UncompressedBlobMagic, CompressedBlobMagic:
		return 12, false, nil
	case EncryptedBlobMagic, EncrComprBlobMagic:
		return EncryptedHeaderLen, true, nil
	default:
		return 0, false, fmt.Errorf("unrecognised blob magic %v", magic)
	}
}

// CheckRawBlob verifies that bytes are a whole PBS blob as stored: a known
// magic, a header that fits, and a CRC that matches what follows it. It
// decodes nothing and needs no key.
//
// THIS IS THE ONLY CHECK A RAW COPY GETS ON AN ENCRYPTED CHUNK. PBS cannot
// verify an encrypted chunk's digest on upload (it would need the key), and it
// recomputes the stored CRC from whatever bytes arrive. Bytes damaged between
// the source's disk and the target's would therefore be stored under a CRC
// that agrees with them, pass every later verify job (which can only check the
// CRC), and fail only at restore. The source's CRC was written when the bytes
// were first stored, so checking it here, before the bytes go anywhere, is
// what keeps a copy honest.
func CheckRawBlob(raw []byte) (encrypted bool, err error) {
	if len(raw) < 12 {
		return false, fmt.Errorf("blob too short: %d bytes", len(raw))
	}
	var magic [8]byte
	copy(magic[:], raw[:8])
	headerLen, encrypted, err := rawBlobHeaderLen(magic)
	if err != nil {
		return false, err
	}
	if len(raw) < headerLen {
		return encrypted, fmt.Errorf("blob too short: %d bytes, its header alone is %d", len(raw), headerLen)
	}
	if stored, computed := binary.LittleEndian.Uint32(raw[8:12]), BlobCRC(raw[headerLen:]); stored != computed {
		return encrypted, fmt.Errorf("blob CRC mismatch (stored %08x, computed %08x): the bytes differ from what PBS stored", stored, computed)
	}
	return encrypted, nil
}

// GetChunkRaw downloads one chunk exactly as the datastore stores it, and
// checks it with CheckRawBlob before returning it. Reader session only.
//
// GetChunkData decodes (decompresses, decrypts); this does not, so it needs no
// key and the result can be uploaded to another datastore unchanged with
// PutEncodedChunk.
func (pbs *PBSClient) GetChunkRaw(digest string) ([]byte, error) {
	q := &url.Values{}
	q.Add("digest", digest)
	req, err := http.NewRequest("GET", pbs.BaseURL+"/chunk?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Authorization", fmt.Sprintf("PBSAPIToken=%s:%s", pbs.AuthID, pbs.Secret))
	resp, err := pbs.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, httpStatusError(resp, "chunk "+digest)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if _, err := CheckRawBlob(raw); err != nil {
		return nil, fmt.Errorf("chunk %s: %w", digest, err)
	}
	return raw, nil
}

// PutEncodedChunk uploads a chunk that is already encoded as PBS stores it
// (from GetChunkRaw), unchanged. Backup session only.
//
// plaintextSize is the chunk's length BEFORE compression and encryption, as
// every upload path records it: the copier takes it from the source index
// (end - start of that chunk), never from len(encoded).
func (pbs *PBSClient) PutEncodedChunk(writerid uint64, digest string, encoded []byte, plaintextSize int, dynamic bool) error {
	return pbs.putChunk(writerid, digest, encoded, plaintextSize, dynamic)
}

// UploadEncodedBlob stores a blob that is already encoded as PBS stores it
// (from DownloadToBytes on the source), unchanged, and records it in this
// session's manifest exactly as UploadBlob records the bytes it encodes.
//
// The bytes are checked with CheckRawBlob first. The manifest entry's
// crypt-mode describes the bytes: "encrypt" for an encrypted blob, "none"
// otherwise. A raw copy uploads the SOURCE's manifest afterwards, whose entries
// are the source's own, so this entry is this session's bookkeeping only.
func (pbs *PBSClient) UploadEncodedBlob(name string, encoded []byte) error {
	encrypted, err := CheckRawBlob(encoded)
	if err != nil {
		return fmt.Errorf("blob %s: %w", name, err)
	}
	mode := "none"
	if encrypted {
		mode = "encrypt"
	}
	return pbs.postBlob(name, encoded, mode)
}

// postBlob POSTs an encoded blob and records it in the manifest. The one
// implementation behind UploadBlob and UploadEncodedBlob, so the two cannot
// drift in how they report a blob to /finish.
func (pbs *PBSClient) postBlob(name string, out []byte, cryptMode string) error {
	q := &url.Values{}
	q.Add("encoded-size", fmt.Sprintf("%d", len(out)))
	q.Add("file-name", name)

	req, err := http.NewRequest("POST", pbs.BaseURL+"/blob?"+q.Encode(), bytes.NewBuffer(out))
	if err != nil {
		return err
	}
	resp2, err := pbs.Client.Do(req)
	if err != nil {
		debugf("Error making request: %v", err)
		return err
	}
	defer func() { _ = resp2.Body.Close() }()

	if resp2.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp2.Body)
		return fmt.Errorf("PBS upload blob failed: HTTP %d - %s", resp2.StatusCode, string(bodyBytes))
	}

	// Manifest csum must match the SHA-256 of the blob file as stored
	// on the datastore — i.e. the encoded buffer (magic + crc32 +
	// payload) we just sent. PBS validates this field on /finish as a
	// strict 64-char hex string; leaving it empty triggers
	// "unable to update manifest blob - Invalid string length".
	//
	// Size must match DataBlob::raw_size() used by PBS verify, which
	// returns raw_data.len() — i.e. the full on-disk file size with
	// the 12-byte DataBlobHeader, not just the inner payload. Using
	// the payload length produces "wrong size" at verify time even
	// though /finish accepts it.
	//
	// Historically this never blew up because the only UploadBlob
	// caller was UploadManifest, which appends the index.json.blob
	// entry AFTER the manifest JSON has already been serialized — so
	// the stored manifest never referenced itself with a broken csum.
	// As soon as another blob (the NTFS ACL side-car) is uploaded
	// before the manifest is serialized, its broken entry lands in
	// the persisted JSON and /finish rejects it.
	sum := sha256.Sum256(out)
	pbs.Manifest.Files = append(pbs.Manifest.Files, File{
		// From the caller, which knows what THE BYTES WE JUST WROTE are: the
		// manifest is a blob that is exempt from encryption on a client that
		// encrypts everything else. Deriving this from the client's
		// configuration would label the manifest "encrypt" and make a verify
		// job try to decrypt cleartext.
		CryptMode: cryptMode,
		Csum:      hex.EncodeToString(sum[:]),
		Filename:  name,
		Size:      int64(len(out)),
	})
	return nil
}

// ConnectWriterAt opens a backup (writer) session for a snapshot at an
// explicit backup-time, where Connect(false, ...) always uses the current
// time. A raw copy writes each snapshot at its SOURCE time, so the copy has
// the same identity (type, id, time) on the target as on the source.
//
// PBS refuses a backup-time older than the newest snapshot already in the
// group, which is why the copier writes a group oldest to newest.
func (pbs *PBSClient) ConnectWriterAt(backuptype string, backupTime int64) {
	pbs.connect(false, backuptype, backupTime)
}

// IndexFile is what a copier needs from a .fidx or .didx file to re-create it
// on another datastore: the chunk list in order, each chunk's place in the
// stream, and the header facts the writer's close must repeat.
type IndexFile struct {
	Name    string
	Dynamic bool
	// Csum is the header's index_csum, the SHA-256 of the entry section. A
	// writer that appends the same entries computes the same value, and
	// PBS's close refuses any other, so closing the copy with this proves
	// the copy indexes exactly what the source does.
	Csum [32]byte
	// Size is the stream length: the last end offset (dynamic), or the
	// header's size (fixed).
	Size uint64
	// ChunkSize is the fixed chunk length; 0 for a dynamic index.
	ChunkSize uint64
	// Digests are the chunks in order, hex, as the assign calls take them.
	Digests []string
	// ends are the cumulative end offsets of a dynamic index.
	ends []uint64
}

// ChunkCount is the number of entries, as the close call takes it.
func (ix *IndexFile) ChunkCount() int { return len(ix.Digests) }

// ChunkStart is the offset of chunk i in the stream, which is what
// /dynamic_index and /fixed_index take for each digest.
func (ix *IndexFile) ChunkStart(i int) uint64 {
	if ix.Dynamic {
		if i == 0 {
			return 0
		}
		return ix.ends[i-1]
	}
	return uint64(i) * ix.ChunkSize
}

// ChunkLen is chunk i's plaintext length: the `size` an upload of it must
// declare. A fixed index's last chunk may be shorter than ChunkSize.
func (ix *IndexFile) ChunkLen(i int) uint64 {
	if ix.Dynamic {
		return ix.ends[i] - ix.ChunkStart(i)
	}
	start := ix.ChunkStart(i)
	if rest := ix.Size - start; rest < ix.ChunkSize {
		return rest
	}
	return ix.ChunkSize
}

// ParseIndexFile decodes a .didx or .fidx as stored, chosen by its magic, and
// checks the header's index_csum against the entries. The index is untrusted
// input (a damaged or hostile datastore can serve one), so the existing
// parsers' guards apply, and an index whose checksum disagrees with its own
// entries is refused rather than copied.
func ParseIndexFile(name string, raw []byte) (*IndexFile, error) {
	const headerLen = 4096
	if len(raw) < headerLen {
		return nil, fmt.Errorf("index %q: short read (%d bytes, need at least %d)", name, len(raw), headerLen)
	}
	ix := &IndexFile{Name: name}
	copy(ix.Csum[:], raw[32:64])

	// didxMagic and fidxMagic are file_formats.rs's DYNAMIC_ and
	// FIXED_SIZED_CHUNK_INDEX_1_0; rawcopy_test pins both to upstream's
	// derivation, sha256(<preimage>)[0..8].
	var section []byte
	switch {
	case bytes.HasPrefix(raw, didxMagic):
		d, err := parseDIDXIndex(name, raw)
		if err != nil {
			return nil, err
		}
		ix.Dynamic = true
		ix.Digests = d.digests
		ix.ends = d.offsets
		ix.Size = d.total
		section = raw[headerLen:]
	case bytes.HasPrefix(raw, fidxMagic):
		f, err := parseFIDX(raw)
		if err != nil {
			return nil, fmt.Errorf("index %q: %w", name, err)
		}
		ix.Digests = f.digests
		ix.Size = f.size
		ix.ChunkSize = f.chunkSize
		section = raw[headerLen : headerLen+32*len(f.digests)]
		if len(raw) != headerLen+len(section) {
			return nil, fmt.Errorf("index %q: %d bytes after the %d digests its header declares", name, len(raw)-headerLen-len(section), len(f.digests))
		}
	default:
		return nil, fmt.Errorf("index %q: not a PBS index (magic %v)", name, raw[:8])
	}

	if computed := sha256.Sum256(section); computed != ix.Csum {
		return nil, fmt.Errorf("index %q: index_csum %x does not match its entries (%x): corrupt or hostile index", name, ix.Csum, computed)
	}
	return ix, nil
}
