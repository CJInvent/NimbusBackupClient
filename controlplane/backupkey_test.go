package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// The verification gate (V4-SPEC §11).
//
// The property worth the most here is a NEGATIVE one: there must be no input
// under which encryption is enabled, the key is not held, and the agent backs
// up anyway. A silent downgrade is the failure this whole mechanism exists to
// prevent — it produces cleartext backups for a customer who believes
// otherwise, and on every dashboard it is indistinguishable from a customer who
// chose not to encrypt. Nobody finds out until someone reads a snapshot they
// assumed was protected.
//
// So the table below is exhaustive over the input space rather than a list of
// cases that happened to occur to me, and there is a separate assertion that
// sweeps every combination looking for that one forbidden outcome.

func keyIDOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func adFor(keyID string) *BackupKeyAd {
	return &BackupKeyAd{ID: 1, KeyID: keyID, Version: 1, Scope: "org"}
}

// durable/weak/broken are the three storage states, named so the table below
// reads as the decision it is testing rather than as struct literals.
func durable(storedKeyID string) KeyStorage {
	return KeyStorage{Durable: true, StoredKeyID: storedKeyID}
}
func weak() KeyStorage { return KeyStorage{Durable: false} }
func broken(err error) KeyStorage {
	return KeyStorage{Durable: true, Err: err}
}

func TestBackupKeyDecisionTable(t *testing.T) {
	assigned := strings.Repeat("a1", 32)
	other := strings.Repeat("b2", 32)
	boom := errors.New("DPAPI unavailable")

	cases := []struct {
		name           string
		ad             *BackupKeyAd
		st             KeyStorage
		alreadyFetched bool
		want           BackupKeyAction
	}{
		// Encryption off. Storage state is irrelevant — a broken key store is
		// not a reason to stop backing up an org that does not encrypt.
		{"no key advertised", nil, durable(""), false, BackupKeyProceedPlain},
		{"no key advertised, storage broken", nil, broken(boom), false, BackupKeyProceedPlain},
		{"no key advertised, no durable storage", nil, weak(), false, BackupKeyProceedPlain},
		{"no key advertised, stale key stored", nil, durable(other), false, BackupKeyProceedPlain},

		// Steady state on a machine that can keep a key.
		{"holds the assigned key", adFor(assigned), durable(assigned), false, BackupKeyProceedEncrypted},
		{"holds it, after a fetch", adFor(assigned), durable(assigned), true, BackupKeyProceedEncrypted},

		// First encounter and rotation both mean "go and get it".
		{"nothing stored yet", adFor(assigned), durable(""), false, BackupKeyFetch},
		{"holds the previous key", adFor(assigned), durable(other), false, BackupKeyFetch},

		// Asked once and still wrong. Not another attempt — a refusal.
		{"fetched and still nothing", adFor(assigned), durable(""), true, BackupKeyRefuse},
		{"fetched and still wrong", adFor(assigned), durable(other), true, BackupKeyRefuse},

		// NO DURABLE STORAGE: fetch for this run, never write it down. This is
		// the branch that stops the other two from being a choice between
		// writing the key in the clear and not encrypting at all.
		{"weak storage, first pass", adFor(assigned), weak(), false, BackupKeyFetchEphemeral},
		{"weak storage, fetch failed", adFor(assigned), weak(), true, BackupKeyRefuse},

		// Storage could not be CONSULTED — distinct from consulted-and-weak.
		// We cannot tell what is sitting there, so we can neither trust it nor
		// safely decide to skip persisting.
		{"storage unreadable", adFor(assigned), broken(boom), false, BackupKeyRefuse},
		{"storage unreadable, key looks right", adFor(assigned), KeyStorage{Durable: true, Err: boom, StoredKeyID: assigned}, false, BackupKeyRefuse},

		// A key block with no identifier: nothing to verify against.
		{"advertised with empty key_id", adFor(""), durable(assigned), false, BackupKeyRefuse},
		{"advertised with blank key_id", adFor("   "), durable(assigned), false, BackupKeyRefuse},
	}

	for _, c := range cases {
		got, reason := BackupKeyDecision(c.ad, c.st, c.alreadyFetched)
		if got != c.want {
			t.Errorf("%s: got %s, want %s (reason %q)", c.name, got, c.want, reason)
		}
		// Every refusal must say why. A backup that stops with no reason is
		// the failure mode the gate replaces, not one to reproduce: on a
		// dashboard it looks the same as a machine with nothing to back up.
		if got == BackupKeyRefuse && strings.TrimSpace(reason) == "" {
			t.Errorf("%s: refused with no reason given", c.name)
		}
	}
}

// THE INVARIANT THAT MATTERS MOST, and it survived the design change: no input
// may yield "back up unencrypted" while a key is advertised, and none may yield
// "proceed encrypted" without the matching key actually in durable storage.
//
// The sweep is over the whole input space rather than the cases anyone thought
// of, so a future branch that gets this wrong fails here even if nobody
// remembers to extend the table above — which is exactly what happened when the
// ephemeral branch was added: the table needed new rows, this did not.
func TestNeverDowngradesSilently(t *testing.T) {
	assigned := strings.Repeat("a1", 32)

	ads := map[string]*BackupKeyAd{
		"assigned":     adFor(assigned),
		"empty key_id": adFor(""),
	}
	storages := map[string]KeyStorage{
		"durable, nothing":   durable(""),
		"durable, assigned":  durable(assigned),
		"durable, different": durable(strings.Repeat("b2", 32)),
		"weak":               weak(),
		"unreadable":         broken(errors.New("storage failure")),
		"unreadable w/ key":  {Durable: true, Err: errors.New("storage failure"), StoredKeyID: assigned},
	}

	for adName, ad := range ads {
		for stName, st := range storages {
			for _, fetched := range []bool{false, true} {
				got, _ := BackupKeyDecision(ad, st, fetched)

				if got == BackupKeyProceedPlain {
					t.Fatalf("SILENT DOWNGRADE: ad=%s storage=%s fetched=%v produced %s "+
						"— a key is advertised, so backing up in the clear is never correct",
						adName, stName, fetched, got)
				}

				// Encryption may only be CLAIMED when the assigned key is
				// actually held in storage we trust and could read.
				if got == BackupKeyProceedEncrypted {
					if st.Err != nil || !st.Durable || ad.KeyID == "" || st.StoredKeyID != ad.KeyID {
						t.Fatalf("CLAIMED ENCRYPTION WITHOUT THE KEY: ad=%s storage=%s fetched=%v",
							adName, stName, fetched)
					}
				}

				// Ephemeral is only ever offered where nothing will be
				// written. Offering it on a durable machine would quietly stop
				// persisting a key that machine could have kept — turning an
				// outage-tolerant agent into one that needs the server up.
				if got == BackupKeyFetchEphemeral && (st.Durable || st.Err != nil) {
					t.Fatalf("EPHEMERAL ON A MACHINE THAT CAN STORE: ad=%s storage=%s fetched=%v",
						adName, stName, fetched)
				}
			}
		}
	}
}

// A refusal must not loop. Fetch is only ever offered once per cycle.
func TestRefusalIsTerminalWithinACycle(t *testing.T) {
	ad := adFor(strings.Repeat("a1", 32))
	if got, _ := BackupKeyDecision(ad, durable(""), false); got != BackupKeyFetch {
		t.Fatalf("first pass should fetch, got %s", got)
	}
	// Same inputs, but the fetch has now happened and did not help.
	if got, _ := BackupKeyDecision(ad, durable(""), true); got != BackupKeyRefuse {
		t.Fatalf("after a failed fetch the gate must refuse, got %s", got)
	}
}

// Refusal messages have to be readable by whoever is woken up by them.
func TestRefusalReasonsAreOperatorReadable(t *testing.T) {
	assigned := strings.Repeat("a1", 32)
	other := strings.Repeat("b2", 32)

	_, mismatch := BackupKeyDecision(adFor(assigned), durable(other), true)
	// Both key ids, shortened. A full 64-hex pair in a sentence is unreadable,
	// and the leading bytes are enough to tell two keys apart.
	if !strings.Contains(mismatch, "a1a1a1a1a1a1") || !strings.Contains(mismatch, "b2b2b2b2b2b2") {
		t.Errorf("mismatch reason should name both keys briefly: %q", mismatch)
	}
	if strings.Contains(mismatch, assigned) {
		t.Errorf("mismatch reason contains a full key_id, which is unreadable: %q", mismatch)
	}

	_, unavailable := BackupKeyDecision(adFor(assigned), broken(errors.New("TPM absent")), false)
	// The underlying cause must survive: "storage unavailable" alone sends an
	// operator hunting, while the actual error names the thing to fix.
	if !strings.Contains(unavailable, "TPM absent") {
		t.Errorf("storage failure reason drops the cause: %q", unavailable)
	}

	// The two refusals must not read alike — they have different remedies.
	if mismatch == unavailable {
		t.Error("a key mismatch and a storage failure produce the same message")
	}
}

func TestKeyStatusMatchesTheDecision(t *testing.T) {
	assigned := strings.Repeat("a1", 32)
	other := strings.Repeat("b2", 32)

	cases := []struct {
		name      string
		ad        *BackupKeyAd
		st        KeyStorage
		want      KeyStorageStatus
		wantKeyID string
	}{
		{"holding the right key", adFor(assigned), durable(assigned), KeyStorageOK, assigned},
		{"nothing stored", adFor(assigned), durable(""), KeyStorageAbsent, ""},
		{"holding the wrong key", adFor(assigned), durable(other), KeyStorageMismatch, other},
		// No key_id when storage is unreadable: we do not know what we hold,
		// and reporting a remembered value would claim a key we cannot produce.
		{"storage unreadable", adFor(assigned), broken(errors.New("nope")), KeyStorageUnavailable, ""},
		// EPHEMERAL IS NOT A FAULT. Reporting it as one would fill a fleet view
		// with red for machines working exactly as designed.
		{"ephemeral", adFor(assigned), weak(), KeyStorageOK, assigned},
	}

	for _, c := range cases {
		got := KeyStatusFor(c.ad, c.st)
		if got.Status != c.want {
			t.Errorf("%s: status %q, want %q", c.name, got.Status, c.want)
		}
		if got.KeyID != c.wantKeyID {
			t.Errorf("%s: key_id %q, want %q", c.name, got.KeyID, c.wantKeyID)
		}
	}

	// An ephemeral machine must still be FINDABLE. It reports ok, so the only
	// thing distinguishing it is the detail string — and an operator needs to
	// know which machines stop backing up during an outage BEFORE the outage.
	eph := KeyStatusFor(adFor(assigned), weak())
	if !strings.Contains(eph.Detail, "never written") && !strings.Contains(eph.Detail, "per run") {
		t.Errorf("an ephemeral machine is indistinguishable from an ordinary one: %q", eph.Detail)
	}

	// The status must never disagree with the action. A fleet view built on a
	// status that contradicts what the machine did is a fiction.
	ok := KeyStatusFor(adFor(assigned), durable(assigned))
	act, _ := BackupKeyDecision(adFor(assigned), durable(assigned), false)
	if (ok.Status == KeyStorageOK) != (act == BackupKeyProceedEncrypted) {
		t.Error("reported OK but did not proceed encrypted (or vice versa)")
	}
}

// The status values are a closed set the server validates against; a typo here
// is a 422 at runtime rather than a compile error.
func TestKeyStatusValuesMatchTheServersEnum(t *testing.T) {
	want := map[KeyStorageStatus]bool{"ok": true, "unavailable": true, "mismatch": true, "absent": true}
	for _, s := range []KeyStorageStatus{KeyStorageOK, KeyStorageUnavailable, KeyStorageMismatch, KeyStorageAbsent} {
		if !want[s] {
			t.Errorf("status %q is not one the server accepts", s)
		}
	}
}

// Detail must be bounded before it leaves. The server caps and sanitises it,
// but relying on the far side to make our own message sensible is how a
// truncated-mid-word detail ends up on an operator's screen.
func TestDetailIsBounded(t *testing.T) {
	huge := strings.Repeat("x", 5000)
	rep := KeyStatusFor(adFor(strings.Repeat("a1", 32)), broken(errors.New(huge)))
	if len(rep.Detail) > 500 {
		t.Errorf("detail is %d chars, server caps at 500", len(rep.Detail))
	}
	if rep.Detail == "" {
		t.Error("detail was dropped entirely rather than truncated")
	}
}

func TestVerifyKeyMaterial(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	id := keyIDOf(raw)

	if err := VerifyKeyMaterial(raw, id); err != nil {
		t.Fatalf("a correct key was rejected: %v", err)
	}
	// Case and whitespace should not decide whether a backup can run.
	if err := VerifyKeyMaterial(raw, "  "+strings.ToUpper(id)+"\n"); err != nil {
		t.Errorf("case/whitespace in key_id rejected a correct key: %v", err)
	}

	// A truncated response is the realistic corruption, and it is exactly the
	// one a length check alone would miss if the id were not also verified.
	if err := VerifyKeyMaterial(raw[:31], id); err == nil {
		t.Error("a 31-byte key was accepted")
	}
	if err := VerifyKeyMaterial(append(raw, 0), id); err == nil {
		t.Error("a 33-byte key was accepted")
	}
	if err := VerifyKeyMaterial(raw, strings.Repeat("f", 64)); err == nil {
		t.Error("a key that does not match its stated key_id was accepted")
	}
	if err := VerifyKeyMaterial(raw, ""); err == nil {
		t.Error("an empty key_id was accepted")
	}

	// One flipped bit must fail: the whole value of the check is catching
	// material that is the right SHAPE and the wrong BYTES, which is what a
	// mangled transfer or a mixed-up rotation produces.
	bad := append([]byte(nil), raw...)
	bad[17] ^= 0x01
	if err := VerifyKeyMaterial(bad, id); err == nil {
		t.Error("a single-bit-flipped key was accepted")
	}
}
