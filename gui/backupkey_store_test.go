package main

// Tests for the durable backup-key store (phase F item 1).
//
// THE THING UNDER TEST IS THE FAILURE BEHAVIOUR, not the round trip. A store
// that seals and opens correctly but degrades quietly on the way — storing
// plaintext when the DEK is missing, returning "" instead of an error when the
// record cannot be opened — would pass a round-trip test and lose a customer's
// backups. Each case below names the degradation it forbids.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"controlplane"
)

// withProtector installs a fixed DEK under a named protector for one test, so
// the durable paths are reachable on a Linux runner that has neither DPAPI nor
// a TPM.
func withProtector(t *testing.T, protector string) {
	t.Helper()
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		t.Fatalf("rand: %v", err)
	}
	prev := dekSource
	dekSource = func() ([]byte, string, error) { return dek, protector, nil }
	t.Cleanup(func() { dekSource = prev })
}

func withBrokenDEK(t *testing.T, err error) {
	t.Helper()
	prev := dekSource
	dekSource = func() ([]byte, string, error) { return nil, "", err }
	t.Cleanup(func() { dekSource = prev })
}

// freshKeyMaterial builds a delivery whose key_id genuinely matches its bytes.
// GENERATED, never a literal: a fixed key in a test file is a credential-shaped
// literal the secret scanner is right to flag, and a fresh one only passes if
// the verification actually recomputes the digest.
func freshKeyMaterial(t *testing.T) (*controlplane.BackupKeyMaterial, []byte) {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand: %v", err)
	}
	escrow := make([]byte, 256)
	if _, err := rand.Read(escrow); err != nil {
		t.Fatalf("rand: %v", err)
	}
	sealed, sealedTo := sealToThisMachine(t, raw)
	return &controlplane.BackupKeyMaterial{
		KeySealedB64:      sealed,
		SealedToKeyID:     sealedTo,
		KeyID:             keyIDOf(raw),
		Version:           2,
		Scope:             "org",
		EscrowBlobB64:     base64.StdEncoding.EncodeToString(escrow),
		MasterFingerprint: "fp-test",
	}, raw
}

// keyIDOf mirrors the server's definition: sha256 of the RAW key. Computed from
// the primitive here rather than by calling the production helper, so a test
// cannot pass by agreeing with a broken implementation of the same thing.
func keyIDOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestBackupKeyStoreRoundTrip(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	m, raw := freshKeyMaterial(t)
	if err := storeBackupKey(m); err != nil {
		t.Fatalf("storeBackupKey: %v", err)
	}

	got, escrow, keyID, err := loadBackupKey()
	if err != nil {
		t.Fatalf("loadBackupKey: %v", err)
	}
	if string(got) != string(raw) {
		t.Errorf("key did not round-trip")
	}
	if keyID != m.KeyID {
		t.Errorf("key_id = %q, want %q", keyID, m.KeyID)
	}
	if len(escrow) == 0 {
		t.Errorf("escrow blob lost in storage — every snapshot would ship without a recovery path")
	}

	st := backupKeyStorage()
	if st.Err != nil || !st.Durable || st.StoredKeyID != m.KeyID {
		t.Errorf("storage = %+v, want durable holding %s", st, m.KeyID)
	}
}

// THE RAW KEY MUST NOT BE READABLE IN THE FILE. The whole point of sealing it
// under the DEK is defeated by any field that carries the material verbatim, and
// a round-trip test passes just as well when the "sealing" is a base64 copy.
func TestBackupKeyStoreDoesNotWriteTheKeyInTheClear(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	m, raw := freshKeyMaterial(t)
	if err := storeBackupKey(m); err != nil {
		t.Fatalf("storeBackupKey: %v", err)
	}
	path, err := backupKeyPath()
	if err != nil {
		t.Fatalf("backupKeyPath: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the stored record: %v", err)
	}
	if strings.Contains(string(data), base64.StdEncoding.EncodeToString(raw)) {
		t.Errorf("the raw backup key appears base64-encoded in %s", path)
	}
	if strings.Contains(string(data), string(raw)) {
		t.Errorf("the raw backup key appears verbatim in %s", path)
	}
}

// A machine whose only protector is `plaintext` MUST NOT get a durable store.
// This is the difference from encryptSecret, which would write the value
// unencrypted and log a warning nobody reads.
func TestBackupKeyStoreRefusesPlaintextProtector(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "plaintext")

	m, _ := freshKeyMaterial(t)
	err := storeBackupKey(m)
	if err == nil {
		t.Fatalf("stored a backup key under the plaintext protector")
	}
	if !errors.Is(err, errNoDurableStorage) {
		t.Errorf("error = %v, want it to wrap errNoDurableStorage so the caller can take the ephemeral path", err)
	}
	path, err2 := backupKeyPath()
	if err2 != nil {
		t.Fatalf("backupKeyPath: %v", err2)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a key file exists at %s after a refused store", path)
	}

	// And the gate must be told this is WEAK storage, not BROKEN storage —
	// weak means fetch per run, broken means refuse.
	st := backupKeyStorage()
	if st.Durable {
		t.Errorf("storage reported durable under the plaintext protector")
	}
	if st.Err != nil {
		t.Errorf("weak storage reported as unreadable (%v) — that would refuse the backup instead of going ephemeral", st.Err)
	}
	if st.StoredKeyID != "" {
		t.Errorf("non-durable storage claims to hold %s; nothing is ever written there", st.StoredKeyID)
	}
}

// An unreadable DEK is NOT the same as a weak one. The gate refuses on this,
// because a key may be sitting in that file and we cannot say which.
func TestBackupKeyStorageDistinguishesUnreadableFromWeak(t *testing.T) {
	isolateConfigDir(t)
	boom := errors.New("tpm unwrap failed")
	withBrokenDEK(t, boom)

	st := backupKeyStorage()
	if st.Err == nil {
		t.Fatalf("an unreadable master key was reported as ordinary storage")
	}
	if st.Durable {
		t.Errorf("unreadable storage must not claim to be durable")
	}
	if !strings.Contains(st.Err.Error(), boom.Error()) {
		t.Errorf("the underlying storage error was lost: %v", st.Err)
	}
}

// Absent is not an error. A machine that has never fetched must be allowed to
// fetch, not refused.
func TestBackupKeyStorageAbsentIsNotAnError(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "tpm")

	st := backupKeyStorage()
	if st.Err != nil {
		t.Fatalf("a missing key file reported as a storage error: %v", st.Err)
	}
	if !st.Durable || st.StoredKeyID != "" {
		t.Errorf("storage = %+v, want durable and empty", st)
	}
}

// A record that cannot be opened must ERROR, never yield an empty key. This is
// decryptSecret's "" behaviour, and it is the one that would silently encrypt
// under nothing.
func TestBackupKeyLoadFailsLoudlyOnAForeignKey(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	m, _ := freshKeyMaterial(t)
	if err := storeBackupKey(m); err != nil {
		t.Fatalf("storeBackupKey: %v", err)
	}

	// Same protector name, different DEK — exactly what moving the disk to
	// another machine looks like.
	withProtector(t, "dpapi")

	raw, _, _, err := loadBackupKey()
	if err == nil {
		t.Fatalf("opened a key sealed under a different DEK")
	}
	if raw != nil {
		t.Errorf("material returned alongside an error")
	}
	// PIN WHICH CHECK CAUGHT IT. Two guards stand here — the AEAD open and the
	// key_id re-verification — and the second will report an error even if the
	// first is broken. Replacing the open's error with an empty key therefore
	// passes an assertion that only asks "did something fail", which is exactly
	// the degradation this file exists to prevent. Asserting the message pins
	// the AEAD as the guard that fired.
	if !strings.Contains(err.Error(), "could not be decrypted") {
		t.Errorf("error = %v; want the AEAD open to be what refused, not a downstream check", err)
	}

	// And the gate must see storage it cannot trust rather than "absent".
	st := backupKeyStorage()
	if st.StoredKeyID != m.KeyID {
		t.Errorf("StoredKeyID = %q; the record is still readable and still names its key", st.StoredKeyID)
	}
}

// Corrupt JSON is unreadable storage, not absent storage.
func TestBackupKeyStorageCorruptRecordIsUnreadable(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	path, err := backupKeyPath()
	if err != nil {
		t.Fatalf("backupKeyPath: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatalf("writing a corrupt record: %v", err)
	}
	st := backupKeyStorage()
	if st.Err == nil {
		t.Fatalf("a corrupt record was reported as ordinary (absent) storage")
	}
}

// Material that does not match its stated key_id must never reach the disk.
func TestBackupKeyStoreRejectsMismatchedMaterial(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	m, _ := freshKeyMaterial(t)
	other := make([]byte, 32)
	if _, err := rand.Read(other); err != nil {
		t.Fatalf("rand: %v", err)
	}
	m.KeySealedB64, m.SealedToKeyID = sealToThisMachine(t, other) // right shape, wrong bytes

	if err := storeBackupKey(m); err == nil {
		t.Fatalf("stored material that does not match its key_id")
	}
	path, _ := backupKeyPath()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a key file was written despite the verification failing")
	}
}

// A delivery with no escrow blob is refused. It encrypts perfectly well and is
// unrecoverable the day the control plane is gone.
func TestBackupKeyStoreRefusesMissingEscrowBlob(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	m, _ := freshKeyMaterial(t)
	m.EscrowBlobB64 = ""
	if err := storeBackupKey(m); err == nil {
		t.Fatalf("stored a backup key with no escrow blob")
	}
}

// The record must name the key it holds even when the material cannot be
// opened — otherwise a machine cannot report a mismatch it can see.
func TestBackupKeyRecordKeepsIdentifiersReadable(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	m, _ := freshKeyMaterial(t)
	if err := storeBackupKey(m); err != nil {
		t.Fatalf("storeBackupKey: %v", err)
	}
	path, _ := backupKeyPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var rec storedBackupKey
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("record is not valid JSON: %v", err)
	}
	if rec.KeyID != m.KeyID || rec.Scope != m.Scope || rec.Version != m.Version {
		t.Errorf("record = %+v, want the delivered identifiers", rec)
	}
	if rec.Protector != "dpapi" {
		t.Errorf("record protector = %q, want dpapi", rec.Protector)
	}
}
