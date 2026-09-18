package main

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"pbscommon"
)

// Execute the actual Windows image upload worker, with a previous plain
// snapshot. A manifest flag cannot make those old addresses safe to reuse.
func TestImageConsumerDoesNotReusePlainChunksUnderEncryption(t *testing.T) {
	key := bytes.Repeat([]byte{3}, 32)
	data := bytes.Repeat([]byte("image-data"), 4096)
	want := expectedChunkAddress(t, key, data)
	plain := sha256.Sum256(data)
	previous := make([]byte, 4096)
	copy(previous, []byte{47, 127, 65, 237, 145, 253, 15, 205})
	previous = append(previous, plain[:]...)
	crypt, err := pbscommon.NewCryptConfig(key)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	uploads := 0
	var assigned []string
	var closeReq pbscommon.IndexCloseReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/previous":
			_, _ = w.Write(previous)
			return
		case "/fixed_chunk":
			blob, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			restored, err := crypt.DecodeEncryptedBlob(blob)
			if err != nil || !bytes.Equal(restored, data) {
				t.Errorf("uploaded image cannot restore: %v", err)
			}
			if r.URL.Query().Get("digest") != want {
				t.Error("wrong chunk scope")
			}
			uploads++
		case "/fixed_index":
			if r.Method == http.MethodPut {
				var req pbscommon.IndexPutReq
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				assigned = append(assigned, req.DigestList...)
			}
		case "/fixed_close":
			if err := json.NewDecoder(r.Body).Decode(&closeReq); err != nil {
				t.Error(err)
			}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":1}`)
	}))
	defer srv.Close()
	client := &pbscommon.PBSClient{BaseURL: srv.URL, Client: *srv.Client(), WritersManifest: map[uint64]int{}}
	if _, err := client.SetCryptKey(key); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ch := make(chan []byte, 1)
	ch <- data
	close(ch)
	if err := uploadWorker(client, "drive.img.fidx", uint64(len(data)), ch); err != nil {
		t.Fatal(err)
	}
	if uploads != 1 || len(assigned) != 1 || assigned[0] != want {
		t.Fatalf("wrong image scope: uploads=%d assigned=%v", uploads, assigned)
	}
	raw, err := hex.DecodeString(want)
	if err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(raw)
	if closeReq.CheckSum != hex.EncodeToString(checksum[:]) {
		t.Fatal("FIDX checksum did not cover scoped digest")
	}
}

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
