package api

import (
	"net/http/httptest"
	"testing"
)

type storageAPIStub struct {
	*stubHandler
	approved bool
}

func (h *storageAPIStub) GetStorageIdentityStatus() (map[string]interface{}, error) {
	return map[string]interface{}{"error": "device changed"}, nil
}
func (h *storageAPIStub) ApproveStorageIdentityFromMap(map[string]interface{}) error {
	h.approved = true
	return nil
}
func TestStorageIdentityAPIRequiresAuthAndHonorsLock(t *testing.T) {
	h := &storageAPIStub{stubHandler: &stubHandler{}}
	s := NewServer(h)
	s.SetLockedFunc(func() bool { return true })
	ts := serveTest(s)
	t.Cleanup(ts.Close)
	bare := serveTestWithoutCaller(s)
	t.Cleanup(bare.Close)
	for _, c := range []struct {
		name   string
		srv    *httptest.Server
		method string
		want   int
	}{
		{"no caller", bare, "GET", 401},
		{"caller", ts, "GET", 200},
		{"caller, locked write", ts, "POST", 403},
	} {
		code, _ := do(t, c.srv, c.method, "/storage/identity", "{}")
		if code != c.want {
			t.Fatalf("%s: %s got %d want %d", c.name, c.method, code, c.want)
		}
	}
	if h.approved {
		t.Fatal("locked approval reached configuration writer")
	}
}
