package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"controlplane"
	"pbscommon"
)

// applyPBSTargetFields writes only on a real change, and the reason is not
// tidiness: a check-in lands roughly every two minutes, so a function that
// reported "changed" every time would rewrite the config file about 720 times
// a day to store identical bytes, and bury every genuine provisioning event in
// a log nobody can read.
func TestTheConfigIsRewrittenOnlyWhenSomethingActuallyChanged(t *testing.T) {
	target := controlplane.PBSTarget{
		AuthID: "nimbus-clients@pbs!acme--fd-01-17", BaseURL: "https://pbs.example:8007",
		Datastore: "DS1", Namespace: "NimbusManaged/acme/fd-01-17", Fingerprint: "AB:CD",
	}

	cfg := &Config{}
	if !applyPBSTargetFields(cfg, &target) {
		t.Fatal("the first target ever seen reported no change")
	}
	if cfg.BaseURL != target.BaseURL || cfg.AuthID != target.AuthID ||
		cfg.Datastore != target.Datastore || cfg.Namespace != target.Namespace ||
		cfg.CertFingerprint != target.Fingerprint {
		t.Fatalf("the target was not applied in full: %+v", cfg)
	}
	if applyPBSTargetFields(cfg, &target) {
		t.Fatal("re-applying an identical target reported a change")
	}

	// Every field independently, because a comparison that omits one is a
	// machine that silently keeps backing up to the old one.
	for name, mutate := range map[string]func(*controlplane.PBSTarget){
		"base URL":    func(p *controlplane.PBSTarget) { p.BaseURL = "https://other:8007" },
		"auth id":     func(p *controlplane.PBSTarget) { p.AuthID = "nimbus-clients@pbs!acme--fd-01-18" },
		"datastore":   func(p *controlplane.PBSTarget) { p.Datastore = "DS2" },
		"namespace":   func(p *controlplane.PBSTarget) { p.Namespace = "NimbusManaged/acme/other" },
		"fingerprint": func(p *controlplane.PBSTarget) { p.Fingerprint = "EF:01" },
	} {
		fresh := &Config{}
		applyPBSTargetFields(fresh, &target)
		moved := target
		mutate(&moved)
		if !applyPBSTargetFields(fresh, &moved) {
			t.Fatalf("a changed %s was not noticed", name)
		}
	}
}

// The secret survives a target change on purpose. Blanking it first would
// leave a window in which this machine can reach PBS and cannot authenticate
// to it; a wrong-but-present secret fails the same way an absent one does, and
// fails without a gap.
func TestApplyingATargetDoesNotBlankTheSecretItIsAboutToReplace(t *testing.T) {
	cfg := &Config{
		AuthID: "nimbus-clients@pbs!acme--fd-01-17", BaseURL: "https://pbs.example:8007",
		Datastore: "DS1", Secret: "the-old-token",
	}
	applyPBSTargetFields(cfg, &controlplane.PBSTarget{
		AuthID: "nimbus-clients@pbs!acme--fd-01-18", BaseURL: "https://pbs.example:8007",
		Datastore: "DS1",
	})
	if cfg.Secret != "the-old-token" {
		t.Fatalf("the secret was cleared, leaving an authentication gap: %q", cfg.Secret)
	}
}

// A sealed credential that opens to nothing must be an error, never an empty
// string. A blank token written into the config is indistinguishable from
// "never provisioned" on the next cycle, so the machine would refetch forever
// against a 3/hour limit while looking, in the config, entirely healthy.
func TestAnUnopenableCredentialIsAnErrorAndNotAnEmptySecret(t *testing.T) {
	for name, cred := range map[string]*controlplane.PBSCredential{
		"nothing delivered":  nil,
		"no sealed material": {},
		"not base64":         {SecretSealedB64: "!!! not base64 !!!"},
	} {
		got, err := openSealedPBSSecret(cred)
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		if got != "" {
			t.Fatalf("%s: returned a secret alongside its error: %q", name, got)
		}
	}
}

// ===================================================================== F-38
//
// V4-BETA-FIXES §1.1. Every case goes through the REAL
// applyPBSTargetFromCheckin against a stand-in control server that seals the
// secret exactly as the real one does (sealToThisMachine), so the held test,
// the cooldown, the key registration and the store are all in the loop.

type pbsCredServer struct {
	mu      sync.Mutex
	fetches int
	authID  string
	gen     int64
	secret  string
	leaves  []string // bearer presented on each POST /leave
	leaveOK bool
	srv     *httptest.Server
}

func newPBSCredServer(t *testing.T) *pbsCredServer {
	t.Helper()
	s := &pbsCredServer{leaveOK: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agent/v1/keys/register", func(w http.ResponseWriter, r *http.Request) {
		var in controlplane.KeyRegisterRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(controlplane.KeyRegisterResponse{KeyID: in.KeyID, State: controlplane.KeyStateActive})
	})
	mux.HandleFunc("/api/agent/v1/pbs-credential", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.fetches++
		authID, gen, secret := s.authID, s.gen, s.secret
		s.mu.Unlock()
		sealed, keyID := sealToThisMachine(t, []byte(secret))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"auth_id": authID, "credential_gen": gen, "base_url": "https://pbs.example:8007",
			"datastore": "DS1", "namespace": "nimbus/org/m", "fingerprint": nil,
			"secret_sealed": sealed, "key_id": keyID,
		})
	})
	mux.HandleFunc("/api/agent/v1/leave", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.leaves = append(s.leaves, r.Header.Get("Authorization"))
		ok := s.leaveOK
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"down"}`))
			return
		}
		_, _ = w.Write([]byte(`{"left":true}`))
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *pbsCredServer) serve(authID string, gen int64, secret string) {
	s.mu.Lock()
	s.authID, s.gen, s.secret = authID, gen, secret
	s.mu.Unlock()
}

func (s *pbsCredServer) fetchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fetches
}

// resetPBSCredentialState clears the package-level memory the credential code
// keeps between check-ins, and points the control-plane client at srv.
func resetPBSCredentialState(t *testing.T, srv *pbsCredServer, agentID int64) {
	t.Helper()
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	forgetAgentKeyRegistration()
	t.Cleanup(forgetAgentKeyRegistration)
	pbsTargetMu.Lock()
	pbsTargetLastSaid, pbsFetchFor, pbsFetchAt, pbsLastTarget, pbsRefusalSaid =
		"", PBSCredentialTuple{}, time.Time{}, nil, PBSCredentialTuple{}
	pbsTargetMu.Unlock()
	cpMu.Lock()
	prev := cpClient
	cpClient = &controlplane.Client{BaseURL: srv.srv.URL, AgentID: agentID, Secret: strings.Repeat("a", 40)}
	cpMu.Unlock()
	t.Cleanup(func() { cpMu.Lock(); cpClient = prev; cpMu.Unlock() })
}

func pbsTarget(authID string, gen int64) *controlplane.PBSTarget {
	return &controlplane.PBSTarget{AuthID: authID, CredentialGen: gen, BaseURL: "https://pbs.example:8007",
		Datastore: "DS1", Namespace: "nimbus/org/m"}
}

// (1) F-38a: retire then reactivate re-mints under the SAME auth-id. Only the
// generation says the secret changed, and exactly one fetch follows.
func TestSameAuthIDHigherGenerationFetchesExactlyOnce(t *testing.T) {
	srv := newPBSCredServer(t)
	resetPBSCredentialState(t, srv, 11)
	const aid = "nimbus_test@pbs!nc3fa9c1-2-11"
	a := &App{config: &Config{ControlAgentID: 11, AuthID: aid, Secret: "revoked-secret",
		BaseURL: "https://pbs.example:8007", Datastore: "DS1", Namespace: "nimbus/org/m",
		PBSCredentialFor: &PBSCredentialTuple{AgentID: 11, AuthID: aid, Gen: 1}}}
	srv.serve(aid, 2, "fresh-secret")

	a.applyPBSTargetFromCheckin(pbsTarget(aid, 2))
	a.applyPBSTargetFromCheckin(pbsTarget(aid, 2))

	if n := srv.fetchCount(); n != 1 {
		t.Fatalf("fetched %d times, want exactly 1", n)
	}
	if a.config.Secret != "fresh-secret" {
		t.Fatalf("the re-minted secret was not stored: %q", a.config.Secret)
	}
	if got := *a.config.PBSCredentialFor; got != (PBSCredentialTuple{AgentID: 11, AuthID: aid, Gen: 2}) {
		t.Fatalf("stored tuple %+v", got)
	}
}

// (2) F-38b, THE ONE THAT FAILED ON dev.189: re-enrollment (agent 1 -> 11)
// brings a NEW auth-id while an old secret is still saved. The old code copied
// the new auth-id into the config BEFORE its held test, so the test always
// passed and the dead secret was kept ("the credential this machine holds
// matches"). Written against fields dev.189 already had, so it runs -- and
// fails -- on that code too.
func TestANewAuthIDWithASavedSecretFetches(t *testing.T) {
	srv := newPBSCredServer(t)
	resetPBSCredentialState(t, srv, 11)
	a := &App{config: &Config{ControlAgentID: 11, AuthID: "nimbus_test@pbs!c5eu6kz8isyiavorr--desktop-nd3eqi2-1",
		Secret: "agent-1-secret", BaseURL: "https://pbs.example:8007", Datastore: "DS1"}}
	srv.serve("nimbus_test@pbs!c5eu6kz8isyiavorr--desktop-nd3eqi2-11", 1, "agent-11-secret")

	a.applyPBSTargetFromCheckin(&controlplane.PBSTarget{AuthID: "nimbus_test@pbs!c5eu6kz8isyiavorr--desktop-nd3eqi2-11",
		BaseURL: "https://pbs.example:8007", Datastore: "DS1"})

	if n := srv.fetchCount(); n != 1 {
		t.Fatalf("fetched %d times after the auth-id changed, want exactly 1", n)
	}
	if a.config.Secret != "agent-11-secret" {
		t.Fatalf("still holding the previous identity's secret: %q", a.config.Secret)
	}
}

// (3) A new agent id with the same auth-id and generation still fetches: the
// agent is part of the tuple, so re-enrollment always fetches even when a new
// agent's generation happens to equal the old one's.
func TestANewAgentIDFetchesEvenWhenAuthIDAndGenerationMatch(t *testing.T) {
	srv := newPBSCredServer(t)
	resetPBSCredentialState(t, srv, 12)
	const aid = "nimbus@pbs!nc3fa9c1-2-12"
	a := &App{config: &Config{ControlAgentID: 12, AuthID: aid, Secret: "s", BaseURL: "https://pbs.example:8007",
		Datastore: "DS1", Namespace: "nimbus/org/m",
		PBSCredentialFor: &PBSCredentialTuple{AgentID: 11, AuthID: aid, Gen: 1}}}
	srv.serve(aid, 1, "s2")
	a.applyPBSTargetFromCheckin(pbsTarget(aid, 1))
	if n := srv.fetchCount(); n != 1 {
		t.Fatalf("fetched %d times for a new agent id, want 1", n)
	}
}

// (4) The steady state: the tuple matches and nothing is fetched, however many
// check-ins arrive.
func TestAMatchingTupleFetchesNothing(t *testing.T) {
	srv := newPBSCredServer(t)
	resetPBSCredentialState(t, srv, 11)
	const aid = "nimbus@pbs!nc3fa9c1-2-11"
	a := &App{config: &Config{ControlAgentID: 11, AuthID: aid, Secret: "s", BaseURL: "https://pbs.example:8007",
		Datastore: "DS1", Namespace: "nimbus/org/m",
		PBSCredentialFor: &PBSCredentialTuple{AgentID: 11, AuthID: aid, Gen: 4}}}
	for i := 0; i < 5; i++ {
		a.applyPBSTargetFromCheckin(pbsTarget(aid, 4))
	}
	if n := srv.fetchCount(); n != 0 {
		t.Fatalf("fetched %d times while holding the named credential", n)
	}
}

// (5) What is stored is the tuple that ARRIVED WITH THE SECRET, not the one the
// check-in named: a mint racing the fetch must not pair a new secret with an
// old generation.
func TestTheStoredTupleIsTheFetchResponses(t *testing.T) {
	srv := newPBSCredServer(t)
	resetPBSCredentialState(t, srv, 11)
	const aid = "nimbus@pbs!nc3fa9c1-2-11"
	a := &App{config: &Config{ControlAgentID: 11}}
	srv.serve(aid, 3, "newest")
	a.applyPBSTargetFromCheckin(pbsTarget(aid, 2))
	if a.config.PBSCredentialFor == nil || *a.config.PBSCredentialFor != (PBSCredentialTuple{AgentID: 11, AuthID: aid, Gen: 3}) {
		t.Fatalf("stored %+v, want the response's generation 3", a.config.PBSCredentialFor)
	}
	// And it is then held: generation 3 on the next check-in fetches nothing.
	a.applyPBSTargetFromCheckin(pbsTarget(aid, 3))
	if n := srv.fetchCount(); n != 1 {
		t.Fatalf("fetched %d times, want 1", n)
	}
}

func readRefusalCapture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "pbs-refusal", name))
	if err != nil {
		t.Fatalf("capture %s: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}

// (6) A refused credential is its OWN failure class, classified from the real
// lines the VM logged (dev rule 25) -- not session-lost (the 25-minute wait
// T5.4 hit), and not a network failure or a 403.
func TestPBSRefusalIsClassifiedFromTheCapturedLines(t *testing.T) {
	dir401 := strings.TrimPrefix(readRefusalCapture(t, "t54-directory-previous-401.txt"), "No previous backup found (first backup?): ")
	for name, line := range map[string]string{
		"directory engine, T5.4":    dir401,
		"image engine, re-enrolled": readRefusalCapture(t, "reenroll-image-401.txt"),
	} {
		if !isPBSCredentialRefused(errors.New(line)) {
			t.Errorf("%s: a captured 401 was not classified as a refused credential", name)
		}
		if isFatalSessionError(errors.New(line)) {
			t.Errorf("%s: a refused credential must not be 'session lost' (the 25-minute wait)", name)
		}
	}
	for name, line := range map[string]string{
		"session lost": readRefusalCapture(t, "t54-directory-session-lost.txt"),
		"offline":      readRefusalCapture(t, "offline-directory-previous.txt"),
	} {
		if isPBSCredentialRefused(errors.New(line)) {
			t.Errorf("%s was classified as a refused credential", name)
		}
	}
	// Typed, through wrapping, as the engines produce it.
	wrapped := fmt.Errorf("PBS rejected the backup session: %w",
		&url.Error{Op: "Get", URL: "https://pbs/previous", Err: &pbscommon.PBSResponseError{StatusCode: "401 Unauthorized"}})
	if !isPBSCredentialRefused(wrapped) {
		t.Error("a wrapped 401 PBSResponseError was not classified as refused")
	}
	if isPBSCredentialRefused(&pbscommon.PBSResponseError{StatusCode: "403 Forbidden"}) {
		t.Error("a 403 (valid token, no permission) is not a refused CREDENTIAL; a new secret does not fix it")
	}
}

// (7) A refusal records itself, WARNs once per refused credential, and asks
// for the credential the server names through the normal cooldown.
func TestARefusalRecordsItselfWarnsOnceAndFetchesOnce(t *testing.T) {
	srv := newPBSCredServer(t)
	resetPBSCredentialState(t, srv, 11)
	read := captureServiceLog(t)
	const aid = "nimbus@pbs!nc3fa9c1-2-11"
	a := &App{config: &Config{ControlAgentID: 11, AuthID: aid, Secret: "dead", BaseURL: "https://pbs.example:8007",
		Datastore: "DS1", Namespace: "nimbus/org/m",
		PBSCredentialFor: &PBSCredentialTuple{AgentID: 11, AuthID: aid, Gen: 2}}}
	a.applyPBSTargetFromCheckin(pbsTarget(aid, 2)) // held: nothing fetched, target remembered
	srv.serve(aid, 2, "dead")                      // the server has nothing newer (secret regenerated on PBS)

	fetch := withAgentKeyFetcher(cpClient)
	renewed1 := a.notePBSCredentialRefused(fetch)
	renewed2 := a.notePBSCredentialRefused(fetch)

	if renewed1 || renewed2 {
		t.Fatal("the same credential came back; nothing was renewed")
	}
	if n := srv.fetchCount(); n != 1 {
		t.Fatalf("two refusals fetched %d times, want 1 (the cooldown)", n)
	}
	if a.config.PBSRefusedAt == 0 {
		t.Fatal("the refusal was not recorded")
	}
	rep := a.heldPBSCredentialReport()
	if rep == nil || rep.AuthID != aid || rep.Gen != 2 || rep.RefusedAt == nil || *rep.RefusedAt != a.config.PBSRefusedAt {
		t.Fatalf("the check-in report does not carry the refusal: %+v", rep)
	}
	if n := strings.Count(read(), pbsCredentialRefusedMsg); n != 1 {
		t.Fatalf("the refusal WARN was written %d times, want once per refused credential", n)
	}
	if err := pbsRefusalRunError(errors.New("x"), false); !strings.HasPrefix(err.Error(), pbsCredentialRefusedMsg) {
		t.Fatalf("the run error does not lead with the operator sentence: %v", err)
	}
}

// ...and a refusal the server CAN answer (a newer generation) stores the new
// credential and clears the refusal: it was about the one it replaced.
func TestARefusalWithANewerCredentialRenewsAndClears(t *testing.T) {
	srv := newPBSCredServer(t)
	resetPBSCredentialState(t, srv, 11)
	const aid = "nimbus@pbs!nc3fa9c1-2-11"
	a := &App{config: &Config{ControlAgentID: 11, AuthID: aid, Secret: "dead", BaseURL: "https://pbs.example:8007",
		Datastore: "DS1", Namespace: "nimbus/org/m",
		PBSCredentialFor: &PBSCredentialTuple{AgentID: 11, AuthID: aid, Gen: 2}}}
	pbsTargetMu.Lock()
	pbsLastTarget = pbsTarget(aid, 3)
	pbsTargetMu.Unlock()
	srv.serve(aid, 3, "alive")
	if !a.notePBSCredentialRefused(withAgentKeyFetcher(cpClient)) {
		t.Fatal("a newer credential was fetched but not reported as renewed")
	}
	if a.config.Secret != "alive" || a.config.PBSRefusedAt != 0 {
		t.Fatalf("secret %q refused_at %d", a.config.Secret, a.config.PBSRefusedAt)
	}
}

// (8) Nothing held, nothing reported: the field is omitted.
func TestNoCredentialNoReport(t *testing.T) {
	if (&App{config: &Config{}}).heldPBSCredentialReport() != nil {
		t.Fatal("reported a credential this machine does not hold")
	}
}

// ===================================================================== F-45
//
// Leaving the control server tells it first, while the identity to say it
// with still exists (V4-BETA-FIXES §1.3 part 2).
func TestLeavingCallsLeaveBeforeClearingTheIdentity(t *testing.T) {
	srv := newPBSCredServer(t)
	resetPBSCredentialState(t, srv, 21)
	secret := strings.Repeat("b", 43)
	a := &App{config: &Config{ControlServerURL: srv.srv.URL, ControlAgentID: 21, ControlSecret: encryptSecret(secret)}}
	if err := a.SaveControlPlaneFromMap(map[string]interface{}{"control_server_url": ""}); err != nil {
		t.Fatalf("save: %v", err)
	}
	srv.mu.Lock()
	leaves := append([]string(nil), srv.leaves...)
	srv.mu.Unlock()
	if len(leaves) != 1 || leaves[0] != "Bearer 21."+secret {
		t.Fatalf("POST /leave with the old identity: got %q", leaves)
	}
	if a.config.ControlAgentID != 0 || a.config.ControlSecret != "" || a.config.ControlServerURL != "" {
		t.Fatalf("the identity was not cleared: %+v", a.config)
	}
}

// A server that cannot be told still lets the machine leave, with a WARN.
func TestLeavingStillLeavesWhenTheServerCannotBeTold(t *testing.T) {
	srv := newPBSCredServer(t)
	resetPBSCredentialState(t, srv, 22)
	read := captureServiceLog(t)
	srv.mu.Lock()
	srv.leaveOK = false
	srv.mu.Unlock()
	a := &App{config: &Config{ControlServerURL: srv.srv.URL, ControlAgentID: 22, ControlSecret: encryptSecret(strings.Repeat("c", 43))}}
	if err := a.SaveControlPlaneFromMap(map[string]interface{}{"control_server_url": ""}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if a.config.ControlAgentID != 0 {
		t.Fatal("a failed /leave kept the machine enrolled")
	}
	if !strings.Contains(read(), "[WARN]") || !strings.Contains(read(), "could not tell the control server that this machine (agent 22) is leaving") {
		t.Fatalf("no WARN for the failed leave:\n%s", read())
	}
}
