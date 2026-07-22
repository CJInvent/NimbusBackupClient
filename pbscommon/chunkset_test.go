package pbscommon

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"sync"
	"testing"
)

// buildTestFIDX makes a synthetic, valid .fidx carrying the given raw digests.
// hdr.Size/ChunkSize are set to "plausible" values on purpose so the tests can
// prove the parser ignores them and counts from the byte length instead.
func buildTestFIDX(digests [][]byte) []byte {
	var b bytes.Buffer
	var hdr FIDXHeader
	copy(hdr.Magic[:], []byte{47, 127, 65, 237, 145, 253, 15, 205})
	hdr.Size = uint64(len(digests)) * 4 * 1024 * 1024
	hdr.ChunkSize = 4 * 1024 * 1024
	_ = binary.Write(&b, binary.LittleEndian, &hdr)
	for _, d := range digests {
		b.Write(d)
	}
	return b.Bytes()
}

func mkTestDigests(n int) [][]byte {
	out := make([][]byte, n)
	raw := make([]byte, 32*n)
	_, _ = rand.Read(raw)
	for i := 0; i < n; i++ {
		out[i] = raw[i*32 : i*32+32]
	}
	return out
}

func TestParseFIDXRoundTrip(t *testing.T) {
	digs := mkTestDigests(4096)
	cs, err := parseFIDXIndex(buildTestFIDX(digs))
	if err != nil {
		t.Fatal(err)
	}
	if cs.Len() != len(digs) {
		t.Fatalf("Len()=%d want %d", cs.Len(), len(digs))
	}
	for _, d := range digs {
		if _, ok := cs.Get(hex.EncodeToString(d)); !ok {
			t.Fatalf("digest %x missing after parse", d)
		}
	}
	if _, ok := cs.Get(hex.EncodeToString(make([]byte, 32))); ok {
		t.Fatal("absent digest reported present")
	}
}

// The count must come from the byte length, not the header. An empty index
// (header only) is valid and yields an empty set.
func TestParseFIDXEmptyIsValid(t *testing.T) {
	cs, err := parseFIDXIndex(buildTestFIDX(nil))
	if err != nil {
		t.Fatalf("empty index should be valid: %v", err)
	}
	if cs.Len() != 0 {
		t.Fatalf("Len()=%d want 0", cs.Len())
	}
}

func TestParseFIDXRejectsMalformed(t *testing.T) {
	cases := map[string][]byte{
		"short header": make([]byte, 100),
		"bad magic": func() []byte {
			b := buildTestFIDX(mkTestDigests(2))
			b[0] ^= 0xff
			return b
		}(),
		"digest region not multiple of 32": func() []byte {
			b := buildTestFIDX(mkTestDigests(3))
			return b[:len(b)-5]
		}(),
		"truncated mid-digest": func() []byte {
			b := buildTestFIDX(mkTestDigests(10))
			return b[:4096+32*10-16] // drops 16 bytes of the last digest
		}(),
	}
	for name, blob := range cases {
		if _, err := parseFIDXIndex(blob); err == nil {
			t.Errorf("%s: expected an error, got nil", name)
		}
	}
}

func TestChunkSetGetOrInsert(t *testing.T) {
	cs := NewChunkSet()
	if _, loaded := cs.GetOrInsert("aa", true); loaded {
		t.Fatal("first GetOrInsert should report loaded=false (new)")
	}
	if _, loaded := cs.GetOrInsert("aa", true); !loaded {
		t.Fatal("second GetOrInsert should report loaded=true (present)")
	}
	if _, ok := cs.Get("aa"); !ok {
		t.Fatal("Get after GetOrInsert should find the key")
	}
	cs.Set("bb", true)
	if _, ok := cs.Get("bb"); !ok {
		t.Fatal("Get after Set should find the key")
	}
	if cs.Len() != 2 {
		t.Fatalf("Len()=%d want 2", cs.Len())
	}
}

// Run under -race in CI: readers and writers hit the set concurrently the way
// the upload workers do.
func TestChunkSetConcurrent(t *testing.T) {
	cs, err := parseFIDXIndex(buildTestFIDX(mkTestDigests(1000)))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 3000; i++ {
				k := hex.EncodeToString([]byte{byte(g), byte(i), byte(i >> 8), 0})
				cs.GetOrInsert(k, true)
				_, _ = cs.Get(k)
				cs.Set(k, true)
			}
		}(g)
	}
	wg.Wait()
}
