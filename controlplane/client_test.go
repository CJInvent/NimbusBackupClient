package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// fake NimbusControl covering enroll, checkin, runs, results, and 429 retry.
func fakeServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	var runPosts atomic.Int64
	mux := http.NewServeMux()

	mux.HandleFunc("/api/agent/v1/enroll", func(w http.ResponseWriter, r *http.Request) {
		var req EnrollRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Token != "good-token" {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":"Enrollment token is not valid"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(EnrollResponse{AgentID: 7, Secret: "s3cret", CheckinSeconds: 120})
	})

	mux.HandleFunc("/api/agent/v1/checkin", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer 7.s3cret" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"Invalid agent credentials"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(CheckinResponse{
			Commands:       []Command{{ID: 1, Command: "run_backup", Payload: map[string]interface{}{"job": "J1"}}},
			CheckinSeconds: 300,
			Policy:         Policy{FileRestore: true},
		})
	})

	mux.HandleFunc("/api/agent/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		// First attempt gets throttled to exercise the backoff path.
		if runPosts.Add(1) == 1 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"Too many requests"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	})

	mux.HandleFunc("/api/agent/v1/commands/1/result", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	})

	return httptest.NewServer(mux), &runPosts
}

func TestEnrollCheckinAndPolicy(t *testing.T) {
	srv, _ := fakeServer(t)
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	if _, err := c.Enroll(EnrollRequest{Token: "bad", Hostname: "h"}); err == nil {
		t.Fatal("bad token must fail")
	}
	resp, err := c.Enroll(EnrollRequest{Token: "good-token", Hostname: "h"})
	if err != nil || resp.AgentID != 7 {
		t.Fatalf("enroll: %v %+v", err, resp)
	}
	c.AgentID, c.Secret = resp.AgentID, resp.Secret

	var gotPolicy Policy
	var handled atomic.Int64
	a := &Agent{
		Client:        c,
		OnPolicy:      func(p Policy) { gotPolicy = p },
		HandleCommand: func(cmd Command) CommandResult { handled.Add(1); return CommandResult{OK: true} },
	}
	if a.CurrentPolicy().FileRestore {
		t.Fatal("policy must fail CLOSED before first check-in")
	}
	a.CheckinNow()
	if !gotPolicy.FileRestore || !a.CurrentPolicy().FileRestore {
		t.Fatal("policy not applied from check-in")
	}
	if handled.Load() != 1 {
		t.Fatal("command not dispatched")
	}
	if a.interval.Load() != 300 {
		t.Fatalf("interval not adopted: %d", a.interval.Load())
	}
}

func TestReportRunRetriesOn429(t *testing.T) {
	srv, runPosts := fakeServer(t)
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, AgentID: 7, Secret: "s3cret"}
	if err := c.ReportRun(RunReport{RunUUID: NewRunUUID(), JobName: "J", BackupType: "directory",
		Status: StatusPreparing, StartedAt: "2026-07-07T00:00:00Z"}); err != nil {
		t.Fatalf("report should succeed after retry: %v", err)
	}
	if runPosts.Load() < 2 {
		t.Fatal("429 was not retried")
	}
}

func TestRunReporterForwardOnly(t *testing.T) {
	r := (&Client{BaseURL: "http://127.0.0.1:1"}).NewRun("J", "directory")
	r.terminal = true
	r.Running() // must be a no-op after terminal
	if !r.terminal {
		t.Fatal("terminal latch broken")
	}
}

func TestUUIDShape(t *testing.T) {
	u := NewRunUUID()
	if len(u) != 36 || u[14] != '4' {
		t.Fatalf("bad uuid: %s", u)
	}
}

// TestCertPinningVerifyConnection exercises the pinned-fingerprint path end
// to end: a correct pin connects, a wrong pin is rejected. Guards against
// regressions in the VerifyConnection wiring (G123 fix).
func TestCertPinningVerifyConnection(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(EnrollResponse{AgentID: 1, Secret: "x", CheckinSeconds: 120})
	}))
	defer srv.Close()

	// Real fingerprint of the test server's leaf certificate.
	leaf := srv.Certificate()
	sum := sha256.Sum256(leaf.Raw)
	goodFP := hex.EncodeToString(sum[:])

	// Correct pin: handshake + call succeed.
	ok := &Client{BaseURL: srv.URL, CertFingerprint: goodFP}
	if _, err := ok.Enroll(EnrollRequest{Token: "good-token", Hostname: "h"}); err != nil {
		t.Fatalf("correct pin should connect: %v", err)
	}

	// Wrong pin: every attempt fails the handshake (not a 4xx — a TLS error,
	// so the retry ladder runs and still ends in failure).
	bad := &Client{BaseURL: srv.URL, CertFingerprint: "00" + goodFP[2:]}
	if _, err := bad.Enroll(EnrollRequest{Token: "good-token", Hostname: "h"}); err == nil {
		t.Fatal("wrong pin must be rejected")
	}
}
