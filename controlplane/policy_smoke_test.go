package controlplane

// S4 promotion (ARCHITECTURE.md Part III, Phase 1): assertions for the two
// security properties the agent<->server channel is claimed to have, both of
// which were previously only claimed in prose.
//
// These pin down what "fail closed" actually means today, including the case
// where it does NOT hold — a test that documents a gap is worth more than a
// document that asserts it away.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// selfSignedCert mints a certificate distinct from httptest's built-in one.
// httptest.NewTLSServer reuses a single hardcoded certificate for every
// server it starts, so two httptest servers are indistinguishable to a pin —
// testing substitution with them proves nothing.
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "impostor.example"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:              []string{"localhost"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// Before any successful check-in the agent must grant nothing, whatever the
// caller does. This is the property that holds unconditionally.
func TestPolicyDeniesBeforeFirstCheckin(t *testing.T) {
	a := &Agent{}
	if p := a.CurrentPolicy(); p.FileRestore {
		t.Errorf("file_restore granted before any check-in: %+v", p)
	}
	if a.PolicyIsStale() {
		t.Error("policy cannot be stale before one was ever delivered")
	}
}

// A check-in that fails must not widen the policy, and must not clear a grant
// that is still within its confirmation window.
func TestPolicySurvivesTransientFailureButNotExpiry(t *testing.T) {
	var grant bool
	var fail bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			// 400 rather than 5xx: a rejected check-in is just as much a
			// failed check-in for this test's purpose, and it does not walk
			// the 40s retry ladder. Ladder behavior is covered by
			// TestReportRunRetriesOn429.
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(CheckinResponse{
			CheckinSeconds: 120,
			Policy:         Policy{FileRestore: grant},
		})
	}))
	defer srv.Close()

	a := &Agent{
		Client:       &Client{BaseURL: srv.URL, AgentID: 1, Secret: "s"},
		PolicyMaxAge: time.Hour, // generous: this test is about failure, not expiry
	}

	grant = true
	a.CheckinNow()
	if !a.CurrentPolicy().FileRestore {
		t.Fatal("a granted capability did not take effect")
	}

	// Server starts failing: the grant stays in force for its window, and the
	// agent must not invent a wider one.
	fail = true
	a.CheckinNow()
	if !a.CurrentPolicy().FileRestore {
		t.Error("a failed check-in revoked a still-valid grant")
	}
	if a.PolicyIsStale() {
		t.Error("policy reported stale while still inside PolicyMaxAge")
	}

	// A server that comes back and revokes must be obeyed immediately.
	fail, grant = false, false
	a.CheckinNow()
	if a.CurrentPolicy().FileRestore {
		t.Error("revocation delivered by the server was not applied")
	}
}

// The gap this knob exists for: with PolicyMaxAge unset, a grant outlives any
// outage forever, so blocking the agent's egress freezes its capabilities.
// With it set, the grant expires down to the safe defaults.
func TestPolicyMaxAgeExpiresUnconfirmedGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(CheckinResponse{
			CheckinSeconds: 120,
			Policy:         Policy{FileRestore: true},
		})
	}))

	// Default behavior: the grant persists after the server disappears.
	keep := &Agent{Client: &Client{BaseURL: srv.URL, AgentID: 1, Secret: "s"}}
	keep.CheckinNow()
	if !keep.CurrentPolicy().FileRestore {
		t.Fatal("grant did not take effect")
	}

	// Bounded behavior: the same grant expires once unconfirmed.
	expire := &Agent{
		Client:       &Client{BaseURL: srv.URL, AgentID: 1, Secret: "s"},
		PolicyMaxAge: 20 * time.Millisecond,
	}
	expire.CheckinNow()
	if !expire.CurrentPolicy().FileRestore {
		t.Fatal("grant did not take effect under PolicyMaxAge")
	}

	srv.Close() // the control server is now unreachable
	time.Sleep(60 * time.Millisecond)

	if keep.CurrentPolicy().FileRestore != true {
		t.Error("default behavior changed: unbounded grants should persist")
	}
	if expire.CurrentPolicy().FileRestore {
		t.Error("PolicyMaxAge did not expire an unconfirmed grant — an attacker " +
			"who blocks control-plane egress would keep the capability forever")
	}
	if !expire.PolicyIsStale() {
		t.Error("expired policy did not report itself stale, so the UI cannot explain why")
	}
}

// Pinning must hold on EVERY authenticated call, not just enrollment: check-in
// and run reporting carry the agent secret and the inventory.
func TestCertPinningAppliesToAllCalls(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(CheckinResponse{CheckinSeconds: 120})
	}))
	defer srv.Close()

	sum := sha256.Sum256(srv.Certificate().Raw)
	goodFP := hex.EncodeToString(sum[:])
	wrongFP := "00" + goodFP[2:]

	good := &Client{BaseURL: srv.URL, CertFingerprint: goodFP, AgentID: 1, Secret: "s"}
	if _, err := good.Checkin(CheckinRequest{AgentVersion: "t"}); err != nil {
		t.Errorf("correct pin should allow check-in: %v", err)
	}
	if err := good.ReportRun(RunReport{RunUUID: "u", Status: StatusRunning}); err != nil {
		t.Errorf("correct pin should allow run reporting: %v", err)
	}

	bad := &Client{BaseURL: srv.URL, CertFingerprint: wrongFP, AgentID: 1, Secret: "s"}
	if _, err := bad.Checkin(CheckinRequest{AgentVersion: "t"}); err == nil {
		t.Error("check-in accepted a certificate that does not match the pin")
	}
	if err := bad.ReportRun(RunReport{RunUUID: "u", Status: StatusRunning}); err == nil {
		t.Error("run reporting accepted a certificate that does not match the pin")
	}
}

// A pinned agent must refuse a DIFFERENT server presenting a valid certificate
// of its own — the substitution a MITM actually performs.
func TestCertPinningRefusesSubstitutedServer(t *testing.T) {
	real := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(CheckinResponse{CheckinSeconds: 120})
	}))
	defer real.Close()
	// A genuinely different certificate — the substitution a MITM performs.
	impostor := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(CheckinResponse{CheckinSeconds: 30, Policy: Policy{FileRestore: true}})
	}))
	impostor.TLS = &tls.Config{Certificates: []tls.Certificate{selfSignedCert(t)}}
	impostor.StartTLS()
	defer impostor.Close()

	sum := sha256.Sum256(real.Certificate().Raw)
	pinnedToReal := hex.EncodeToString(sum[:])

	if sha256.Sum256(impostor.Certificate().Raw) == sha256.Sum256(real.Certificate().Raw) {
		t.Fatal("test premise broken: both servers present the same certificate")
	}

	c := &Client{BaseURL: impostor.URL, CertFingerprint: pinnedToReal, AgentID: 1, Secret: "s"}
	start := time.Now()
	_, err := c.Checkin(CheckinRequest{AgentVersion: "t"})
	if err == nil {
		t.Fatal("agent talked to a substituted server presenting its own certificate")
	}
	if !errors.Is(err, ErrCertPinMismatch) {
		t.Errorf("pin failure not reported as ErrCertPinMismatch: %v", err)
	}
	// A permanent trust failure must fail fast, not walk the retry ladder:
	// under an active MITM that ladder means hammering the impostor every cycle.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("pin mismatch took %v — it was retried despite being permanent", elapsed)
	}
}
