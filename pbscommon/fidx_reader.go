package pbscommon

// fidx_reader.go — FIDXReaderAt: an io.ReaderAt over a PBS FIXED-index
// archive (*.img.fidx — raw disk images from machine/volume backups), fetching
// chunks from the server ON DEMAND with the same LRU cache used by
// DIDXReaderAt.
//
// Fixed indexes are simpler than dynamic ones: every chunk covers exactly
// ChunkSize bytes of the image (the last one may be logically short), so
// chunk lookup is pure arithmetic instead of a binary search. This reader is
// the storage primitive under image-backup file browsing (imagebrowse
// module): an NTFS/partition parser does sparse reads over the disk image,
// and only the chunks those reads touch are ever downloaded — listing a
// directory on a multi-TB image fetches a handful of chunks, not the image.
//
// Contract matches DIDXReaderAt: ReadAt fills p fully unless it reaches the
// end of the image (then returns n < len(p) with io.EOF); an optional cancel
// predicate aborts between chunk fetches with ErrReadCancelled; the PBSClient
// must stay Connected (reader session) for the reader's lifetime.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"sync"
)

var fidxMagic = []byte{47, 127, 65, 237, 145, 253, 15, 205}

// FIDXReaderAt is a lazy io.ReaderAt over a fixed-index PBS archive.
type FIDXReaderAt struct {
	pbs       *PBSClient
	digests   []string
	size      uint64 // logical image size in bytes
	chunkSize uint64 // fixed chunk span (PBS_FIXED_CHUNK_SIZE in practice)
	cache     *chunkCache
	progress  func(fetched, total int)
	cancel    func() bool

	mu      sync.Mutex
	fetched int
}

// SetCancelCheck installs a predicate polled before each read and chunk
// fetch; once it returns true the reader aborts with ErrReadCancelled. Same
// semantics and caveats as DIDXReaderAt.SetCancelCheck.
func (r *FIDXReaderAt) SetCancelCheck(fn func() bool) { r.cancel = fn }

// Size returns the logical size of the disk image in bytes.
func (r *FIDXReaderAt) Size() int64 { return int64(r.size) }

// NewFIDXReaderAt downloads and parses the .fidx index for archiveName and
// returns a lazy reader over the raw disk image plus its total size.
// cacheChunks defaults to 32 (32 x 4 MiB = 128 MiB ceiling); progress
// (optional) is called with (chunksFetchedSoFar, totalChunks) on each NEW
// chunk fetch, mirroring NewDIDXReaderAt.
func (pbs *PBSClient) NewFIDXReaderAt(archiveName string, cacheChunks int, progress func(fetched, total int)) (*FIDXReaderAt, int64, error) {
	if cacheChunks <= 0 {
		cacheChunks = 32
	}
	raw, err := pbs.DownloadToBytes(archiveName)
	if err != nil {
		return nil, 0, fmt.Errorf("download fixed index %s: %w", archiveName, err)
	}
	r, err := parseFIDX(raw)
	if err != nil {
		return nil, 0, fmt.Errorf("parse fixed index %s: %w", archiveName, err)
	}
	r.pbs = pbs
	r.cache = newChunkCache(cacheChunks)
	r.progress = progress
	return r, int64(r.size), nil
}

// parseFIDX validates the header and reads the per-chunk digest table.
// Factored out of NewFIDXReaderAt so it is unit-testable without a server.
func parseFIDX(raw []byte) (*FIDXReaderAt, error) {
	rdr := bytes.NewReader(raw)
	var hdr FIDXHeader
	if err := binary.Read(rdr, binary.LittleEndian, &hdr); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if !slices.Equal(hdr.Magic[:], fidxMagic) {
		return nil, fmt.Errorf("invalid FIDX magic %v", hdr.Magic)
	}
	if hdr.ChunkSize == 0 {
		return nil, fmt.Errorf("invalid FIDX chunk size 0")
	}
	// Number of chunks covering Size bytes at ChunkSize each, final partial
	// chunk included: ceil(Size / ChunkSize).
	n := hdr.Size / hdr.ChunkSize
	if hdr.Size%hdr.ChunkSize != 0 {
		n++
	}
	digests := make([]string, 0, n)
	h := make([]byte, 32)
	for i := uint64(0); i < n; i++ {
		if _, err := io.ReadFull(rdr, h); err != nil {
			return nil, fmt.Errorf("digest table truncated at %d/%d: %w", i, n, err)
		}
		digests = append(digests, hex.EncodeToString(h))
	}
	return &FIDXReaderAt{
		digests:   digests,
		size:      hdr.Size,
		chunkSize: hdr.ChunkSize,
	}, nil
}

// chunkAt returns the plaintext of chunk ci, from cache or by fetching it
// (verifying SHA-256 against the index digest, same policy as DIDX: a
// mismatch means corruption or tampering — fail, never serve wrong bytes).
func (r *FIDXReaderAt) chunkAt(ci int) ([]byte, error) {
	if r.cancel != nil && r.cancel() {
		return nil, ErrReadCancelled
	}
	if data, ok := r.cache.get(ci); ok {
		return data, nil
	}
	digest := r.digests[ci]
	chunk, err := r.pbs.GetChunkData(digest)
	if err != nil {
		return nil, fmt.Errorf("fetch chunk %s (index %d/%d): %w", digest, ci, len(r.digests), err)
	}
	// Every chunk spans exactly chunkSize image bytes except possibly the
	// last. PBS stores fixed chunks at full chunkSize (images are padded), but
	// tolerate a short FINAL chunk >= the logical remainder, since only the
	// remainder is addressable through this reader anyway.
	want := r.chunkSpan(ci)
	if uint64(len(chunk)) < want {
		return nil, fmt.Errorf("chunk %s (index %d): got %d bytes, need %d", digest, ci, len(chunk), want)
	}
	sum := sha256.Sum256(chunk)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, fmt.Errorf("chunk %s (index %d): content hash mismatch", digest, ci)
	}
	r.cache.put(ci, chunk)

	r.mu.Lock()
	r.fetched++
	fetched := r.fetched
	r.mu.Unlock()
	if r.progress != nil {
		r.progress(fetched, len(r.digests))
	}
	return chunk, nil
}

// chunkSpan is how many LOGICAL image bytes chunk ci covers (chunkSize, or
// the remainder for the final chunk of a non-aligned image).
func (r *FIDXReaderAt) chunkSpan(ci int) uint64 {
	start := uint64(ci) * r.chunkSize
	if start+r.chunkSize > r.size {
		return r.size - start
	}
	return r.chunkSize
}

// ReadAt implements io.ReaderAt over the raw disk image.
func (r *FIDXReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if r.cancel != nil && r.cancel() {
		return 0, ErrReadCancelled
	}
	if off < 0 {
		return 0, fmt.Errorf("fidx readerat: negative offset %d", off)
	}
	if uint64(off) >= r.size {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) {
		pos := uint64(off) + uint64(n)
		if pos >= r.size {
			break
		}
		ci := int(pos / r.chunkSize)
		chunk, err := r.chunkAt(ci)
		if err != nil {
			return n, err
		}
		inChunk := pos - uint64(ci)*r.chunkSize
		// Copy no further than the chunk's logical span (matters only on the
		// final, possibly padded chunk).
		span := r.chunkSpan(ci)
		if inChunk >= span {
			break
		}
		n += copy(p[n:], chunk[inChunk:span])
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
