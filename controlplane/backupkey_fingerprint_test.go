package controlplane

// The wire contract for asking the server which key opens a snapshot.
//
// The property under test is the one the restore path's behaviour hinges on:
// "no key of ours matches that fingerprint" and "the server could not be
// reached" must not arrive as the same thing. The first is ordinary — the
// snapshot may belong to another tenant, or predate this server — and the
// caller moves on to the next key source. The second means the operator is one
// outage away from a restore, and a search that treated it as "not found"
// would end with "no source could supply the key" while the key sat in the
// vault the whole time.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fingerprintServer(t *testing.T, status int, body any, capture *BackupKeyByFingerprintRequest) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agent/v1/backup-key/for-fingerprint", func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			_ = json.NewDecoder(r.Body).Decode(capture)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, AgentID: 1, Secret: "s"}
}

const testFingerprint = "39:8d:47:8a:4e:9d:88:1c:6e:79:43:86:c7:7e:e6:36:" +
	"c7:d0:c7:4a:bd:c5:d3:64:62:6c:78:7a:8f:25:f6:f9"

// A retired key comes back, and says so. This is the case the endpoint exists
// for: a snapshot older than a rotation.
func TestARetiredKeyIsReturnedForARestore(t *testing.T) {
	var sent BackupKeyByFingerprintRequest
	c := fingerprintServer(t, http.StatusOK, map[string]any{
		"key_sealed":       "AAAA",
		"sealed_to_key_id": "2026-09-07-deadbeef",
		"key_id":           "abc123",
		"version":          1,
		"scope":            "org",
		"status":           "retired",
		"escrow_blob":      "BBBB",
	}, &sent)

	got, err := c.FetchBackupKeyByFingerprint(testFingerprint)
	if err != nil {
		t.Fatalf("FetchBackupKeyByFingerprint: %v", err)
	}
	if sent.Fingerprint != testFingerprint {
		t.Errorf("the agent asked for %q, want %q", sent.Fingerprint, testFingerprint)
	}
	if got.Status != "retired" {
		t.Errorf("status = %q, want retired — the caller logs this to tell a rotation from a fault", got.Status)
	}
	if got.KeyID != "abc123" {
		t.Errorf("key_id = %q, want abc123", got.KeyID)
	}
	// The embedded shape must decode too: the restore path hands
	// BackupKeyMaterial to the same verifier the backup path uses, and a
	// wrapper that only populated Status would fail there instead of here.
	if got.KeySealedB64 != "AAAA" || got.EscrowBlobB64 != "BBBB" {
		t.Errorf("embedded material did not decode: %+v", got.BackupKeyMaterial)
	}
	// And which of our keys it was sealed to: during a rotation this machine
	// holds two, and opening with the wrong one reports a damaged payload for
	// a perfectly good one.
	if got.SealedToKeyID != "2026-09-07-deadbeef" {
		t.Errorf("sealed_to_key_id = %q, want the key the server sealed to", got.SealedToKeyID)
	}
}

// THE ONE THAT MATTERS. A 404 is "not ours", and it must be distinguishable
// from every other failure by the caller — hence a sentinel rather than a
// string to match on.
func TestAnUnknownFingerprintIsItsOwnAnswer(t *testing.T) {
	c := fingerprintServer(t, http.StatusNotFound,
		map[string]any{"error": "No key held for this org matches that fingerprint"}, nil)

	_, err := c.FetchBackupKeyByFingerprint(testFingerprint)
	if !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("a 404 gave %v, want ErrNoSuchKey — the restore must continue to the next key source", err)
	}
}

// ...and everything else is NOT that answer. A server refusing the token, or
// rejecting the request, must surface as a real error — or a failure to REACH
// the key reads as the key not existing.
//
// 4xx ONLY, and not because 5xx is uninteresting: post() retries 5xx on a
// 2s/8s/30s ladder, so covering one here would add forty seconds to the suite
// for a path this function does not treat specially. The distinction under
// test is 404-versus-everything-else, and these three establish it.
func TestAnUnreachableServerIsNotAMissingKey(t *testing.T) {
	for _, status := range []int{401, 403, 422} {
		c := fingerprintServer(t, status, map[string]any{"error": "nope"}, nil)
		_, err := c.FetchBackupKeyByFingerprint(testFingerprint)
		if err == nil {
			t.Errorf("HTTP %d returned no error at all", status)
			continue
		}
		if errors.Is(err, ErrNoSuchKey) {
			t.Errorf("HTTP %d was reported as ErrNoSuchKey; a failure to REACH the server "+
				"must not read as the server not having the key", status)
		}
	}
}

// An empty fingerprint never reaches the wire. A restore of an unencrypted
// snapshot resolves before any source is consulted, so a request with no
// fingerprint means a caller lost track of what it was asking.
func TestAnEmptyFingerprintIsRefusedLocally(t *testing.T) {
	reached := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agent/v1/backup-key/for-fingerprint", func(w http.ResponseWriter, r *http.Request) {
		reached = true
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, AgentID: 1, Secret: "s"}

	if _, err := c.FetchBackupKeyByFingerprint(""); err == nil {
		t.Fatal("an empty fingerprint was accepted")
	}
	if reached {
		t.Error("an empty fingerprint was sent to the server")
	}
}
