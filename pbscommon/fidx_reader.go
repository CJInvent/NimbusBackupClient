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
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"sync"

	"golang.org/x/time/rate"
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

	// fetchFn retrieves one chunk's plaintext by digest. Defaults to
	// pbs.GetChunkData; injectable so the prefetch machinery is unit-testable
	// without a PBS server.
	fetchFn func(digest string) ([]byte, error)

	// Prefetch pipeline. A single HTTP stream is LATENCY-bound (~120 Mbps on a
	// LAN): every 4 MB chunk costs a full request round trip before the next
	// starts. Sequential readers (the $MFT scan, file extraction) tell us
	// exactly which chunks come next, so on each miss the reader also fetches
	// the next prefetchDepth chunks with up to prefetchWorkers concurrent
	// requests. GetChunkData is safe concurrently: fresh request + fresh zstd
	// decoder per call over net/http's pooled, thread-safe client.
	prefetchWorkers int
	prefetchDepth   int
	prefetchSem     chan struct{}

	inflightMu sync.Mutex
	inflight   map[int]chan struct{} // chunk index -> closed when its fetch finishes

	// limiter, when set, paces chunk downloads to the configured bandwidth so
	// image browsing honours the same network limits as backups.
	limiter *rate.Limiter

	mu      sync.Mutex
	fetched int
}

// SetRateLimitMbps caps chunk-download bandwidth in megabits/s (<= 0 removes
// the cap). Applies across all concurrent fetches: the whole pipeline shares
// one token bucket, so parallelism never multiplies past the limit.
func (r *FIDXReaderAt) SetRateLimitMbps(mbps float64) {
	if mbps <= 0 {
		r.limiter = nil
		return
	}
	bytesPerSec := mbps * 1000 * 1000 / 8
	// Burst of one chunk (of THIS index's chunk size) keeps WaitN legal for
	// chunk-sized requests without granting a free multi-chunk head start.
	burst := int(r.chunkSize)
	if burst < 1 {
		burst = 1
	}
	r.limiter = rate.NewLimiter(rate.Limit(bytesPerSec), burst)
}

// PlanPrefetch downloads exactly the chunks covering the given IMAGE-space
// byte ranges, in order, with `workers` concurrent requests — and nothing
// else. This is for consumers that know their read pattern up front (the
// NTFS $MFT scan hands its run list here): on a fragmented volume, image-
// linear read-ahead drags in unrelated data between fragments, while a plan
// moves only the bytes the scan will actually read. Fetches are best-effort
// (the real read retries and surfaces errors) and dedupe against the read
// path via the same in-flight table. The returned stop function cancels
// outstanding work and blocks until the workers exit.
func (r *FIDXReaderAt) PlanPrefetch(ranges [][2]int64, workers int) (stop func()) {
	if workers <= 0 {
		workers = 4
	}
	if workers > 64 {
		workers = 64 // HTTP/2 multiplexes; stay well under the server's stream cap
	}
	// Expand ranges to an ordered, de-duplicated chunk index list.
	seen := make(map[int]bool)
	var order []int
	for _, rg := range ranges {
		if rg[1] <= 0 {
			continue
		}
		first := int(rg[0] / int64(r.chunkSize))
		last := int((rg[0] + rg[1] - 1) / int64(r.chunkSize))
		for ci := first; ci <= last && ci < len(r.digests); ci++ {
			if ci >= 0 && !seen[ci] {
				seen[ci] = true
				order = append(order, ci)
			}
		}
	}
	r.cache.ensureCapacity(workers * 4)

	done := make(chan struct{})
	var wg sync.WaitGroup
	work := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ci := range work {
				if r.cancel != nil && r.cancel() {
					return
				}
				if _, ok := r.cache.get(ci); ok {
					continue
				}
				r.inflightMu.Lock()
				if _, busy := r.inflight[ci]; busy {
					r.inflightMu.Unlock()
					continue
				}
				ch := make(chan struct{})
				r.inflight[ci] = ch
				r.inflightMu.Unlock()
				_, _ = r.fetchAndCache(ci) // best-effort by design
				r.inflightMu.Lock()
				delete(r.inflight, ci)
				close(ch)
				r.inflightMu.Unlock()
			}
		}()
	}
	go func() {
		defer close(work)
		for _, ci := range order {
			select {
			case work <- ci:
			case <-done:
				return
			}
		}
	}()
	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() { close(done) })
		wg.Wait()
	}
}

// SetPrefetch enables read-ahead: up to depth chunks beyond the current read
// are fetched with up to workers concurrent requests. Call before reading;
// pass (0, 0) to disable (the default). The chunk cache must be larger than
// depth or prefetched data would evict itself — capacity is raised if needed.
func (r *FIDXReaderAt) SetPrefetch(workers, depth int) {
	if workers <= 0 || depth <= 0 {
		r.prefetchWorkers, r.prefetchDepth, r.prefetchSem = 0, 0, nil
		return
	}
	if workers > 16 {
		workers = 16
	}
	if depth > 64 {
		depth = 64
	}
	r.prefetchWorkers = workers
	r.prefetchDepth = depth
	r.prefetchSem = make(chan struct{}, workers)
	r.cache.ensureCapacity(depth * 2)
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
	r.fetchFn = pbs.GetChunkData
	r.inflight = make(map[int]chan struct{})
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

// chunkAt returns the plaintext of chunk ci — from cache, from an in-flight
// prefetch, or by fetching it — and kicks off read-ahead of the following
// chunks when prefetch is enabled.
func (r *FIDXReaderAt) chunkAt(ci int) ([]byte, error) {
	if r.cancel != nil && r.cancel() {
		return nil, ErrReadCancelled
	}
	data, err := r.ensureChunk(ci)
	if err != nil {
		return nil, err
	}
	r.schedulePrefetch(ci)
	return data, nil
}

// ensureChunk returns chunk ci, deduplicating against concurrent fetches of
// the same chunk: if another goroutine (usually a prefetch worker) is already
// downloading it, wait for that instead of fetching twice.
func (r *FIDXReaderAt) ensureChunk(ci int) ([]byte, error) {
	for {
		if data, ok := r.cache.get(ci); ok {
			return data, nil
		}
		r.inflightMu.Lock()
		if done, busy := r.inflight[ci]; busy {
			r.inflightMu.Unlock()
			<-done // fetch finished (or failed); loop to re-check the cache
			if data, ok := r.cache.get(ci); ok {
				return data, nil
			}
			// The in-flight fetch failed. Fall through and fetch it ourselves
			// so the CALLER gets the real error, not a stale cache miss.
		} else {
			done := make(chan struct{})
			r.inflight[ci] = done
			r.inflightMu.Unlock()
			data, err := r.fetchAndCache(ci)
			r.inflightMu.Lock()
			delete(r.inflight, ci)
			close(done)
			r.inflightMu.Unlock()
			return data, err
		}
	}
}

// fetchAndCache downloads chunk ci, verifies it against the index digest
// (mismatch = corruption or tampering — fail, never serve wrong bytes), and
// stores it in the cache.
func (r *FIDXReaderAt) fetchAndCache(ci int) ([]byte, error) {
	digest := r.digests[ci]
	if lim := r.limiter; lim != nil {
		n := int(r.chunkSpan(ci))
		if b := lim.Burst(); n > b {
			n = b
		}
		if err := lim.WaitN(context.Background(), n); err != nil {
			return nil, fmt.Errorf("rate limit wait: %w", err)
		}
	}
	chunk, err := r.fetchFn(digest)
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

// schedulePrefetch starts background fetches for the chunks after ci, bounded
// by prefetchDepth ahead and prefetchWorkers concurrent requests. Best-effort
// by design: a failed prefetch is simply retried synchronously when the read
// actually reaches that chunk, so the error surfaces on the real read path.
func (r *FIDXReaderAt) schedulePrefetch(ci int) {
	if r.prefetchSem == nil {
		return
	}
	for next := ci + 1; next <= ci+r.prefetchDepth && next < len(r.digests); next++ {
		if r.cancel != nil && r.cancel() {
			return
		}
		if _, ok := r.cache.get(next); ok {
			continue
		}
		r.inflightMu.Lock()
		if _, busy := r.inflight[next]; busy {
			r.inflightMu.Unlock()
			continue
		}
		select {
		case r.prefetchSem <- struct{}{}: // worker slot free
		default:
			r.inflightMu.Unlock()
			return // all workers busy; further look-ahead would just queue
		}
		done := make(chan struct{})
		r.inflight[next] = done
		r.inflightMu.Unlock()

		go func(idx int, done chan struct{}) {
			defer func() { <-r.prefetchSem }()
			if r.cancel == nil || !r.cancel() {
				_, _ = r.fetchAndCache(idx) // best-effort; real reads surface errors
			}
			r.inflightMu.Lock()
			delete(r.inflight, idx)
			close(done)
			r.inflightMu.Unlock()
		}(next, done)
	}
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
