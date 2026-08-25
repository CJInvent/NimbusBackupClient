package main

// Seeding the org's encryption answer from a provisioning profile.
//
// THE BEHAVIOUR THIS EXISTS TO CHANGE: an enrolled agent that has never
// completed a check-in refuses to back up, because it cannot tell "this org
// does not encrypt" from "I have not been told" and guessing either way is
// either a silent downgrade or a stopped backup. Correct with no information —
// and a stopped backup on a freshly imaged machine, which is the worst moment
// for this product to say no. A preconfigured MSI knows the answer; this is the
// answer arriving with it.
//
// THE PROFILE IS UNAUTHENTICATED, so the tests below pin the limits as hard as
// the feature: it may fill a void, it may never overrule an answer that exists,
// it may not invent a third state, and it never seeds key material.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readRecord reads backup-key.json raw, without the DEK — the same thing a
// support engineer would do on a customer's machine.
func readRecord(t *testing.T) storedBackupKey {
	t.Helper()
	path, err := backupKeyPath()
	if err != nil {
		t.Fatalf("backupKeyPath: %v", err)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.Base(path), err)
	}
	var rec storedBackupKey
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("record is not valid JSON: %v", err)
	}
	return rec
}

// A machine with nothing recorded takes the profile's answer, and the answer
// is usable: adFromPersistedState stops returning ErrBackupKeyUnknown, which
// is the refusal this whole feature exists to lift.
func TestAProvisionedAnswerLiftsTheUnknownStateRefusal(t *testing.T) {
	isolateConfigDir(t)
	resetDEKCache()

	// PRECONDITION, and it is not decoration: if the machine were not in the
	// unknown state to begin with, everything below would pass over a feature
	// that does nothing.
	if _, err := adFromPersistedState(); err == nil {
		t.Fatal("a fresh machine was not in the unknown state; this test proves nothing")
	}

	seeded, err := seedEncryptionFromProvisioning(encStateOff)
	if err != nil {
		t.Fatalf("seedEncryptionFromProvisioning: %v", err)
	}
	if !seeded {
		t.Fatal("nothing was recorded on a machine with no prior answer")
	}

	ad, err := adFromPersistedState()
	if err != nil {
		t.Fatalf("still refusing after the profile answered: %v", err)
	}
	if ad != nil {
		t.Errorf("encryption=off produced an advertisement: %+v", ad)
	}

	rec := readRecord(t)
	if rec.Encryption != encStateOff {
		t.Errorf("recorded encryption = %q, want %q", rec.Encryption, encStateOff)
	}
	if rec.EncryptionSource != encSourceProvisioning {
		t.Errorf("source = %q, want %q — a provisional answer must be marked as one",
			rec.EncryptionSource, encSourceProvisioning)
	}
}

// "on" is seeded too, and it seeds NO KEY. A profile carries no material and
// could not be trusted with any; "on" means "expect to need a key", which
// makes the machine fetch or refuse rather than back up in the clear.
func TestSeedingOnRecordsNoKeyMaterial(t *testing.T) {
	isolateConfigDir(t)
	resetDEKCache()

	if _, err := seedEncryptionFromProvisioning(encStateOn); err != nil {
		t.Fatalf("seedEncryptionFromProvisioning: %v", err)
	}
	rec := readRecord(t)
	if rec.Encryption != encStateOn {
		t.Fatalf("recorded encryption = %q, want on", rec.Encryption)
	}
	if rec.Sealed != "" || rec.KeyID != "" || rec.EscrowBlob != "" {
		t.Errorf("a profile seeded key material: sealed=%q key_id=%q escrow=%q",
			rec.Sealed, rec.KeyID, rec.EscrowBlob)
	}
}

// THE LIMIT THAT MATTERS MOST. A profile may fill a void; it may never
// overrule an answer that already exists. Without this, an attacker who can
// drop a file into ProgramData could turn encryption off on a machine the
// server has already told to encrypt.
func TestAProfileNeverOverrulesAnExistingAnswer(t *testing.T) {
	isolateConfigDir(t)
	resetDEKCache()

	// The server has spoken: this org does not encrypt.
	if err := recordEncryptionOff(); err != nil {
		t.Fatalf("recordEncryptionOff: %v", err)
	}
	before := readRecord(t)
	if before.EncryptionSource != encSourceCheckin {
		t.Fatalf("PRECONDITION: a check-in answer is sourced %q, want %q",
			before.EncryptionSource, encSourceCheckin)
	}

	seeded, err := seedEncryptionFromProvisioning(encStateOn)
	if err != nil {
		t.Fatalf("seeding over an existing answer errored: %v", err)
	}
	if seeded {
		t.Fatal("a profile overwrote an answer the machine already had")
	}
	after := readRecord(t)
	if after.Encryption != encStateOff || after.EncryptionSource != encSourceCheckin {
		t.Errorf("the existing answer changed: %+v -> %+v", before, after)
	}
}

// A third value is refused rather than written. Writing one would put the
// machine back in the unknown state this feature exists to leave, with a
// record on disk claiming otherwise.
func TestSeedingRefusesAnythingButOnOrOff(t *testing.T) {
	isolateConfigDir(t)
	resetDEKCache()

	for _, bad := range []string{"", "maybe", "ON", "true"} {
		seeded, err := seedEncryptionFromProvisioning(bad)
		if err == nil {
			t.Errorf("seeding %q was accepted", bad)
		}
		if seeded {
			t.Errorf("seeding %q reported that it wrote something", bad)
		}
	}
	if _, err := adFromPersistedState(); err == nil {
		t.Error("a refused seed still left an answer behind")
	}
}

// A record that cannot be READ is not an empty one. Overwriting it would
// destroy a sealed key, so the seed refuses and reports rather than clobbers.
func TestSeedingRefusesWhenTheExistingRecordIsUnreadable(t *testing.T) {
	isolateConfigDir(t)
	resetDEKCache()

	path, err := backupKeyPath()
	if err != nil {
		t.Fatalf("backupKeyPath: %v", err)
	}
	if err := os.WriteFile(path, []byte("{this is not json"), 0o600); err != nil {
		t.Fatalf("writing a corrupt record: %v", err)
	}

	seeded, err := seedEncryptionFromProvisioning(encStateOff)
	if err == nil {
		t.Fatal("seeding over an unreadable record succeeded")
	}
	if seeded {
		t.Fatal("seeding over an unreadable record reported a write")
	}
	raw, rerr := os.ReadFile(path) //nolint:gosec // test-controlled temp path
	if rerr != nil || !strings.Contains(string(raw), "not json") {
		t.Fatalf("the unreadable record was overwritten: %q (%v)", string(raw), rerr)
	}
}

// Once the server confirms it, the record stops claiming a provisional source.
// Without this a machine would look permanently un-confirmed, and any report
// about provisional answers would be wrong about this one forever.
func TestACheckinUpgradesAProvisionedAnswerToAuthoritative(t *testing.T) {
	isolateConfigDir(t)
	resetDEKCache()

	if _, err := seedEncryptionFromProvisioning(encStateOff); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The server agrees: no key advertised.
	applyBackupKeyFromCheckin(nil)

	rec := readRecord(t)
	if rec.Encryption != encStateOff {
		t.Errorf("encryption = %q, want off", rec.Encryption)
	}
	if rec.EncryptionSource != encSourceCheckin {
		t.Errorf("source = %q, want %q — the server confirmed it, so it is no longer provisional",
			rec.EncryptionSource, encSourceCheckin)
	}
}
