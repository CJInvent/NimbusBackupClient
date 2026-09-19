package api

import (
	"net/http/httptest"
	"strings"
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
	s := NewServer("", h, "test-token")
	s.SetLockedFunc(func() bool { return true })
	handler := s.authMiddleware(s.readOnlyMiddleware(s.mux))
	for _, c := range []struct {
		method, token string
		want          int
	}{{"GET", "", 401}, {"GET", "test-token", 200}, {"POST", "test-token", 403}} {
		req := httptest.NewRequest(c.method, "/storage/identity", strings.NewReader("{}"))
		if c.token != "" {
			req.Header.Set("X-Nimbus-Token", c.token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != c.want {
			t.Fatalf("%s got %d want %d", c.method, w.Code, c.want)
		}
	}
	if h.approved {
		t.Fatal("locked approval reached configuration writer")
	}
}
