package pbscommon

import "sync"

// ChunkSet is a concurrent set of chunk digests (hex-encoded SHA-256 strings).
//
// It replaces github.com/cornelk/hashmap, whose bulk sequential-insert cost is
// pathological: loading a previous index of ~194k digests took over three
// minutes in isolation and ~9.5 minutes on a machine that was also reading
// disk and running VSS — a silent ten-minute stall before the first chunk
// moved. The same load into this type is ~40ms.
//
// The set is populated single-threaded from the previous index (see
// GetKnownSha265FromFIDX) and then read and updated concurrently by the upload
// workers. An RWMutex guards it; because chunk uploads are network-bound, the
// lock is never the bottleneck during upload. The method set mirrors the
// subset of the old hashmap API the callers used (Get / Set / GetOrInsert /
// Len), so the change is a type swap at the call sites.
type ChunkSet struct {
	mu sync.RWMutex
	m  map[string]struct{}
}

// NewChunkSet returns an empty set.
func NewChunkSet() *ChunkSet {
	return &ChunkSet{m: make(map[string]struct{})}
}

// NewChunkSetSized returns an empty set preallocated for n entries, so a bulk
// load of a known size does not rehash repeatedly as it grows.
func NewChunkSetSized(n int) *ChunkSet {
	if n < 0 {
		n = 0
	}
	return &ChunkSet{m: make(map[string]struct{}, n)}
}

// Get reports whether digest is present. The first return mirrors the old
// (value, ok) shape of hashmap.Get; it equals ok, since the stored value was
// always true. Callers only inspect the second return.
func (s *ChunkSet) Get(digest string) (bool, bool) {
	s.mu.RLock()
	_, ok := s.m[digest]
	s.mu.RUnlock()
	return ok, ok
}

// Set adds digest to the set. The bool argument is ignored; it exists only to
// match the replaced hashmap.Set(key, value) signature.
func (s *ChunkSet) Set(digest string, _ bool) {
	s.mu.Lock()
	s.m[digest] = struct{}{}
	s.mu.Unlock()
}

// GetOrInsert adds digest if it is absent and reports whether it was already
// present. This matches cornelk's GetOrInsert semantics: loaded is true when
// the key already existed (an upload can be skipped as reuse) and false when
// this call inserted it (a new chunk to upload). The bool argument is ignored.
func (s *ChunkSet) GetOrInsert(digest string, _ bool) (value bool, loaded bool) {
	s.mu.Lock()
	_, loaded = s.m[digest]
	if !loaded {
		s.m[digest] = struct{}{}
	}
	s.mu.Unlock()
	return true, loaded
}

// Len returns the number of digests in the set.
func (s *ChunkSet) Len() int {
	s.mu.RLock()
	n := len(s.m)
	s.mu.RUnlock()
	return n
}

// addUnlocked inserts without taking the lock. It is only for the
// single-threaded bulk load, before any worker can touch the set.
func (s *ChunkSet) addUnlocked(digest string) {
	s.m[digest] = struct{}{}
}
