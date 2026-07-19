package pbscommon

// Chunk-fetch deduplication under concurrency.
//
// ensureChunk coordinates concurrent readers through r.inflight so that N
// goroutines wanting the same chunk cost ONE download. That mattered enough to
// build, and it was subtly broken: a goroutine could miss the cache, then take
// inflightMu AFTER the goroutine that was fetching had already cached its
// result and deregistered itself. It would then see neither the data nor an
// in-flight marker, and download the same chunk again.
//
// The window is small but it is widest exactly when it hurts most — many
// readers converging on one chunk, over a link where a duplicate 4 MB fetch is
// real money on a metered or CGNAT connection.

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestFIDXConcurrentReadersFetchEachChunkOnce(t *testing.T) {
	const size, cs = 8 * 4096, 4096 // 8 chunks
	img := make([]byte, size)
	for o := range img {
		img[o] = byte(o*11 + 5)
	}
	raw, chunks := buildFIDX(t, size, cs, func(i int, b []byte) {
		copy(b, img[uint64(i)*cs:])
	})
	r, err := parseFIDX(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Cache big enough to hold every chunk, so a second fetch can only be a
	// deduplication failure and never an eviction.
	r.cache = newChunkCache(len(r.digests) * 2)
	r.inflight = make(map[int]chan struct{})

	var mu sync.Mutex
	fetches := map[string]int{}
	r.fetchFn = func(digest string) ([]byte, error) {
		mu.Lock()
		fetches[digest]++
		mu.Unlock()
		// Widen the window between "cache miss" and "registered in flight"
		// that the bug lived in.
		time.Sleep(2 * time.Millisecond)
		c, ok := chunks[digest]
		if !ok {
			return nil, fmt.Errorf("unknown digest %s", digest)
		}
		return c, nil
	}

	// Many readers, all converging on the same chunks at the same moment.
	const readers = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			for ci := 0; ci < len(r.digests); ci++ {
				buf := make([]byte, cs)
				off := int64(ci) * cs
				if _, err := r.ReadAt(buf, off); err != nil {
					t.Errorf("reader %d ReadAt(%d): %v", n, off, err)
					return
				}
				want := img[off : off+cs]
				for j := range buf {
					if buf[j] != want[j] {
						t.Errorf("reader %d chunk %d: wrong byte at %d", n, ci, j)
						return
					}
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(fetches) != len(r.digests) {
		t.Errorf("fetched %d distinct chunks, want %d", len(fetches), len(r.digests))
	}
	for digest, n := range fetches {
		if n != 1 {
			t.Errorf("chunk %s fetched %d times — %d concurrent readers should cost one download",
				digest, n, readers)
		}
	}
}
