package pbscommon

// The raw-copy functions (rawcopy.go) move bytes between two datastores
// without decoding them. What they must prove is that the bytes and the
// parameters PBS checks cross unchanged: the body of every upload, the
// encoded-size and plaintext size of a chunk, the manifest entry of a blob,
// the backup-time of the writer session, and the checksum a copied index
// closes with. Each test below looks at what goes on the wire.
//
// Rule 25 (NimbusControl ARCHITECTURE.md): constants are pinned to upstream's
// own derivation, not to literals this package also encodes with.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// file_formats.rs states each magic as sha256(<preimage>)[0..8] in the comment
// above it. Recomputing them here is what makes a transcription typo fail.
func TestFormatMagicsAreUpstreamsDerivation(t *testing.T) {
	for _, c := range []struct {
		name, preimage string
		got            []byte
	}{
		{"FIXED_SIZED_CHUNK_INDEX_1_0", "Proxmox Backup fixed sized chunk index v1.0", fidxMagic},
		{"DYNAMIC_SIZED_CHUNK_INDEX_1_0", "Proxmox Backup dynamic sized chunk index v1.0", didxMagic},
		{"UNCOMPRESSED_BLOB_MAGIC_1_0", "Proxmox Backup uncompressed blob v1.0", UncompressedBlobMagic[:]},
		{"COMPRESSED_BLOB_MAGIC_1_0", "Proxmox Backup zstd compressed blob v1.0", CompressedBlobMagic[:]},
		{"ENCRYPTED_BLOB_MAGIC_1_0", "Proxmox Backup encrypted blob v1.0", EncryptedBlobMagic[:]},
		{"ENCR_COMPR_BLOB_MAGIC_1_0", "Proxmox Backup zstd compressed encrypted blob v1.0", EncrComprBlobMagic[:]},
	} {
		sum := sha256.Sum256([]byte(c.preimage))
		if !bytes.Equal(c.got, sum[:8]) {
			t.Errorf("%s = %v, upstream derives %v from %q", c.name, c.got, sum[:8], c.preimage)
		}
	}
}

// plainBlob is a stored unencrypted blob: magic, CRC of the payload, payload.
func plainBlob(magic [8]byte, payload []byte) []byte {
	out := append([]byte{}, magic[:]...)
	out = binary.LittleEndian.AppendUint32(out, BlobCRC(payload))
	return append(out, payload...)
}

func encryptedBlob(t *testing.T, compressed bool) []byte {
	t.Helper()
	c, err := NewCryptConfig(bytes.Repeat([]byte{7}, KeySize))
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("nimbus raw copy "), 64)
	var b []byte
	if compressed {
		b, err = c.EncodeEncryptedBlobCompressed(payload, []byte("pretend-zstd-frame"))
	} else {
		b, err = c.EncodeEncryptedBlob(payload)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCheckRawBlobAcceptsEveryStoredFormWithoutAKey(t *testing.T) {
	for name, b := range map[string][]byte{
		"uncompressed": plainBlob(UncompressedBlobMagic, []byte("hello")),
		"compressed":   plainBlob(CompressedBlobMagic, []byte("zstd bytes as stored")),
		"encrypted":    encryptedBlob(t, false),
		"encr+compr":   encryptedBlob(t, true),
	} {
		enc, err := CheckRawBlob(b)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if want := strings.HasPrefix(name, "encr"); enc != want {
			t.Errorf("%s: encrypted = %v, want %v", name, enc, want)
		}
	}
}

// The CRC covers what FOLLOWS the header, and the header is 44 bytes on an
// encrypted blob. A check that assumed 12 everywhere would pass a damaged
// ciphertext and fail a good one.
func TestCheckRawBlobRefusesDamagedBytes(t *testing.T) {
	enc := encryptedBlob(t, false)
	enc[EncryptedHeaderLen+3] ^= 0x01
	if _, err := CheckRawBlob(enc); err == nil || !strings.Contains(err.Error(), "CRC mismatch") {
		t.Errorf("a flipped ciphertext byte: %v, want a CRC mismatch", err)
	}
	plain := plainBlob(UncompressedBlobMagic, []byte("hello"))
	plain[len(plain)-1] ^= 0x80
	if _, err := CheckRawBlob(plain); err == nil {
		t.Error("a flipped payload byte passed")
	}
	if _, err := CheckRawBlob(encryptedBlob(t, false)[:40]); err == nil || !strings.Contains(err.Error(), "header alone is 44") {
		t.Errorf("an encrypted blob cut inside its header: %v", err)
	}
	bad := plainBlob(UncompressedBlobMagic, []byte("x"))
	bad[0] ^= 0xff
	if _, err := CheckRawBlob(bad); err == nil || !strings.Contains(err.Error(), "magic") {
		t.Errorf("an unknown magic: %v", err)
	}
	if _, err := CheckRawBlob([]byte("<html>502</html>")[:11]); err == nil {
		t.Error("eleven bytes passed as a blob")
	}
}

// captured is what one request looked like when it reached the server.
type captured struct {
	method, path string
	query        url.Values
	body         []byte
}

func recordingServer(t *testing.T, reply func(w http.ResponseWriter, r *http.Request)) (*PBSClient, *[]captured) {
	t.Helper()
	var seen []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen = append(seen, captured{r.Method, r.URL.Path, r.URL.Query(), b})
		reply(w, r)
	}))
	t.Cleanup(srv.Close)
	return &PBSClient{BaseURL: srv.URL, AuthID: "a@pbs!t", Secret: "s", Client: *srv.Client()}, &seen
}

func TestGetChunkRawReturnsTheStoredBytesUnchanged(t *testing.T) {
	stored := encryptedBlob(t, true)
	c, seen := recordingServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(stored) })
	got, err := c.GetChunkRaw("ab12")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, stored) {
		t.Fatal("the chunk came back changed: a raw read must not decode")
	}
	if s := (*seen)[0]; s.path != "/chunk" || s.query.Get("digest") != "ab12" {
		t.Fatalf("request %+v", s)
	}
}

func TestGetChunkRawSaysWhatPBSAnsweredAndRefusesDamage(t *testing.T) {
	c, _ := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "chunk not found", http.StatusNotFound)
	})
	_, err := c.GetChunkRaw("ab12")
	var st *HTTPStatusError
	if !errors.As(err, &st) || st.StatusCode != http.StatusNotFound || !strings.Contains(st.Body, "chunk not found") {
		t.Fatalf("a 404 must say what PBS answered: %v", err)
	}
	damaged := plainBlob(CompressedBlobMagic, []byte("frame"))
	damaged[14] ^= 1
	c2, _ := recordingServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(damaged) })
	if _, err := c2.GetChunkRaw("ab12"); err == nil || !strings.Contains(err.Error(), "CRC") {
		t.Fatalf("damaged bytes were returned for copying: %v", err)
	}
}

// encoded-size is the bytes on the wire; size is the PLAINTEXT length the
// copier takes from the index. Swapping them is invisible until a restore.
func TestPutEncodedChunkSendsTheBytesAndBothSizes(t *testing.T) {
	stored := encryptedBlob(t, true)
	c, seen := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if err := c.PutEncodedChunk(9, "cd34", stored, 4194304, true); err != nil {
		t.Fatal(err)
	}
	if err := c.PutEncodedChunk(9, "cd34", stored, 1000, false); err != nil {
		t.Fatal(err)
	}
	dyn, fix := (*seen)[0], (*seen)[1]
	if dyn.method != "POST" || dyn.path != "/dynamic_chunk" || fix.path != "/fixed_chunk" {
		t.Fatalf("endpoints %s %s, %s", dyn.method, dyn.path, fix.path)
	}
	if !bytes.Equal(dyn.body, stored) {
		t.Fatal("the uploaded body is not the stored bytes")
	}
	if dyn.query.Get("encoded-size") != strconv.Itoa(len(stored)) || dyn.query.Get("size") != "4194304" ||
		dyn.query.Get("digest") != "cd34" || dyn.query.Get("wid") != "9" {
		t.Fatalf("query %v", dyn.query)
	}
	if c.UploadedBytes() != uint64(2*len(stored)) {
		t.Errorf("UploadedBytes = %d: a copied chunk is sent bytes like any other", c.UploadedBytes())
	}
}

func TestUploadEncodedBlobStoresTheBytesAndRecordsThem(t *testing.T) {
	stored := encryptedBlob(t, false)
	c, seen := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if err := c.UploadEncodedBlob("catalog.pcat1.blob", stored); err != nil {
		t.Fatal(err)
	}
	s := (*seen)[0]
	if s.path != "/blob" || s.query.Get("file-name") != "catalog.pcat1.blob" ||
		s.query.Get("encoded-size") != strconv.Itoa(len(stored)) || !bytes.Equal(s.body, stored) {
		t.Fatalf("request %s %v (%d bytes)", s.path, s.query, len(s.body))
	}
	sum := sha256.Sum256(stored)
	f := c.Manifest.Files[0]
	if f.Filename != "catalog.pcat1.blob" || f.Csum != hex.EncodeToString(sum[:]) ||
		f.Size != int64(len(stored)) || f.CryptMode != "encrypt" {
		t.Fatalf("manifest entry %+v", f)
	}

	plain := plainBlob(UncompressedBlobMagic, []byte(`{"x":1}`))
	if err := c.UploadEncodedBlob("qemu-server.conf.blob", plain); err != nil {
		t.Fatal(err)
	}
	if c.Manifest.Files[1].CryptMode != "none" {
		t.Errorf("a plain blob recorded as %q", c.Manifest.Files[1].CryptMode)
	}
}

func TestUploadEncodedBlobRefusesDamageBeforeSendingAnything(t *testing.T) {
	bad := plainBlob(UncompressedBlobMagic, []byte("payload"))
	bad[len(bad)-1] ^= 1
	c, seen := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if err := c.UploadEncodedBlob("x.blob", bad); err == nil {
		t.Fatal("a damaged blob was accepted")
	}
	if len(*seen) != 0 || len(c.Manifest.Files) != 0 {
		t.Fatalf("a refused blob reached the server (%d requests) or the manifest (%d)", len(*seen), len(c.Manifest.Files))
	}
}

// UploadBlob now posts through the same postBlob: what it reports to /finish
// must be what it reported before the refactor.
func TestUploadBlobStillRecordsTheStoredBytes(t *testing.T) {
	c, seen := recordingServer(t, func(w http.ResponseWriter, r *http.Request) {})
	if err := c.UploadBlob(ManifestBlobName, []byte(`{"files":[]}`)); err != nil {
		t.Fatal(err)
	}
	body := (*seen)[0].body
	sum := sha256.Sum256(body)
	f := c.Manifest.Files[0]
	if f.Csum != hex.EncodeToString(sum[:]) || f.Size != int64(len(body)) || f.CryptMode != "none" {
		t.Fatalf("manifest entry %+v for %d stored bytes", f, len(body))
	}
	if _, err := CheckRawBlob(body); err != nil {
		t.Fatalf("UploadBlob stored something that is not a blob: %v", err)
	}
}

// The copy must land at the SOURCE snapshot's time; Connect(false) stamps now.
func TestConnectWriterAtOpensTheSessionAtTheGivenTime(t *testing.T) {
	ca := newDPCA(t)
	in := startInstance(t, ca, "101")
	c := grantClient(in, &DataPath{Cert: ca.clientCert(t, "nimbus-ns-sync"), Endpoints: []Endpoint{{in.url, in.fp}}})
	c.ConnectWriterAt("vm", 1783486801)
	resp, err := c.Client.Get(c.BaseURL + "/probe")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	c.Close()
	up := in.upgrades()
	if len(up) != 1 || !strings.HasPrefix(up[0].line, "/api2/json/backup?") ||
		!strings.Contains(up[0].line, "backup-time=1783486801") || !strings.Contains(up[0].line, "backup-type=vm") {
		t.Fatalf("upgrade %+v", up)
	}
	if c.Manifest.BackupTime != 1783486801 {
		t.Errorf("Manifest.BackupTime = %d", c.Manifest.BackupTime)
	}

	// And Connect(false) is unchanged: a new backup is stamped now.
	before := time.Now().Unix()
	c.Connect(false, "host")
	c.Close()
	if c.Manifest.BackupTime < before {
		t.Errorf("Connect(false) kept a stale time %d", c.Manifest.BackupTime)
	}
}

// buildIndex writes an index the way PBS's IndexWriter lays it out
// (pbs-datastore/src/{dynamic,fixed}_index.rs): a 4096-byte header with the
// magic, a uuid, ctime, index_csum at 32..64 and, for a fixed index, size and
// chunk_size at 64 and 72; then the entries. index_csum is the SHA-256 of the
// entry section (DynamicIndexWriter::close hashes each end_le || digest;
// FixedIndexWriter's close hashes the digest table).
func buildIndex(dynamic bool, size, chunkSize uint64, ends []uint64, digests [][32]byte) []byte {
	raw := make([]byte, 4096)
	if dynamic {
		copy(raw, didxMagic)
	} else {
		copy(raw, fidxMagic)
		binary.LittleEndian.PutUint64(raw[64:], size)
		binary.LittleEndian.PutUint64(raw[72:], chunkSize)
	}
	copy(raw[8:24], []byte("0123456789abcdef"))
	binary.LittleEndian.PutUint64(raw[24:], 1783486801)
	var section []byte
	for i, d := range digests {
		if dynamic {
			section = binary.LittleEndian.AppendUint64(section, ends[i])
		}
		section = append(section, d[:]...)
	}
	sum := sha256.Sum256(section)
	copy(raw[32:64], sum[:])
	return append(raw, section...)
}

func digestsN(n int) [][32]byte {
	out := make([][32]byte, n)
	for i := range out {
		out[i] = sha256.Sum256([]byte(fmt.Sprintf("chunk %d", i)))
	}
	return out
}

func TestParseIndexFileDynamic(t *testing.T) {
	d := digestsN(3)
	raw := buildIndex(true, 0, 0, []uint64{100, 4196, 5000}, d)
	ix, err := ParseIndexFile("drive-c.pxar.didx", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !ix.Dynamic || ix.Size != 5000 || ix.ChunkCount() != 3 || ix.Digests[1] != hex.EncodeToString(d[1][:]) {
		t.Fatalf("%+v", ix)
	}
	for i, want := range [][2]uint64{{0, 100}, {100, 4096}, {4196, 804}} {
		if ix.ChunkStart(i) != want[0] || ix.ChunkLen(i) != want[1] {
			t.Errorf("chunk %d: start %d len %d, want %v", i, ix.ChunkStart(i), ix.ChunkLen(i), want)
		}
	}
	if want := sha256.Sum256(raw[4096:]); ix.Csum != want {
		t.Error("Csum is not the header's index_csum")
	}
}

// A fixed index's last chunk is shorter when the image is not a multiple of
// the chunk size, and its upload must declare that shorter size.
func TestParseIndexFileFixedWithAShortLastChunk(t *testing.T) {
	const cs = 4 << 20
	size := uint64(2*cs + 12345)
	raw := buildIndex(false, size, cs, nil, digestsN(3))
	ix, err := ParseIndexFile("drive-scsi0.img.fidx", raw)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Dynamic || ix.Size != size || ix.ChunkSize != cs || ix.ChunkCount() != 3 {
		t.Fatalf("%+v", ix)
	}
	if ix.ChunkStart(2) != 2*cs || ix.ChunkLen(2) != 12345 || ix.ChunkLen(0) != cs {
		t.Fatalf("last chunk start %d len %d", ix.ChunkStart(2), ix.ChunkLen(2))
	}
}

func TestParseIndexFileRefusesWhatCannotBeCopiedFaithfully(t *testing.T) {
	good := buildIndex(true, 0, 0, []uint64{10, 20}, digestsN(2))
	tampered := append([]byte{}, good...)
	tampered[4096+8] ^= 1 // a digest byte: the entries no longer match index_csum
	if _, err := ParseIndexFile("a.didx", tampered); err == nil || !strings.Contains(err.Error(), "index_csum") {
		t.Errorf("an index whose entries disagree with its checksum: %v", err)
	}
	fixed := buildIndex(false, 8192, 4096, nil, digestsN(2))
	if _, err := ParseIndexFile("b.fidx", append(fixed, make([]byte, 32)...)); err == nil {
		t.Error("a fixed index with a digest more than its header declares was accepted")
	}
	if _, err := ParseIndexFile("c.didx", buildIndex(true, 0, 0, []uint64{20, 10}, digestsN(2))); err == nil {
		t.Error("descending end offsets were accepted")
	}
	notIndex := append([]byte("{\"errors\":\"no valid previous backup\"}"), make([]byte, 4096)...)
	if _, err := ParseIndexFile("d.didx", notIndex); err == nil || !strings.Contains(err.Error(), "not a PBS index") {
		t.Errorf("PBS's error text passed as an index: %v", err)
	}
	if _, err := ParseIndexFile("e.didx", good[:4000]); err == nil {
		t.Error("a truncated header was accepted")
	}
}
