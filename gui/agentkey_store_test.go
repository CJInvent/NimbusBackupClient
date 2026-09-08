package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"controlplane"

	"golang.org/x/crypto/nacl/box"
)

// This machine's identity: created once, survives a restart, and opens what
// the control plane seals to it.
//
// Every test isolates the config dir and installs a protector, because
// getConfigDir() consults ProgramData on every platform and a Linux runner has
// neither DPAPI nor a TPM -- without both, these would either write into a
// developer's real home directory or never reach a durable path at all.

func TestAgentKeyIsCreatedOnceAndReadBack(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	keyID, pub, err := loadOrCreateAgentKey()
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if len(pub) != 32 {
		t.Fatalf("public key is %d bytes, the wire contract says 32", len(pub))
	}

	// A second call is the next process start, not a second key. An agent that
	// re-keyed on every boot would register a rotation each time and the
	// server would refuse the second one as already in flight.
	againID, againPub, err := loadOrCreateAgentKey()
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if againID != keyID || string(againPub) != string(pub) {
		t.Fatalf("a second call produced a different key: %s -> %s", keyID, againID)
	}
}

// The private half must be at rest under the DEK, never in the clear beside
// the public one. This reads the actual file rather than trusting the writer.
func TestAgentKeyPrivateHalfIsNotOnDiskInTheClear(t *testing.T) {
	dir := isolateConfigDir(t)
	withProtector(t, "dpapi")

	_, pub, err := loadOrCreateAgentKey()
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Find the record wherever getConfigDir() put it under the temp root.
	var data []byte
	err = filepath.Walk(dir, func(p string, info os.FileInfo, werr error) error {
		if werr == nil && !info.IsDir() && filepath.Base(p) == agentKeyFileName {
			data, _ = os.ReadFile(p)
		}
		return nil
	})
	if err != nil || len(data) == 0 {
		t.Fatalf("no %s written under %s", agentKeyFileName, dir)
	}

	// Whatever the record contains, the raw private bytes must not be in it.
	// Reconstruct them the only legitimate way and search for their encoding.
	priv := openPrivateForTest(t, "active")
	if strings.Contains(string(data), base64.StdEncoding.EncodeToString(priv)) {
		t.Fatal("the private key is stored in the clear")
	}
	if !strings.Contains(string(data), base64.StdEncoding.EncodeToString(pub)) {
		t.Fatal("the public key should be stored in the clear — a load has to verify the pair")
	}
}

// What the server does, done here: seal to the registered public key, open
// with the stored private one.
func TestAgentKeyOpensWhatWasSealedToIt(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "tpm")

	secret := []byte("nimbus-clients@pbs!frontdesk-01 token secret")
	sealedB64, _ := sealToThisMachine(t, secret)
	sealed, err := base64.StdEncoding.DecodeString(sealedB64)
	if err != nil {
		t.Fatal(err)
	}

	opened, err := openSealedToAgent(sealed, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(opened) != string(secret) {
		t.Fatalf("opened %q, want %q", opened, secret)
	}
}

// A payload sealed to somebody ELSE'S key must fail, and say so as itself. The
// realistic cause is a rotation the server has moved past, not a corrupted
// message, and the two want different reactions from whoever reads the log.
func TestAgentKeyRefusesAPayloadSealedToAnotherKey(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	if _, _, err := loadOrCreateAgentKey(); err != nil {
		t.Fatal(err)
	}

	otherPub, _, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.SealAnonymous(nil, []byte("not for you"), otherPub, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := openSealedToAgent(sealed, ""); err == nil {
		t.Fatal("opening a payload sealed to another key must fail, not return empty bytes")
	} else if !strings.Contains(err.Error(), "could not be opened") {
		t.Fatalf("unhelpful error for the rotation case: %v", err)
	}
}

// THE FORMAT PIN. crypto_box_seal is 32 bytes of ephemeral public key plus a
// 16-byte Poly1305 tag around the plaintext -- 48 bytes of overhead, no nonce
// on the wire because it is derived from the two public keys.
//
// This is the part of PHP-libsodium/Go-nacl agreement a test can check without
// a PHP interpreter on the runner. The live check was done directly: a payload
// sealed by the server's sodium_crypto_box_seal opened with this client's
// nacl/box.OpenAnonymous (2026-09-07, 43-byte plaintext, 91-byte sealed, 48
// bytes of overhead). If either library ever changed the construction, this
// fails here instead of on a customer's first encrypted backup.
func TestSealedBoxOverheadMatchesLibsodium(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	plain := make([]byte, 43)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	sealedB64, _ := sealToThisMachine(t, plain)
	sealed, err := base64.StdEncoding.DecodeString(sealedB64)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(sealed) - len(plain); got != 48 {
		t.Fatalf("sealed overhead is %d bytes, libsodium's crypto_box_seal is 48", got)
	}
	if len(sealed) != 91 {
		t.Fatalf("a 43-byte payload sealed to 32-byte key is 91 bytes; got %d", len(sealed))
	}
}

// The id is a function of the key, which is what makes re-registration
// idempotent and makes "same id, different bytes" -- the collision the server
// refuses -- unreachable by accident.
//
// THE DERIVATION IS RECOMPUTED HERE, from the primitive, rather than asserted
// by calling agentKeyID twice and comparing the two answers. That comparison
// was the first version of this test and it was worth almost nothing: it
// proves the function is not RANDOM, which is not the property anyone cares
// about, and it would have passed just as happily if the id stopped depending
// on the key at all. staticcheck refused it outright (SA4000, identical
// expressions either side of !=) and was right to -- the lint found a weak
// assertion, not a style problem.
func TestAgentKeyIDIsDerivedFromTheKey(t *testing.T) {
	a, _, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	id := agentKeyID(a[:])

	// Structural, not "equals today's date": agentKeyID reads the clock, and
	// an assertion against a second clock read is a test that fails once a
	// year at midnight UTC for no reason anyone will enjoy diagnosing.
	cut := strings.LastIndex(id, "-")
	if cut < 0 {
		t.Fatalf("id %q is not <date>-<hash>", id)
	}
	if _, err := time.Parse("2006-01-02", id[:cut]); err != nil {
		t.Fatalf("id %q does not start with a UTC date: %v", id, err)
	}

	sum := sha256.Sum256(a[:])
	if got, want := id[cut+1:], hex.EncodeToString(sum[:4]); got != want {
		t.Fatalf("id suffix = %q, want %q (the first 8 hex of sha256 over the public key)", got, want)
	}

	// And the part that matters to the server: two keys cannot collide on one
	// id, because that is the state it refuses as a rotation gone wrong.
	if agentKeyID(a[:]) == agentKeyID(b[:]) {
		t.Fatal("two different keys produced the same id")
	}
	if len(id) > 128 {
		t.Fatal("the server caps a key id at 128 characters")
	}
}

// openDeliveredKey is the one place a delivery is opened, so its refusals are
// worth pinning: every one of them must be an error, never empty bytes a
// caller would read as "no key".
func TestOpenDeliveredKeyRefusesEveryMalformedDelivery(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	if _, _, err := loadOrCreateAgentKey(); err != nil {
		t.Fatal(err)
	}

	cases := map[string]*controlplane.BackupKeyMaterial{
		"nothing delivered at all": nil,
		"no sealed material":       {KeyID: "k"},
		"sealed material that is not base64": {
			KeySealedB64: "not base64!!", KeyID: "k",
		},
		"sealed material that is not a sealed box": {
			KeySealedB64: base64.StdEncoding.EncodeToString([]byte("short")), KeyID: "k",
		},
	}
	for label, m := range cases {
		raw, err := openDeliveredKey(m)
		if err == nil {
			t.Fatalf("%s: expected an error", label)
		}
		if raw != nil {
			t.Fatalf("%s: returned %d bytes alongside an error", label, len(raw))
		}
	}
}

// openPrivateForTest reconstructs a stored private half through the production
// path, so the leak check above searches for the real bytes.
func openPrivateForTest(t *testing.T, slot string) []byte {
	t.Helper()
	f, err := readAgentKeyFile()
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	rec := f.Active
	if slot == "pending" {
		rec = f.Pending
	}
	if rec == nil {
		t.Fatalf("no %s key in the record", slot)
	}
	priv, err := openStoredAgentPrivate(rec)
	if err != nil {
		t.Fatalf("open private: %v", err)
	}
	return priv
}
