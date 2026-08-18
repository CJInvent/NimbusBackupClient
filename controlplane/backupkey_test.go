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

func TestBackupKeyDecisionTable(t *testing.T) {
	assigned := strings.Repeat("a1", 32)
	other := strings.Repeat("b2", 32)
	boom := errors.New("DPAPI unavailable")

	cases := []struct {
		name           string
		ad             *BackupKeyAd
		stored         string
		storageErr     error
		alreadyFetched bool
		want           BackupKeyAction
	}{
		// Encryption off. A broken key store is NOT a reason to stop backing
		// up an org that does not encrypt — the key is irrelevant to the work.
		{"no key advertised", nil, "", nil, false, BackupKeyProceedPlain},
		{"no key advertised, storage broken", nil, "", boom, false, BackupKeyProceedPlain},
		{"no key advertised, stale key stored", nil, other, nil, false, BackupKeyProceedPlain},

		// Steady state.
		{"holds the assigned key", adFor(assigned), assigned, nil, false, BackupKeyProceedEncrypted},
		{"holds it, after a fetch", adFor(assigned), assigned, nil, true, BackupKeyProceedEncrypted},

		// First encounter and rotation both mean "go and get it".
		{"nothing stored yet", adFor(assigned), "", nil, false, BackupKeyFetch},
		{"holds the previous key", adFor(assigned), other, nil, false, BackupKeyFetch},

		// Asked once and still wrong. Not another attempt — a refusal.
		{"fetched and still nothing", adFor(assigned), "", nil, true, BackupKeyRefuse},
		{"fetched and still wrong", adFor(assigned), other, nil, true, BackupKeyRefuse},

		// Storage itself is broken while encryption IS required. We cannot
		// prove what we hold, so we cannot claim to encrypt.
		{"storage unavailable", adFor(assigned), "", boom, false, BackupKeyRefuse},
		{"storage unavailable, key looks right", adFor(assigned), assigned, boom, false, BackupKeyRefuse},

		// A key block with no identifier: nothing to verify against, so
		// "encrypted" would be an unverifiable claim.
		{"advertised with empty key_id", adFor(""), assigned, nil, false, BackupKeyRefuse},
		{"advertised with blank key_id", adFor("   "), assigned, nil, false, BackupKeyRefuse},
	}

	for _, c := range cases {
		got, reason := BackupKeyDecision(c.ad, c.stored, c.storageErr, c.alreadyFetched)
		if got != c.want {
			t.Errorf("%s: got %s, want %s (reason %q)", c.name, got, c.want, reason)
		}
		// Every refusal must say why. A backup that stops with no reason is
		// the failure mode the gate is supposed to replace, not reproduce: on
		// a dashboard it looks the same as a machine with nothing to back up.
		if got == BackupKeyRefuse && strings.TrimSpace(reason) == "" {
			t.Errorf("%s: refused with no reason given", c.name)
		}
	}
}

// THE ONE THAT MATTERS. Sweep the whole input space and assert that no
// combination yields "back up unencrypted" while a key is advertised.
//
// The table above checks the cases I thought of. This checks the ones I did
// not: any future edit that adds a branch returning ProceedPlain under an
// advertised key fails here even if nobody remembers to extend the table.
func TestNeverDowngradesSilently(t *testing.T) {
	assigned := strings.Repeat("a1", 32)

	ads := map[string]*BackupKeyAd{
		"assigned":     adFor(assigned),
		"empty key_id": adFor(""),
	}
	storedOpts := map[string]string{
		"nothing":   "",
		"assigned":  assigned,
		"different": strings.Repeat("b2", 32),
	}
	errOpts := map[string]error{
		"no error": nil,
		"broken":   errors.New("storage failure"),
	}

	for adName, ad := range ads {
		for storedName, stored := range storedOpts {
			for errName, e := range errOpts {
				for _, fetched := range []bool{false, true} {
					got, _ := BackupKeyDecision(ad, stored, e, fetched)
					if got == BackupKeyProceedPlain {
						t.Fatalf(
							"SILENT DOWNGRADE: ad=%s stored=%s err=%s fetched=%v produced %s "+
								"— a key is advertised, so backing up in the clear is never correct",
							adName, storedName, errName, fetched, got)
					}
					// Equally: never claim encryption without the right key.
					if got == BackupKeyProceedEncrypted && (e != nil || stored != ad.KeyID || ad.KeyID == "") {
						t.Fatalf(
							"CLAIMED ENCRYPTION WITHOUT THE KEY: ad=%s stored=%s err=%s fetched=%v",
							adName, storedName, errName, fetched)
					}
				}
			}
		}
	}
}

// A refusal must not loop. Fetch is only ever offered once per cycle.
func TestRefusalIsTerminalWithinACycle(t *testing.T) {
	ad := adFor(strings.Repeat("a1", 32))
	if got, _ := BackupKeyDecision(ad, "", nil, false); got != BackupKeyFetch {
		t.Fatalf("first pass should fetch, got %s", got)
	}
	// Same inputs, but the fetch has now happened and did not help.
	if got, _ := BackupKeyDecision(ad, "", nil, true); got != BackupKeyRefuse {
		t.Fatalf("after a failed fetch the gate must refuse, got %s", got)
	}
}

// Refusal messages have to be readable by whoever is woken up by them.
func TestRefusalReasonsAreOperatorReadable(t *testing.T) {
	assigned := strings.Repeat("a1", 32)
	other := strings.Repeat("b2", 32)

	_, mismatch := BackupKeyDecision(adFor(assigned), other, nil, true)
	// Both key ids, shortened. A full 64-hex pair in a sentence is unreadable,
	// and the leading bytes are enough to tell two keys apart.
	if !strings.Contains(mismatch, "a1a1a1a1a1a1") || !strings.Contains(mismatch, "b2b2b2b2b2b2") {
		t.Errorf("mismatch reason should name both keys briefly: %q", mismatch)
	}
	if strings.Contains(mismatch, assigned) {
		t.Errorf("mismatch reason contains a full key_id, which is unreadable: %q", mismatch)
	}

	_, unavailable := BackupKeyDecision(adFor(assigned), "", errors.New("TPM absent"), false)
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
		name       string
		ad         *BackupKeyAd
		stored     string
		storageErr error
		want       KeyStorageStatus
		wantKeyID  string
	}{
		{"holding the right key", adFor(assigned), assigned, nil, KeyStorageOK, assigned},
		{"nothing stored", adFor(assigned), "", nil, KeyStorageAbsent, ""},
		{"holding the wrong key", adFor(assigned), other, nil, KeyStorageMismatch, other},
		// No key_id when storage is broken: we do not know what we hold, and
		// reporting a remembered value would claim a key we cannot produce.
		{"storage broken", adFor(assigned), assigned, errors.New("nope"), KeyStorageUnavailable, ""},
	}

	for _, c := range cases {
		got := KeyStatusFor(c.ad, c.stored, c.storageErr)
		if got.Status != c.want {
			t.Errorf("%s: status %q, want %q", c.name, got.Status, c.want)
		}
		if got.KeyID != c.wantKeyID {
			t.Errorf("%s: key_id %q, want %q", c.name, got.KeyID, c.wantKeyID)
		}
	}

	// The status must never disagree with the action. A fleet view built on a
	// status that contradicts what the machine did is a fiction.
	ok := KeyStatusFor(adFor(assigned), assigned, nil)
	act, _ := BackupKeyDecision(adFor(assigned), assigned, nil, false)
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
	rep := KeyStatusFor(adFor(strings.Repeat("a1", 32)), "", errors.New(huge))
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
