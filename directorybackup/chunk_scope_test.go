package main

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"pbscommon"
)

// Compute the PBS contract independently of ChunkDigest and CryptConfig.
func expectedChunkAddress(t *testing.T, key, plain []byte) string {
	t.Helper()
	id, err := pbkdf2.Key(sha256.New, string(key), []byte("_id_key"), 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, _ = h.Write(plain)
	_, _ = h.Write(id)
	return hex.EncodeToString(h.Sum(nil))
}

func TestDirectoryConsumerUsesScopedDigests(t *testing.T) {
	for _, keyByte := range []byte{1, 2} {
		t.Run(string(rune('a'+keyByte)), func(t *testing.T) {
			key := bytes.Repeat([]byte{keyByte}, 32)
			crypt, err := pbscommon.NewCryptConfig(key)
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			uploaded := map[string][]byte{}
			var indexed []string
			var ends []uint64
			var closeReq pbscommon.IndexCloseReq
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/dynamic_chunk":
					blob, readErr := io.ReadAll(r.Body)
					if readErr != nil {
						t.Error(readErr)
						w.WriteHeader(500)
						return
					}
					plain, decErr := crypt.DecodeEncryptedBlob(blob)
					if decErr != nil {
						t.Error(decErr)
						w.WriteHeader(500)
						return
					}
					digest := r.URL.Query().Get("digest")
					if digest != expectedChunkAddress(t, key, plain) {
						t.Error("upload addressed outside key scope")
					}
					uploaded[digest] = plain
				case "/dynamic_index":
					var req pbscommon.IndexPutReq
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					indexed = append(indexed, req.DigestList...)
					ends = append(ends, req.OffsetList...)
				case "/dynamic_close":
					if err := json.NewDecoder(r.Body).Decode(&closeReq); err != nil {
						t.Error(err)
					}
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
				_, _ = io.WriteString(w, `{"data":1}`)
			}))
			defer srv.Close()
			client := &pbscommon.PBSClient{BaseURL: srv.URL, Client: *srv.Client(), WritersManifest: map[uint64]int{0: 0}}
			client.Manifest.Files = append(client.Manifest.Files, pbscommon.File{})
			if _, err := client.SetCryptKey(key); err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			var fresh, reused atomic.Uint64
			cs := ChunkState{}
			cs.Init(&fresh, &reused, pbscommon.NewChunkSet())
			cs.C.New(64)
			data := make([]byte, 8193)
			for i := range data {
				data[i] = byte(i*i + 13*i + 7)
			}
			if err := cs.HandleData(data, client); err != nil {
				t.Fatal(err)
			}
			if err := cs.Eof(client); err != nil {
				t.Fatal(err)
			}
			if len(indexed) < 2 || len(uploaded) == 0 {
				t.Fatal("test did not exercise boundary and final chunks")
			}
			var restored bytes.Buffer
			checksum := sha256.New()
			for i, digest := range indexed {
				chunk, ok := uploaded[digest]
				if !ok {
					t.Fatalf("index references unuploaded digest %s", digest)
				}
				restored.Write(chunk)
				end := ends[i] + uint64(len(chunk))
				if err := binary.Write(checksum, binary.LittleEndian, end); err != nil {
					t.Fatal(err)
				}
				raw, err := hex.DecodeString(digest)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = checksum.Write(raw)
			}
			if !bytes.Equal(restored.Bytes(), data) {
				t.Fatal("restored consumer output differs")
			}
			if closeReq.CheckSum != hex.EncodeToString(checksum.Sum(nil)) {
				t.Fatal("DIDX checksum does not cover scoped digests")
			}
		})
	}
}
