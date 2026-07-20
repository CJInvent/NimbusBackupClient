package pbscommon

import (
	"encoding/binary"
	"strings"
	"testing"
)

// buildDIDX assembles a .didx byte image with the given cumulative end-offsets.
// Digests are arbitrary; only the offset ordering is under test.
func buildDIDX(offsets []uint64) []byte {
	out := make([]byte, didxHeaderSize)
	copy(out, didxMagic)
	entry := make([]byte, didxEntrySize)
	for _, off := range offsets {
		binary.LittleEndian.PutUint64(entry[:8], off)
		for i := 8; i < didxEntrySize; i++ {
			entry[i] = byte(off) // filler digest
		}
		out = append(out, entry...)
	}
	return out
}

// A dynamic index whose cumulative end-offsets are not strictly ascending is
// untrusted input a hostile or corrupt PBS can serve. Before validation it did
// not produce a wrong answer — it crashed: end-start underflows in uint64 and
// ReadAt slices chunk[pos-start:] with a wrapped index. It must be refused at
// parse time.
func TestDIDXRejectsNonAscendingOffsets(t *testing.T) {
	cases := []struct {
		name    string
		offsets []uint64
	}{
		{"decreasing", []uint64{4096, 8192, 6000}},
		{"repeated (zero-length chunk)", []uint64{4096, 4096, 8192}},
		{"first offset zero", []uint64{0, 4096}},
		{"resets to zero mid-stream", []uint64{4096, 8192, 0, 12288}},
		{"wraps to a huge value then back", []uint64{4096, 1 << 63, 8192}},
	}
	for _, c := range cases {
		_, err := parseDIDXIndex("test.didx", buildDIDX(c.offsets))
		if err == nil {
			t.Errorf("%s: accepted a non-ascending index, want refusal", c.name)
			continue
		}
		if !strings.Contains(err.Error(), "ascending") {
			t.Errorf("%s: error %q does not explain the ordering violation", c.name, err)
		}
	}
}

func TestDIDXAcceptsAscendingOffsets(t *testing.T) {
	idx, err := parseDIDXIndex("test.didx", buildDIDX([]uint64{4096, 8192, 12288}))
	if err != nil {
		t.Fatalf("a valid ascending index was refused: %v", err)
	}
	if idx.total != 12288 {
		t.Errorf("total = %d, want 12288", idx.total)
	}
	if len(idx.digests) != 3 {
		t.Errorf("digest count = %d, want 3", len(idx.digests))
	}
	// The span arithmetic the reader relies on must hold.
	if s, e := idx.chunkRange(0); s != 0 || e != 4096 {
		t.Errorf("chunkRange(0) = %d,%d want 0,4096", s, e)
	}
	if s, e := idx.chunkRange(2); s != 8192 || e != 12288 {
		t.Errorf("chunkRange(2) = %d,%d want 8192,12288", s, e)
	}
}

// An empty index is legal (a zero-length archive) and must not trip the check.
func TestDIDXEmptyIndexIsValid(t *testing.T) {
	idx, err := parseDIDXIndex("test.didx", buildDIDX(nil))
	if err != nil {
		t.Fatalf("empty index refused: %v", err)
	}
	if idx.total != 0 || len(idx.digests) != 0 {
		t.Errorf("empty index parsed non-empty: total=%d digests=%d", idx.total, len(idx.digests))
	}
}
