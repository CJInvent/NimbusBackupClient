package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"controlplane"
)

// Registration, and what happens when the server disagrees about it.
//
// The failure this guards against is silent and total: an agent that never
// registers a key authenticates, checks in, takes commands -- and every secret
// endpoint answers 409 forever. There is no symptom at the point of failure,
// only backups that never start being encrypted.

type keyServer struct {
	mu           sync.Mutex
	registers    int
	fetches      int
	failFetchFor int // answer 409 for this many fetches first
	srv          *httptest.Server
}

func newKeyServer(t *testing.T, failFetchFor int) *keyServer {
	t.Helper()
	ks := &keyServer{failFetchFor: failFetchFor}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agent/v1/keys/register", func(w http.ResponseWriter, r *http.Request) {
		var in controlplane.KeyRegisterRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		ks.mu.Lock()
		ks.registers++
		ks.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(controlplane.KeyRegisterResponse{
			KeyID: in.KeyID, State: controlplane.KeyStateActive,
		})
	})
	mux.HandleFunc("/api/agent/v1/backup-key", func(w http.ResponseWriter, r *http.Request) {
		ks.mu.Lock()
		ks.fetches++
		refuse := ks.fetches <= ks.failFetchFor
		ks.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if refuse {
			// The server's own words for this condition.
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "This machine has no registered public key, so no secret can be sealed to it.",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"key_id": "served"})
	})
	ks.srv = httptest.NewServer(mux)
	t.Cleanup(ks.srv.Close)
	return ks
}

func (ks *keyServer) client() *controlplane.Client {
	return &controlplane.Client{BaseURL: ks.srv.URL, AgentID: 1, Secret: strings.Repeat("a", 40)}
}

func (ks *keyServer) counts() (int, int) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	return ks.registers, ks.fetches
}

// The ordinary case: register once per process, however many secrets are
// fetched afterwards. At 720 check-ins a day per machine, "register every
// time" would be a request per machine per cycle for a fact that does not
// change.
func TestAgentKeyRegistersOncePerProcess(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	forgetAgentKeyRegistration()
	t.Cleanup(forgetAgentKeyRegistration)

	ks := newKeyServer(t, 0)
	c := ks.client()

	for i := 0; i < 3; i++ {
		if _, err := withAgentKey(c, func() (*controlplane.BackupKeyMaterial, error) {
			return c.FetchBackupKey(controlplane.BackupKeyRequest{Mode: controlplane.KeyModeDurable})
		}); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}

	registers, fetches := ks.counts()
	if registers != 1 {
		t.Fatalf("registered %d times for 3 fetches, want 1", registers)
	}
	if fetches != 3 {
		t.Fatalf("fetched %d times, want 3", fetches)
	}
}

// THE ONE THAT MATTERS. The server says it has no key for us even though this
// process believes it registered one -- a rebuilt control plane, a restored
// database, a machine re-adopted elsewhere. The client must re-register and
// try again rather than report a permanent failure, because the state is one
// only its own action can reach.
func TestAgentKeyReregistersWhenTheServerSaysItHasNone(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	forgetAgentKeyRegistration()
	t.Cleanup(forgetAgentKeyRegistration)

	ks := newKeyServer(t, 1) // the first fetch is refused
	c := ks.client()

	if _, err := withAgentKey(c, func() (*controlplane.BackupKeyMaterial, error) {
		return c.FetchBackupKey(controlplane.BackupKeyRequest{Mode: controlplane.KeyModeDurable})
	}); err != nil {
		t.Fatalf("the retry after re-registering should have succeeded: %v", err)
	}

	registers, fetches := ks.counts()
	if registers != 2 {
		t.Fatalf("registered %d times, want 2 (once up front, once after the refusal)", registers)
	}
	if fetches != 2 {
		t.Fatalf("fetched %d times, want 2 (the refusal and the retry)", fetches)
	}
}

// ONCE, not in a loop. A server that keeps refusing is a disagreement to
// surface, not a race to win by trying harder -- and a client that retried
// forever would hammer an endpoint whose whole design is a 3/hour rate limit.
func TestAgentKeyRetriesExactlyOnce(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	forgetAgentKeyRegistration()
	t.Cleanup(forgetAgentKeyRegistration)

	ks := newKeyServer(t, 99) // refuses everything
	c := ks.client()

	_, err := withAgentKey(c, func() (*controlplane.BackupKeyMaterial, error) {
		return c.FetchBackupKey(controlplane.BackupKeyRequest{Mode: controlplane.KeyModeDurable})
	})
	if err == nil {
		t.Fatal("a server that never accepts the key must surface an error")
	}
	if _, fetches := ks.counts(); fetches != 2 {
		t.Fatalf("fetched %d times, want exactly 2 — the attempt and one retry", fetches)
	}
}
