package pbscommon

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestEncryptedReadersVerifyScopedAddresses(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	plain := bytes.Repeat([]byte("restore evidence"), 128)
	id, err := pbkdf2.Key(sha256.New, string(key), []byte("_id_key"), 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, _ = h.Write(plain)
	_, _ = h.Write(id)
	digest := h.Sum(nil)
	cc, err := NewCryptConfig(key)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cc.EncodeEncryptedBlob(plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "wrong_address"}[corrupt], func(t *testing.T) {
			address := append([]byte(nil), digest...)
			if corrupt {
				address[0] ^= 1
			}
			fidx := make([]byte, 4096)
			copy(fidx, []byte{47, 127, 65, 237, 145, 253, 15, 205})
			// FIDX: magic 8, UUID 16, ctime 8, checksum 32, size 8, chunk size 8.
			binary.LittleEndian.PutUint64(fidx[64:], uint64(len(plain)))
			binary.LittleEndian.PutUint64(fidx[72:], uint64(len(plain)))
			fidx = append(fidx, address...)
			didx := make([]byte, 4096)
			copy(didx, []byte{28, 145, 78, 165, 25, 186, 179, 205})
			didx = binary.LittleEndian.AppendUint64(didx, uint64(len(plain)))
			didx = append(didx, address...)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/chunk" {
					if r.URL.Query().Get("digest") != hex.EncodeToString(address) {
						t.Error("reader fetched wrong address")
					}
					_, _ = w.Write(encrypted)
				} else if r.URL.Query().Get("file-name") == "disk.img.fidx" {
					_, _ = w.Write(fidx)
				} else {
					_, _ = w.Write(didx)
				}
			}))
			defer srv.Close()
			p := &PBSClient{BaseURL: srv.URL, Client: *srv.Client()}
			if _, err := p.SetCryptKey(key); err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			fr, _, err := p.NewFIDXReaderAt("disk.img.fidx", 2, nil)
			if err != nil {
				t.Fatal(err)
			}
			dr, _, err := p.NewDIDXReaderAt("backup.pxar.didx", 2, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range []io.ReaderAt{fr, dr} {
				got := make([]byte, len(plain))
				_, err := r.ReadAt(got, 0)
				if corrupt {
					if err == nil {
						t.Fatal("accepted wrong scoped digest")
					}
				} else if err != nil || !bytes.Equal(got, plain) {
					t.Fatalf("restore differs: %v", err)
				}
			}
			path, _, err := p.AssembleDIDXToFile("backup.pxar.didx", 1, nil)
			if corrupt {
				if err == nil {
					_ = os.Remove(path)
					t.Fatal("assembler accepted wrong digest")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer os.Remove(path)
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, plain) {
					t.Fatalf("assembled restore differs: %v", err)
				}
			}
		})
	}
}

func TestUploadRejectsPlainAddressWithEncryption(t *testing.T) {
	p := &PBSClient{}
	if _, err := p.SetCryptKey(bytes.Repeat([]byte{1}, 32)); err != nil {
		t.Fatal(err)
	}
	plain := []byte("must never reach PBS at a plain address")
	digest := sha256.Sum256(plain)
	if err := p.UploadFixedCompressedChunk(0, hex.EncodeToString(digest[:]), plain); err == nil {
		t.Fatal("accepted a plain address under encryption")
	}
}
