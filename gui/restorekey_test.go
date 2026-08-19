//go:build service
// +build service

package main

// Tests for restore-side key resolution (phase G).
//
// WHAT IS UNDER TEST IS THE SELECTION, not the crypto. Whether AES-GCM works
// is settled in pbscommon; what is new and easy to get wrong here is deciding
// WHICH key a snapshot needs and what to do when no source has it. The failure
// this guards against is the quiet one: picking up whatever key the machine
// happens to hold, decrypting nothing successfully, and reporting it as data
// corruption.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"pbscommon"
)

// keyWithFingerprint builds real key material and returns it with the PBS
// fingerprint it derives to. Generated, not a literal: a fixed 32-byte key in
// a source file is a credential-shaped literal the secret scan is right to
// flag, and a derived fingerprint proves more than a pasted one.
func keyWithFingerprint(t *testing.T, seed byte) (raw []byte, fingerprint string) {
	t.Helper()
	raw = make([]byte, pbscommon.KeySize)
	for i := range raw {
		raw[i] = seed ^ byte(i)
	}
	cc, err := pbscommon.NewCryptConfig(raw)
	if err != nil {
		t.Fatalf("NewCryptConfig: %v", err)
	}
	return raw, pbscommon.FingerprintString(cc.Fingerprint())
}

// withKeySources swaps the source list for one test and restores it after.
func withKeySources(t *testing.T, srcs ...keySource) {
	t.Helper()
	saved := keySources
	keySources = srcs
	t.Cleanup(func() { keySources = saved })
}

func sourceHolding(name string, raw []byte, fp string) keySource {
	return keySource{name: name, get: func(want string) ([]byte, bool, error) {
		if want != fp {
			return nil, false, nil
		}
		return raw, true, nil
	}}
}

// An empty fingerprint is a snapshot that names no key. It must succeed with
// no key and must not consult a single source — a restore of a pre-encryption
// snapshot has to work on a machine with no key and no control plane at all.
func TestUnencryptedSnapshotNeedsNoKeyAndAsksNobody(t *testing.T) {
	asked := false
	withKeySources(t, keySource{name: "must not be asked", get: func(string) ([]byte, bool, error) {
		asked = true
		return nil, false, nil
	}})

	raw, err := keyForFingerprint("")
	if err != nil {
		t.Fatalf("an unencrypted snapshot was refused: %v", err)
	}
	if raw != nil {
		t.Fatalf("a key was produced for an unencrypted snapshot: %d bytes", len(raw))
	}
	if asked {
		t.Fatal("a key source was consulted for a snapshot that names no key")
	}
}

func TestTheRightKeyIsReturned(t *testing.T) {
	raw, fp := keyWithFingerprint(t, 0x11)
	withKeySources(t, sourceHolding("test store", raw, fp))

	got, err := keyForFingerprint(fp)
	if err != nil {
		t.Fatalf("keyForFingerprint: %v", err)
	}
	cc, err := pbscommon.NewCryptConfig(got)
	if err != nil {
		t.Fatalf("NewCryptConfig: %v", err)
	}
	if derived := pbscommon.FingerprintString(cc.Fingerprint()); derived != fp {
		t.Fatalf("returned material derives to %s, want %s", derived, fp)
	}
}

// LOCAL BEFORE REMOTE. A machine holding the right key must not touch the
// control plane, or durable storage's whole reason for existing — restoring
// through an outage — is quietly undone on the path where it matters most.
func TestALocalKeyIsUsedWithoutAskingTheServer(t *testing.T) {
	raw, fp := keyWithFingerprint(t, 0x22)
	remoteAsked := false
	withKeySources(t,
		sourceHolding("local", raw, fp),
		keySource{name: "remote", get: func(string) ([]byte, bool, error) {
			remoteAsked = true
			return nil, false, nil
		}},
	)

	if _, err := keyForFingerprint(fp); err != nil {
		t.Fatalf("keyForFingerprint: %v", err)
	}
	if remoteAsked {
		t.Fatal("the control plane was consulted while the key was already held locally")
	}
}

// A source that does not have this key says so and the search continues. This
// is the rotation case: the machine holds the CURRENT key, the snapshot wants
// an older one, and that must not read as a failure of the store.
func TestSearchContinuesPastASourceWithoutThisKey(t *testing.T) {
	_, wantFP := keyWithFingerprint(t, 0x33)
	otherRaw, otherFP := keyWithFingerprint(t, 0x44)
	rightRaw, _ := keyWithFingerprint(t, 0x33)

	withKeySources(t,
		sourceHolding("holds a different key", otherRaw, otherFP),
		sourceHolding("holds the right one", rightRaw, wantFP),
	)

	if _, err := keyForFingerprint(wantFP); err != nil {
		t.Fatalf("the search stopped at a source that did not have the key: %v", err)
	}
}

// No source has it: a specific, actionable refusal that names the fingerprint.
// "Restore failed" would be true and useless — the operator's next move is to
// find that key, and they need to know which one it is.
func TestNoSourceHasTheKey(t *testing.T) {
	_, fp := keyWithFingerprint(t, 0x55)
	withKeySources(t, keySource{name: "empty", get: func(string) ([]byte, bool, error) {
		return nil, false, nil
	}})

	_, err := keyForFingerprint(fp)
	if err == nil {
		t.Fatal("a snapshot with no available key resolved successfully")
	}
	if !errors.Is(err, ErrNoKeyForSnapshot) {
		t.Fatalf("refusal is not ErrNoKeyForSnapshot: %v", err)
	}
	if !strings.Contains(err.Error(), fp) {
		t.Fatalf("the refusal does not name the key it needed: %v", err)
	}
}

// A source that could not be CONSULTED is different from one that is empty,
// and its error has to survive into the final message. The case that matters
// is a GUI refused permission on the service-owned key file: an operator
// reading "no source could supply the key" with nothing else would go hunting
// for a missing key that is sitting on the disk in front of them.
func TestAnUnreadableSourceIsReportedInTheRefusal(t *testing.T) {
	_, fp := keyWithFingerprint(t, 0x66)
	withKeySources(t, keySource{name: "the key store", get: func(string) ([]byte, bool, error) {
		return nil, false, errors.New("permission denied")
	}})

	_, err := keyForFingerprint(fp)
	if err == nil {
		t.Fatal("an unreadable key store resolved successfully")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("the store's own error did not survive into the refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "the key store") {
		t.Fatalf("the refusal does not say which source failed: %v", err)
	}
}

// An unreadable source must not stop a later one from succeeding. Failing the
// whole restore because one store was unreachable would make every machine
// depend on every source.
func TestAnUnreadableSourceDoesNotBlockALaterOne(t *testing.T) {
	raw, fp := keyWithFingerprint(t, 0x77)
	withKeySources(t,
		keySource{name: "broken", get: func(string) ([]byte, bool, error) {
			return nil, false, errors.New("permission denied")
		}},
		sourceHolding("working", raw, fp),
	)

	if _, err := keyForFingerprint(fp); err != nil {
		t.Fatalf("a working source was not reached past a broken one: %v", err)
	}
}

// THE ONE THAT MATTERS MOST. A source that hands back material for the wrong
// key must be refused outright, not treated as "not this one, try the next".
// Using it would mean decrypting a snapshot with the wrong key, and the AEAD
// failure that follows arrives hundreds of chunks later looking like corrupt
// storage.
func TestASourceOfferingTheWrongMaterialIsRefused(t *testing.T) {
	_, wantFP := keyWithFingerprint(t, 0x88)
	wrongRaw, wrongFP := keyWithFingerprint(t, 0x99)

	fallbackReached := false
	withKeySources(t,
		keySource{name: "a lying source", get: func(string) ([]byte, bool, error) {
			return wrongRaw, true, nil // claims ok for whatever was asked
		}},
		keySource{name: "fallback", get: func(string) ([]byte, bool, error) {
			fallbackReached = true
			return nil, false, nil
		}},
	)

	_, err := keyForFingerprint(wantFP)
	if err == nil {
		t.Fatal("material that derives to the wrong fingerprint was accepted")
	}
	// Pin WHICH guard fired. "It errored" would also be satisfied by falling
	// through to the no-key-found refusal, which is a completely different
	// outcome and would leave the mismatch undetected.
	if !strings.Contains(err.Error(), "derives to") {
		t.Fatalf("the mismatch guard did not fire; got: %v", err)
	}
	if !strings.Contains(err.Error(), wrongFP) {
		t.Fatalf("the error does not name what the material actually derives to: %v", err)
	}
	if fallbackReached {
		t.Fatal("the search continued past a source that offered wrong material")
	}
}

// Material of the wrong LENGTH must be rejected by the CryptConfig build and
// recorded as that source failing — not crash, and not be mistaken for a
// fingerprint mismatch.
func TestUnusableMaterialIsRecordedAgainstItsSource(t *testing.T) {
	_, fp := keyWithFingerprint(t, 0xAA)
	withKeySources(t, keySource{name: "short-key source", get: func(string) ([]byte, bool, error) {
		return []byte("too short"), true, nil
	}})

	_, err := keyForFingerprint(fp)
	if err == nil {
		t.Fatal("a key of the wrong length was accepted")
	}
	if !strings.Contains(err.Error(), "short-key source") {
		t.Fatalf("the failure was not attributed to its source: %v", err)
	}
}

// The real durable source must report "nothing here" rather than an error when
// this machine has never been told the org encrypts. Every restore on a fresh
// or unmanaged machine takes that path, and an error there would put a storage
// fault in front of an operator restoring an unencrypted snapshot.
func TestStoredSourceIsSilentWhenNothingHasBeenRecorded(t *testing.T) {
	isolateConfigDir(t)
	resetDEKCache()

	_, fp := keyWithFingerprint(t, 0xBB)
	raw, ok, err := storedKeyForFingerprint(fp)
	if err != nil {
		t.Fatalf("an empty key store reported an error: %v", err)
	}
	if ok || raw != nil {
		t.Fatal("an empty key store claimed to hold a key")
	}
}

// And when the org is recorded as NOT encrypting, likewise: no key, no fault.
func TestStoredSourceIsSilentWhenEncryptionIsOff(t *testing.T) {
	isolateConfigDir(t)
	resetDEKCache()

	if err := recordEncryptionOff(); err != nil {
		t.Fatalf("recordEncryptionOff: %v", err)
	}
	_, fp := keyWithFingerprint(t, 0xCC)
	_, ok, err := storedKeyForFingerprint(fp)
	if err != nil {
		t.Fatalf("a store recording encryption-off reported an error: %v", err)
	}
	if ok {
		t.Fatal("a store recording encryption-off claimed to hold a key")
	}
}

// A useful message for the rotation case, which is the one an operator will
// meet in practice.
func TestRefusalNamesTheFingerprintForARotatedKey(t *testing.T) {
	_, oldFP := keyWithFingerprint(t, 0xDD)
	currentRaw, currentFP := keyWithFingerprint(t, 0xEE)
	if oldFP == currentFP {
		t.Fatal("fixture keys collided")
	}
	withKeySources(t, sourceHolding("this machine's key store", currentRaw, currentFP))

	_, err := keyForFingerprint(oldFP)
	if err == nil {
		t.Fatal("a snapshot needing a rotated-away key resolved successfully")
	}
	if !strings.Contains(err.Error(), oldFP) {
		t.Fatalf("the refusal does not name the key the snapshot needs: %v", err)
	}
}

// A sanity check on the fixture itself: two seeds must not produce the same
// key, or several tests above would be asserting nothing.
func TestFixtureKeysAreDistinct(t *testing.T) {
	_, a := keyWithFingerprint(t, 0x01)
	_, b := keyWithFingerprint(t, 0x02)
	if a == b {
		t.Fatalf("two fixture keys share a fingerprint: %s", a)
	}
	if !strings.Contains(a, ":") {
		t.Fatalf("fingerprint %q is not in PBS's colon-separated form", a)
	}
	if _, err := fmt.Sscan(a); err != nil {
		t.Fatalf("fingerprint is unreadable: %v", err)
	}
}
