package pbscommon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCheckConnectivity covers the three real outcomes a caller needs to
// tell apart: PBS answers healthy, PBS answers but rejects the request
// (e.g. revoked credentials), and PBS is not reachable at all (network
// failure). The latter two both mean reachable=false with a nil error --
// CheckConnectivity's whole point is to report a bool, so a normal
// "did not answer" is that bool's false case, not a Go error to bubble up.
func TestCheckConnectivity(t *testing.T) {
	t.Run("200 OK is reachable, and hits the right lightweight endpoint", func(t *testing.T) {
		var gotPath, gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			w.WriteHeader(200)
		}))
		defer srv.Close()

		pbs := &PBSClient{BaseURL: srv.URL, AuthID: "nimbus@pbs!agent1", Secret: "s3cr3t"}
		reachable, err := pbs.CheckConnectivity()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reachable {
			t.Error("expected reachable=true for a 200 response")
		}
		if gotPath != "/api2/json/version" {
			t.Errorf("path = %q, want /api2/json/version -- the lightweight endpoint, not a datastore listing", gotPath)
		}
		if gotAuth != "PBSAPIToken=nimbus@pbs!agent1:s3cr3t" {
			t.Errorf("Authorization header = %q", gotAuth)
		}
	})

	t.Run("HTTP error response is unreachable, not a Go error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
		}))
		defer srv.Close()

		pbs := &PBSClient{BaseURL: srv.URL, AuthID: "a", Secret: "b"}
		reachable, err := pbs.CheckConnectivity()
		if err != nil {
			t.Fatalf("a rejected request should not be a Go error, got: %v", err)
		}
		if reachable {
			t.Error("expected reachable=false for a 401 response")
		}
	})

	t.Run("connection failure is unreachable, not a Go error", func(t *testing.T) {
		// Port 1 is a reserved low port nothing listens on -- connection
		// refused, exercising the actual network-failure path rather than
		// an HTTP-level rejection.
		pbs := &PBSClient{BaseURL: "http://127.0.0.1:1", AuthID: "a", Secret: "b"}
		reachable, err := pbs.CheckConnectivity()
		if err != nil {
			t.Fatalf("a network failure should not be a Go error, got: %v", err)
		}
		if reachable {
			t.Error("expected reachable=false when the server cannot be reached at all")
		}
	})
}
