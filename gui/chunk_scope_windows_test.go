//go:build windows

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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
	readerErr := make(chan error, 1)
	readerErr <- nil
	close(readerErr)
	counts := &chunkCounters{}
	if err := uploadWorker(client, counts, "drive.img.fidx", uint64(len(data)), ch, readerErr, func(float64, string) {}); err != nil {
		t.Fatal(err)
	}
	if uploads != 1 || len(assigned) != 1 || assigned[0] != want || counts.reusedChunks.Load() != 0 {
		t.Fatalf("image used wrong dedup scope: uploads=%d assigned=%v reused=%d", uploads, assigned, counts.reusedChunks.Load())
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

func TestImageConsumerUploadFailureAbortsBeforeIndexClose(t *testing.T) {
	var mu sync.Mutex
	closed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/previous":
			w.WriteHeader(404)
		case "/fixed_chunk":
			w.WriteHeader(500)
		case "/fixed_close":
			mu.Lock()
			closed = true
			mu.Unlock()
		default:
			_, _ = io.WriteString(w, `{"data":1}`)
		}
	}))
	defer srv.Close()
	client := &pbscommon.PBSClient{BaseURL: srv.URL, Client: *srv.Client(), WritersManifest: map[uint64]int{}}
	ch := make(chan []byte, 32)
	for i := 0; i < 32; i++ {
		ch <- bytes.Repeat([]byte{byte(i)}, 4096)
	}
	close(ch)
	readerErr := make(chan error, 1)
	readerErr <- nil
	result := make(chan error, 1)
	go func() {
		result <- uploadWorker(client, &chunkCounters{}, "disk.img.fidx", 32*4096, ch, readerErr, func(float64, string) {})
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("failed upload was successful")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upload workers did not terminate")
	}
	mu.Lock()
	defer mu.Unlock()
	if closed {
		t.Fatal("committed index after failed chunk")
	}
}
