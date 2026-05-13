package pbscommon

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
)

// 8-byte magic prefixing every PBS dynamic-index (.didx) file.
var didxMagic = []byte{28, 145, 78, 165, 25, 186, 179, 205}

const (
	didxHeaderSize = 4096
	didxEntrySize  = 40 // 8 bytes cumulative end-offset (uint64 LE) + 32 bytes SHA-256 digest
)

// AssembleDIDX downloads a dynamic-index archive (e.g. "backup.pxar.didx")
// from PBS and reconstructs the original byte stream by fetching every
// referenced chunk and concatenating them in order.
//
// The .didx file is NOT the archive itself — it is an index of cumulative
// end-offsets and chunk digests. Calling DownloadToBytes alone returns only
// this index; parsing those bytes as PXAR will fail. Use this helper instead.
//
// Chunks are fetched with bounded parallelism (maxParallel; defaults to 8).
// progress is invoked after each chunk lands with (done, total). It may be nil.
//
// The full assembled stream is materialised in memory. A streaming variant
// will be needed once we routinely restore archives bigger than a few GB.
func (pbs *PBSClient) AssembleDIDX(archiveName string, maxParallel int, progress func(done, total int)) ([]byte, error) {
	if maxParallel <= 0 {
		maxParallel = 8
	}

	indexBytes, err := pbs.DownloadToBytes(archiveName)
	if err != nil {
		return nil, fmt.Errorf("download index %q: %w", archiveName, err)
	}
	if len(indexBytes) < didxHeaderSize {
		return nil, fmt.Errorf("index %q: short read (%d bytes, need at least %d)",
			archiveName, len(indexBytes), didxHeaderSize)
	}
	if !bytes.HasPrefix(indexBytes, didxMagic) {
		return nil, fmt.Errorf("index %q: invalid DIDX magic", archiveName)
	}

	entries := indexBytes[didxHeaderSize:]
	if len(entries)%didxEntrySize != 0 {
		return nil, fmt.Errorf("index %q: entries section length %d is not a multiple of %d",
			archiveName, len(entries), didxEntrySize)
	}
	chunkCount := len(entries) / didxEntrySize
	if chunkCount == 0 {
		return []byte{}, nil
	}

	offsets := make([]uint64, chunkCount)
	digests := make([]string, chunkCount)
	for i := 0; i < chunkCount; i++ {
		base := i * didxEntrySize
		offsets[i] = binary.LittleEndian.Uint64(entries[base : base+8])
		digests[i] = hex.EncodeToString(entries[base+8 : base+40])
	}

	totalSize := offsets[chunkCount-1]
	// 1 TiB sanity cap — anything bigger needs the streaming variant.
	if totalSize > 1<<40 {
		return nil, fmt.Errorf("assembled archive too large for in-memory restore: %d bytes (cap 1 TiB)", totalSize)
	}
	out := make([]byte, totalSize)

	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	var firstErr atomic.Value
	var done atomic.Int64

	for i := 0; i < chunkCount; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()

			if firstErr.Load() != nil {
				return
			}
			chunk, gerr := pbs.GetChunkData(digests[idx])
			if gerr != nil {
				firstErr.CompareAndSwap(nil, fmt.Errorf("chunk %s (index %d/%d): %w",
					digests[idx], idx, chunkCount, gerr))
				return
			}
			start := uint64(0)
			if idx > 0 {
				start = offsets[idx-1]
			}
			end := offsets[idx]
			expected := int(end - start)
			if len(chunk) != expected {
				firstErr.CompareAndSwap(nil, fmt.Errorf("chunk %s (index %d): decompressed size %d != expected %d",
					digests[idx], idx, len(chunk), expected))
				return
			}
			copy(out[start:end], chunk)

			n := done.Add(1)
			if progress != nil {
				progress(int(n), chunkCount)
			}
		}(i)
	}

	wg.Wait()

	if v := firstErr.Load(); v != nil {
		return nil, v.(error)
	}
	return out, nil
}
