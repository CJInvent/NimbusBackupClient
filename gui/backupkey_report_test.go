package main

import (
	"controlplane"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// keyStatusServer records every key-status report a test's agent sends.
type keyStatusServer struct {
	mu      sync.Mutex
	reports []controlplane.KeyStatusReport
	fail    bool
}

func (s *keyStatusServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reports)
}

func startKeyStatusServer(t *testing.T) *keyStatusServer {
	t.Helper()
	ks := &keyStatusServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v1/key-status" {
			w.WriteHeader(404)
			return
		}
		var rep controlplane.KeyStatusReport
		_ = json.NewDecoder(r.Body).Decode(&rep)
		ks.mu.Lock()
		fail := ks.fail
		if !fail {
			ks.reports = append(ks.reports, rep)
		}
		ks.mu.Unlock()
		if fail {
			w.WriteHeader(422) // non-retryable, so the test does not wait on backoff
			_, _ = w.Write([]byte(`{"error":"refused"}`))
			return
		}
		_, _ = w.Write([]byte(`{"matches":true}`))
	}))
	prev := cpClient
	cpMu.Lock()
	cpClient = &controlplane.Client{BaseURL: srv.URL, AgentID: 1, Secret: "test-only"}
	cpMu.Unlock()
	resetCheckinLogState()
	t.Cleanup(func() {
		cpMu.Lock()
		cpClient = prev
		cpMu.Unlock()
		resetCheckinLogState()
		srv.Close()
	})
	return ks
}

func waitReports(t *testing.T, ks *keyStatusServer, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for ks.count() < want {
		if time.Now().After(deadline) {
			t.Fatalf("expected %d key-status report(s), got %d", want, ks.count())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Give an unwanted extra report the chance to arrive before asserting none did.
	time.Sleep(50 * time.Millisecond)
}

// The regression for the stale 'mismatch': a move to NO key is reported (ok),
// on the first check-in and on every change, and never on a check-in that
// changed nothing.
func TestKeyStatusIsReportedOnAssignmentChangeOnly(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	ks := startKeyStatusServer(t)

	applyBackupKeyFromCheckin(nil)
	waitReports(t, ks, 1)
	if got := ks.reports[0]; got.Status != controlplane.KeyStorageOK {
		t.Fatalf("no key assigned must report ok (it is what clears a stale verdict), got %+v", got)
	}

	applyBackupKeyFromCheckin(nil)
	applyBackupKeyFromCheckin(nil)
	waitReports(t, ks, 1)
	if n := ks.count(); n != 1 {
		t.Fatalf("unchanged check-ins must not report again, got %d reports", n)
	}

	applyBackupKeyFromCheckin(&controlplane.BackupKeyAd{KeyID: "abc123"})
	waitReports(t, ks, 2)
	if got := ks.reports[1]; got.Status != controlplane.KeyStorageAbsent {
		t.Fatalf("a newly assigned key this machine does not hold must report absent, got %+v", got)
	}

	applyBackupKeyFromCheckin(nil)
	waitReports(t, ks, 3)
	if got := ks.reports[2]; got.Status != controlplane.KeyStorageOK {
		t.Fatalf("moving back to no key must report ok again, got %+v", got)
	}
}

// A report the server refused is not remembered as sent, so the next check-in
// tries again instead of leaving the verdict stale.
func TestKeyStatusReportIsRetriedAfterFailure(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	ks := startKeyStatusServer(t)

	ks.mu.Lock()
	ks.fail = true
	ks.mu.Unlock()
	applyBackupKeyFromCheckin(nil)
	time.Sleep(200 * time.Millisecond)

	ks.mu.Lock()
	ks.fail = false
	ks.mu.Unlock()
	applyBackupKeyFromCheckin(nil)
	waitReports(t, ks, 1)
}
