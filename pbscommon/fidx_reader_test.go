package pbscommon

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// buildFIDX assembles a synthetic .fidx blob for the given image size and
// chunk size, returning the blob and the plaintext chunks keyed by digest.
func buildFIDX(t *testing.T, size, chunkSize uint64, fill func(i int, b []byte)) ([]byte, map[string][]byte) {
	t.Helper()
	var hdr FIDXHeader
	copy(hdr.Magic[:], fidxMagic)
	hdr.Size = size
	hdr.ChunkSize = chunkSize

	n := size / chunkSize
	if size%chunkSize != 0 {
		n++
	}
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, &hdr); err != nil {
		t.Fatal(err)
	}
	chunks := make(map[string][]byte, n)
	for i := uint64(0); i < n; i++ {
		// PBS stores fixed chunks at full chunkSize (padded); logical size
		// addressing is the reader's job.
		c := make([]byte, chunkSize)
		fill(int(i), c)
		sum := sha256.Sum256(c)
		buf.Write(sum[:])
		chunks[hex.EncodeToString(sum[:])] = c
	}
	return buf.Bytes(), chunks
}

func TestParseFIDX(t *testing.T) {
	raw, _ := buildFIDX(t, 10*1024, 4*1024, func(i int, b []byte) {
		for j := range b {
			b[j] = byte(i)
		}
	})
	r, err := parseFIDX(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if r.size != 10*1024 || r.chunkSize != 4*1024 {
		t.Fatalf("size/chunkSize wrong: %d/%d", r.size, r.chunkSize)
	}
	// ceil(10k / 4k) = 3 chunks
	if len(r.digests) != 3 {
		t.Fatalf("want 3 digests, got %d", len(r.digests))
	}
	// chunk spans: 4k, 4k, 2k
	if r.chunkSpan(0) != 4*1024 || r.chunkSpan(2) != 2*1024 {
		t.Fatalf("chunk spans wrong: %d %d", r.chunkSpan(0), r.chunkSpan(2))
	}
}

func TestParseFIDXBadMagic(t *testing.T) {
	raw, _ := buildFIDX(t, 4096, 4096, func(int, []byte) {})
	raw[0] ^= 0xFF
	if _, err := parseFIDX(raw); err == nil {
		t.Fatal("expected magic error")
	}
}

// testChunkSource satisfies chunk fetching without a server by injecting a
// lookup function; ReadAt paths are exercised through a reader whose chunkAt
// is fed from the map.
func newTestFIDXReader(t *testing.T, size, chunkSize uint64) (*FIDXReaderAt, []byte) {
	t.Helper()
	// Deterministic image content: byte at offset o = byte(o*7 + 3).
	img := make([]byte, size)
	for o := range img {
		img[o] = byte(o*7 + 3)
	}
	raw, chunks := buildFIDX(t, size, chunkSize, func(i int, b []byte) {
		start := uint64(i) * chunkSize
		for j := range b {
			if start+uint64(j) < size {
				b[j] = img[start+uint64(j)]
			}
		}
	})
	r, err := parseFIDX(raw)
	if err != nil {
		t.Fatal(err)
	}
	r.cache = newChunkCache(4)
	// Bypass the network: pre-seed every chunk into the cache. ReadAt then
	// exercises span math, copy offsets, and EOF handling exactly as in
	// production, minus the HTTP fetch (covered by integration use).
	for ci := range r.digests {
		r.cache.put(ci, chunks[r.digests[ci]])
	}
	return r, img
}

func TestFIDXReadAtSpansAndEOF(t *testing.T) {
	const size, cs = 10000, 4096 // 3 chunks, final logical span 1808
	r, img := newTestFIDXReader(t, size, cs)

	// Read crossing a chunk boundary.
	p := make([]byte, 1000)
	if n, err := r.ReadAt(p, 4000); err != nil || n != 1000 {
		t.Fatalf("cross-boundary read: n=%d err=%v", n, err)
	}
	if !bytes.Equal(p, img[4000:5000]) {
		t.Fatal("cross-boundary bytes wrong")
	}

	// Read into the short final span, hitting logical EOF.
	p = make([]byte, 4096)
	n, err := r.ReadAt(p, size-100)
	if err != io.EOF {
		t.Fatalf("want io.EOF at tail, got %v", err)
	}
	if n != 100 {
		t.Fatalf("tail read n=%d want 100", n)
	}
	if !bytes.Equal(p[:100], img[size-100:]) {
		t.Fatal("tail bytes wrong")
	}

	// Read entirely past EOF.
	if _, err := r.ReadAt(p, size); err != io.EOF {
		t.Fatalf("past-EOF want io.EOF, got %v", err)
	}

	// Negative offset.
	if _, err := r.ReadAt(p, -1); err == nil {
		t.Fatal("negative offset should error")
	}
}

func TestFIDXReadAtFullImage(t *testing.T) {
	const size, cs = 10000, 4096
	r, img := newTestFIDXReader(t, size, cs)
	got := make([]byte, size)
	if n, err := r.ReadAt(got, 0); err != nil && err != io.EOF {
		t.Fatalf("full read: %v", err)
	} else if n != size {
		t.Fatalf("full read n=%d", n)
	}
	if !bytes.Equal(got, img) {
		t.Fatal("full image mismatch")
	}
}

// TestFIDXPrefetchParallelism proves the prefetch pipeline actually overlaps
// requests (max observed concurrency > 1), never fetches a chunk twice
// (inflight dedupe), and returns byte-identical data — with a synthetic fetch
// function standing in for PBS, delayed to make serial vs parallel obvious.
func TestFIDXPrefetchParallelism(t *testing.T) {
	const size, cs = 64 * 4096, 4096 // 64 chunks
	// Build a deterministic image + index.
	img := make([]byte, size)
	for o := range img {
		img[o] = byte(o*13 + 7)
	}
	raw, chunks := buildFIDX(t, size, cs, func(i int, b []byte) {
		copy(b, img[uint64(i)*cs:])
	})
	r, err := parseFIDX(raw)
	if err != nil {
		t.Fatal(err)
	}
	r.cache = newChunkCache(8)
	r.inflight = make(map[int]chan struct{})

	var inFlight, maxInFlight, totalFetches int64
	var mu sync.Mutex
	r.fetchFn = func(digest string) ([]byte, error) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		totalFetches++
		mu.Unlock()
		time.Sleep(3 * time.Millisecond) // simulated network round trip
		mu.Lock()
		inFlight--
		mu.Unlock()
		c, ok := chunks[digest]
		if !ok {
			return nil, fmt.Errorf("unknown digest %s", digest)
		}
		return c, nil
	}
	r.SetPrefetch(6, 12)

	// Sequential read of the whole image, like the $MFT scan does.
	got := make([]byte, size)
	var pos int64
	buf := make([]byte, 3000) // deliberately not chunk-aligned
	for pos < size {
		n, err := r.ReadAt(buf, pos)
		if err != nil && err != io.EOF {
			t.Fatalf("ReadAt(%d): %v", pos, err)
		}
		copy(got[pos:], buf[:n])
		pos += int64(n)
		if n == 0 {
			break
		}
	}
	if !bytes.Equal(got, img) {
		t.Fatal("prefetched sequential read returned wrong bytes")
	}

	mu.Lock()
	defer mu.Unlock()
	if maxInFlight < 2 {
		t.Fatalf("no parallelism observed (max in-flight = %d)", maxInFlight)
	}
	// Dedupe: with a cache (8 raised to 24 by SetPrefetch) larger than the
	// prefetch window, a strictly forward read must fetch each chunk exactly
	// once.
	if totalFetches != 64 {
		t.Fatalf("expected exactly 64 fetches, got %d (dedupe broken?)", totalFetches)
	}
	t.Logf("max concurrent fetches: %d (workers=6)", maxInFlight)
}

// TestFIDXPrefetchErrorSurfaces: a failing prefetch must not poison the read —
// the error must surface on the synchronous path when the read arrives there.
func TestFIDXPrefetchErrorSurfaces(t *testing.T) {
	const size, cs = 8 * 4096, 4096
	raw, chunks := buildFIDX(t, size, cs, func(i int, b []byte) {
		for j := range b {
			b[j] = byte(i)
		}
	})
	r, err := parseFIDX(raw)
	if err != nil {
		t.Fatal(err)
	}
	r.cache = newChunkCache(8)
	r.inflight = make(map[int]chan struct{})
	var calls int64
	r.fetchFn = func(digest string) ([]byte, error) {
		// Chunk 3 fails on its first attempt (prefetch), succeeds after.
		if digest == r.digests[3] && atomic.AddInt64(&calls, 1) == 1 {
			return nil, fmt.Errorf("simulated transient failure")
		}
		c, ok := chunks[digest]
		if !ok {
			return nil, fmt.Errorf("unknown digest")
		}
		return c, nil
	}
	// Force chunk 3's first fetch to be the failing prefetch.
	r.SetPrefetch(2, 4)
	p := make([]byte, cs)
	if _, err := r.ReadAt(p, 2*cs); err != nil { // triggers prefetch of 3..6
		t.Fatalf("read chunk 2: %v", err)
	}
	// Give the prefetch a moment to fail.
	time.Sleep(20 * time.Millisecond)
	// Now the real read of chunk 3 must retry synchronously and succeed.
	if _, err := r.ReadAt(p, 3*cs); err != nil {
		t.Fatalf("read of chunk 3 after failed prefetch: %v", err)
	}
	if p[0] != 3 {
		t.Fatalf("wrong data after prefetch retry: %d", p[0])
	}
}
