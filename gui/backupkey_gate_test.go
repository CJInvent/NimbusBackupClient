package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"controlplane"
)

// The persisted-state half of the gate. The pure decision is tested in
// controlplane/backupkey_test.go; what is testable HERE is the part that
// decides which advertisement the decision even sees when the control plane has
// not been reached — the case that turns "we were never told" into either a
// silent downgrade or a refusal.

func TestPersistedStateUnknownRefuses(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	ad, err := adFromPersistedState()
	if !errors.Is(err, ErrBackupKeyUnknown) {
		t.Fatalf("err = %v, want ErrBackupKeyUnknown", err)
	}
	if ad != nil {
		t.Errorf("ad = %+v, want nil alongside the refusal", ad)
	}
}

// The whole reason recordEncryptionOff exists: after it, a machine that cannot
// reach the server knows the org does not encrypt and backs up normally.
func TestPersistedStateOffProceedsPlain(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	if err := recordEncryptionOff(); err != nil {
		t.Fatalf("recordEncryptionOff: %v", err)
	}
	ad, err := adFromPersistedState()
	if err != nil {
		t.Fatalf("adFromPersistedState after recording off: %v", err)
	}
	if ad != nil {
		t.Errorf("ad = %+v, want nil (proceed in the clear)", ad)
	}
}

// And the mirror: a machine that holds a key keeps encrypting through an
// outage. This is the property that makes durable mode worth having.
func TestPersistedStateOnSurvivesAnOutage(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	m, _ := freshKeyMaterial(t)
	if err := storeBackupKey(m); err != nil {
		t.Fatalf("storeBackupKey: %v", err)
	}
	ad, err := adFromPersistedState()
	if err != nil {
		t.Fatalf("adFromPersistedState: %v", err)
	}
	if ad == nil || ad.KeyID != m.KeyID {
		t.Fatalf("ad = %+v, want the stored key %s", ad, m.KeyID)
	}
	// The reconstructed advertisement plus the stored key must satisfy the
	// pure decision, or the outage path refuses on a machine that is holding
	// exactly what it was told to hold.
	action, reason := controlplane.BackupKeyDecision(ad, backupKeyStorage(), false)
	if action != controlplane.BackupKeyProceedEncrypted {
		t.Errorf("action = %v (%s), want proceed-encrypted", action, reason)
	}
}

// Recording "off" must drop material: a machine is not entitled to keep a key
// after the org stops encrypting.
func TestRecordEncryptionOffDropsTheKey(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	m, _ := freshKeyMaterial(t)
	if err := storeBackupKey(m); err != nil {
		t.Fatalf("storeBackupKey: %v", err)
	}
	if err := recordEncryptionOff(); err != nil {
		t.Fatalf("recordEncryptionOff: %v", err)
	}
	if _, _, _, err := loadBackupKey(); err == nil {
		t.Error("the backup key is still readable after encryption was turned off")
	}
	if st := backupKeyStorage(); st.StoredKeyID != "" {
		t.Errorf("storage still reports holding %s", st.StoredKeyID)
	}
	path, _ := backupKeyPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), m.KeyID) {
		t.Error("the record still names the dropped key")
	}
}

// recordEncryptionOff must work on a machine with NO durable protector. That is
// exactly the machine most in need of the answer — it fetches per run and would
// otherwise keep trying to reach a server that has told it not to bother.
func TestRecordEncryptionOffNeedsNoProtector(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "plaintext")

	if err := recordEncryptionOff(); err != nil {
		t.Fatalf("recordEncryptionOff on a weak-storage machine: %v", err)
	}
	state, _, err := persistedEncryption()
	if err != nil || state != encStateOff {
		t.Errorf("state = %q err = %v, want off", state, err)
	}
}

// --- the ephemeral holder --------------------------------------------------

func TestEphemeralKeyVerifiesMaterial(t *testing.T) {
	m, raw := freshKeyMaterial(t)
	key, escrow, err := ephemeralKeyFromMaterial(m)
	if err != nil {
		t.Fatalf("ephemeralKeyFromMaterial: %v", err)
	}
	if string(key) != string(raw) || len(escrow) == 0 {
		t.Error("the delivered key or escrow blob did not survive")
	}
}

// ONE FLIPPED BIT. Right length, right shape, wrong bytes — which is what a
// length check alone waves through, and what would encrypt a backup nobody can
// read with nothing on disk afterwards to explain it.
func TestEphemeralKeyRejectsWrongBytes(t *testing.T) {
	m, raw := freshKeyMaterial(t)
	bad := append([]byte(nil), raw...)
	bad[7] ^= 0x01
	m.KeyB64 = base64.StdEncoding.EncodeToString(bad)

	if _, _, err := ephemeralKeyFromMaterial(m); err == nil {
		t.Fatal("accepted material that does not match its key_id")
	}
}

func TestEphemeralKeyRequiresEscrowBlob(t *testing.T) {
	m, _ := freshKeyMaterial(t)
	m.EscrowBlobB64 = ""
	if _, _, err := ephemeralKeyFromMaterial(m); err == nil {
		t.Fatal("accepted an ephemeral key with no escrow blob; the snapshot would be " +
			"recoverable only from the control plane, and this machine keeps nothing")
	}
}

func TestEphemeralKeyRejectsWrongLength(t *testing.T) {
	m, _ := freshKeyMaterial(t)
	short := make([]byte, 16)
	if _, err := rand.Read(short); err != nil {
		t.Fatalf("rand: %v", err)
	}
	m.KeyB64 = base64.StdEncoding.EncodeToString(short)
	if _, _, err := ephemeralKeyFromMaterial(m); err == nil {
		t.Fatal("accepted a 16-byte backup key")
	}
}

// The check-in hook, at its call site. The controlplane package proves the hook
// FIRES on a null advertisement; this proves what this side does with it —
// without which the persisted "off" that the outage path depends on is never
// written by anything.
func TestCheckinHookRecordsEncryptionOff(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	applyBackupKeyFromCheckin(nil)

	state, _, err := persistedEncryption()
	if err != nil {
		t.Fatalf("persistedEncryption: %v", err)
	}
	if state != encStateOff {
		t.Fatalf("state = %q after a null advertisement, want off — without this a machine "+
			"that reboots into an outage cannot tell 'no encryption' from 'never heard'", state)
	}
}

// An advertisement must NOT overwrite the state to "off", and must not be
// mistaken for a key having been stored: material is fetched at backup time, on
// a mismatch, which is what keeps it off the wire every cycle.
func TestCheckinHookDoesNotRecordAKeyItWasOnlyToldAbout(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")

	applyBackupKeyFromCheckin(&controlplane.BackupKeyAd{KeyID: "abc123"})

	state, keyID, err := persistedEncryption()
	if err != nil {
		t.Fatalf("persistedEncryption: %v", err)
	}
	if state != encStateUnknown || keyID != "" {
		t.Errorf("state = %q key = %q; an advertisement is not material and must not be recorded as held",
			state, keyID)
	}
}
