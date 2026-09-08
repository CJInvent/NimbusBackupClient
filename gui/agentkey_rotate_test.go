package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"controlplane"

	"golang.org/x/crypto/nacl/box"
)

// The rotation ceremony, driven against a server that behaves like the real
// one: it assigns state on registration, seals the challenge to the IN-FLIGHT
// key with the same construction the server uses, spends a challenge on any
// answer, and refuses to promote what is not proven.
//
// The PHP/Go agreement on that construction is pinned separately
// (TestSealedBoxOverheadMatchesLibsodium, plus a live cross-check); what is
// under test here is the SEQUENCE and what the client does when it goes wrong.

type rotServer struct {
	mu           sync.Mutex
	active       string
	pending      string
	pubs         map[string][]byte
	proven       bool
	nonce        []byte
	challengeKey string // force the challenge to name a different key
	registers    int
	promotes     int
	srv          *httptest.Server
}

func newRotServer(t *testing.T) *rotServer {
	t.Helper()
	rs := &rotServer{pubs: map[string][]byte{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/agent/v1/keys/register", func(w http.ResponseWriter, r *http.Request) {
		var in controlplane.KeyRegisterRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		pub, _ := base64.StdEncoding.DecodeString(in.PublicKey)

		rs.mu.Lock()
		defer rs.mu.Unlock()
		rs.registers++
		w.Header().Set("Content-Type", "application/json")

		state := ""
		switch {
		case rs.active == in.KeyID:
			state = controlplane.KeyStateActive
		case rs.pending == in.KeyID:
			state = controlplane.KeyStatePending
		case rs.active == "":
			rs.active, state = in.KeyID, controlplane.KeyStateActive
		case rs.pending == "":
			rs.pending, state = in.KeyID, controlplane.KeyStatePending
		default:
			// One rotation at a time, as the partial unique index enforces.
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "A key rotation is already in flight"})
			return
		}
		rs.pubs[in.KeyID] = pub
		_ = json.NewEncoder(w).Encode(controlplane.KeyRegisterResponse{KeyID: in.KeyID, State: state})
	})

	mux.HandleFunc("/api/agent/v1/keys/challenge", func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		defer rs.mu.Unlock()
		target := rs.pending
		if rs.challengeKey != "" {
			target = rs.challengeKey
		}
		nonce := make([]byte, 32)
		_, _ = rand.Read(nonce)
		rs.nonce = nonce

		var pubArr [32]byte
		copy(pubArr[:], rs.pubs[target])
		sealed, _ := box.SealAnonymous(nil, nonce, &pubArr, rand.Reader)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(controlplane.KeyChallengeResponse{
			KeyID: target, Sealed: base64.StdEncoding.EncodeToString(sealed),
		})
	})

	mux.HandleFunc("/api/agent/v1/keys/prove", func(w http.ResponseWriter, r *http.Request) {
		var in controlplane.KeyProveRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		answer, _ := base64.StdEncoding.DecodeString(in.Nonce)

		rs.mu.Lock()
		defer rs.mu.Unlock()
		spent := rs.nonce
		rs.nonce = nil // spent by ANY answer, right or wrong

		w.Header().Set("Content-Type", "application/json")
		if in.KeyID != rs.pending || len(spent) == 0 || string(answer) != string(spent) ||
			strings.TrimSpace(in.StorageDetail) == "" {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "proof refused"})
			return
		}
		rs.proven = true
		_ = json.NewEncoder(w).Encode(controlplane.KeyProveResponse{
			KeyID: in.KeyID, State: controlplane.KeyStateProven,
		})
	})

	mux.HandleFunc("/api/agent/v1/keys/promote", func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		defer rs.mu.Unlock()
		rs.promotes++
		w.Header().Set("Content-Type", "application/json")
		if rs.pending == "" || !rs.proven {
			_ = json.NewEncoder(w).Encode(controlplane.KeyPromoteResponse{
				Promoted: false, ActiveKeyID: rs.active,
			})
			return
		}
		rs.active, rs.pending, rs.proven = rs.pending, "", false
		_ = json.NewEncoder(w).Encode(controlplane.KeyPromoteResponse{
			Promoted: true, ActiveKeyID: rs.active,
		})
	})

	rs.srv = httptest.NewServer(mux)
	t.Cleanup(rs.srv.Close)
	return rs
}

func (rs *rotServer) client() *controlplane.Client {
	return &controlplane.Client{BaseURL: rs.srv.URL, AgentID: 1, Secret: strings.Repeat("a", 40)}
}

func (rs *rotServer) state() (active, pending string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.active, rs.pending
}

// sealAs is the server sealing a payload to one of this agent's keys.
func (rs *rotServer) sealAs(t *testing.T, keyID string, plain []byte) []byte {
	t.Helper()
	rs.mu.Lock()
	pub := rs.pubs[keyID]
	rs.mu.Unlock()
	if len(pub) != 32 {
		t.Fatalf("server holds no public key for %s", keyID)
	}
	var pubArr [32]byte
	copy(pubArr[:], pub)
	sealed, err := box.SealAnonymous(nil, plain, &pubArr, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

// THE CASE THE CEREMONY EXISTS FOR. This machine lost its key file, so the key
// it registers is not the one the server is sealing to. Before this, that
// agent authenticated, checked in, took commands, and could not open a single
// secret -- with nothing anywhere reporting it.
func TestARegistrationThatComesBackPendingRotatesItselfActive(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	forgetAgentKeyRegistration()
	t.Cleanup(forgetAgentKeyRegistration)

	rs := newRotServer(t)
	// The server already has an active key for this machine, whose private
	// half this machine does not hold.
	strangerPub, _, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rs.mu.Lock()
	rs.active = "someone-elses-key"
	rs.pubs["someone-elses-key"] = strangerPub[:]
	rs.mu.Unlock()

	if err := ensureAgentKeyRegistered(rs.client()); err != nil {
		t.Fatalf("registration should have rotated to an active key: %v", err)
	}

	ours, _, err := loadOrCreateAgentKey()
	if err != nil {
		t.Fatal(err)
	}
	active, pending := rs.state()
	if active != ours {
		t.Fatalf("server active = %q, this machine holds %q", active, ours)
	}
	if pending != "" {
		t.Fatalf("a rotation is still in flight (%q)", pending)
	}
	if p := pendingAgentKeyID(); p != "" {
		t.Fatalf("this machine still records a pending key %q after promotion", p)
	}

	// And the point of all of it: a secret sealed to the key the server now
	// calls active opens here.
	secret := []byte("nimbus-clients@pbs!frontdesk-01 token secret")
	opened, err := openSealedToAgent(rs.sealAs(t, ours, secret), ours)
	if err != nil || string(opened) != string(secret) {
		t.Fatalf("the promoted key cannot open what the server seals to it: %v", err)
	}
}

// THE PROPERTY THAT MAKES THE CEREMONY SAFE. The server seals to the OLD key
// for the whole rotation, including after `proven`, so a machine that could
// not open old-key payloads mid-ceremony would go dark exactly when it is
// doing the most delicate thing it ever does.
func TestBothHalvesOpenWhileARotationIsInFlight(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	rs := newRotServer(t)
	c := rs.client()

	oldID, oldPub, err := loadOrCreateAgentKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RegisterKey(oldID, oldPub); err != nil {
		t.Fatal(err)
	}

	newID, newPub, err := beginAgentKeyRotation()
	if err != nil {
		t.Fatal(err)
	}
	if newID == oldID {
		t.Fatal("the rotation produced the key it was replacing")
	}
	if _, err := c.RegisterKey(newID, newPub); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ label, keyID string }{
		{"the old key, which the server is still sealing to", oldID},
		{"the new key, which it will seal to after promotion", newID},
	} {
		plain := []byte("payload for " + tc.label)
		got, err := openSealedToAgent(rs.sealAs(t, tc.keyID, plain), tc.keyID)
		if err != nil || string(got) != string(plain) {
			t.Fatalf("%s: %v", tc.label, err)
		}
	}

	// The hint is a hint. A server that names a key we no longer recognize
	// must not cost us a payload we can actually open.
	plain := []byte("named by a key id we never had")
	got, err := openSealedToAgent(rs.sealAs(t, oldID, plain), "2020-01-01-deadbeef")
	if err != nil || string(got) != string(plain) {
		t.Fatalf("a wrong hint lost a payload this machine could open: %v", err)
	}
}

// A crash between registering the pending key and promoting it must RESUME.
// Generating a second key there would be unrecoverable: the server allows one
// rotation in flight and would refuse the replacement, leaving a machine
// holding a key the server has never heard of and no way to tell it.
func TestARotationResumesRatherThanRestarting(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	rs := newRotServer(t)
	c := rs.client()

	oldID, oldPub, err := loadOrCreateAgentKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RegisterKey(oldID, oldPub); err != nil {
		t.Fatal(err)
	}

	// Registered, then the process died.
	pendingID, pendingPub, err := beginAgentKeyRotation()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RegisterKey(pendingID, pendingPub); err != nil {
		t.Fatal(err)
	}

	if err := rotateAgentKey(c, "resuming after a crash"); err != nil {
		t.Fatalf("resume: %v", err)
	}

	active, pending := rs.state()
	if active != pendingID || pending != "" {
		t.Fatalf("server active = %q pending = %q, want %q promoted", active, pending, pendingID)
	}
	nowID, _, err := loadOrCreateAgentKey()
	if err != nil {
		t.Fatal(err)
	}
	if nowID != pendingID {
		t.Fatalf("this machine's active key is %q, the server promoted %q", nowID, pendingID)
	}
	// Two registrations of the SAME id (the crashed attempt and the resume),
	// never a second key.
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.pubs) != 2 {
		t.Fatalf("the server saw %d distinct keys; a resume must not mint a new one", len(rs.pubs))
	}
}

// THE DEAD END, reported as itself. The server is rotating a key this machine
// does not hold, so the challenge is sealed to a public half whose private
// half is gone. Answering is impossible and GUESSING IS WORSE THAN USELESS: a
// wrong answer spends the challenge. There is no abandon-rotation endpoint, so
// this needs a person, and saying so beats retrying forever.
func TestARotationForSomebodyElsesKeyIsRefusedNotGuessedAt(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	rs := newRotServer(t)
	c := rs.client()

	oldID, oldPub, err := loadOrCreateAgentKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RegisterKey(oldID, oldPub); err != nil {
		t.Fatal(err)
	}

	// Somebody else's rotation is in flight, and the server will challenge it.
	strangerPub, _, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rs.mu.Lock()
	rs.pending = "not-ours"
	rs.pubs["not-ours"] = strangerPub[:]
	rs.challengeKey = "not-ours"
	rs.mu.Unlock()

	err = rotateAgentKey(c, "test")
	if !errors.Is(err, ErrRotationNotOurs) {
		t.Fatalf("err = %v, want ErrRotationNotOurs", err)
	}
	rs.mu.Lock()
	promotes := rs.promotes
	rs.mu.Unlock()
	if promotes != 0 {
		t.Fatal("a rotation it could not answer still tried to promote something")
	}
}

// The attestation is the half the server cannot verify, and it is built from
// this machine's own record. A key that is not in that record gets no claim
// made on its behalf.
func TestNoAttestationIsMadeForAKeyThisMachineDoesNotHold(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "tpm")

	if _, _, err := loadOrCreateAgentKey(); err != nil {
		t.Fatal(err)
	}
	if d := agentKeyStorageDetail("2020-01-01-deadbeef"); d != "" {
		t.Fatalf("attested to storage for a key we do not hold: %q", d)
	}

	id, _, err := loadOrCreateAgentKey()
	if err != nil {
		t.Fatal(err)
	}
	d := agentKeyStorageDetail(id)
	for _, want := range []string{"tpm", "read back"} {
		if !strings.Contains(d, want) {
			t.Fatalf("attestation %q does not mention %q", d, want)
		}
	}
}
