package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// The backup-key half of the wire contract (V4-SPEC §11, docs/AGENT-API.md).
//
// The server tells this agent WHICH key to hold; the material moves through a
// separate endpoint we call only when what we hold does not match. See
// BackupKeyDecision below for why that distinction carries the whole design.
//
// Everything here is pure and I/O-free, in the same style as breakglass.go and
// unmanaged.go, so the decision that stops a backup can be tested without a
// server, a registry or a Windows machine.

// BackupKeyAd is the `backup_key` block on a check-in — the IDENTIFIER and the
// PUBLIC escrow key, never the material.
//
// A nil pointer means the server sent null, which means encrypted backup is not
// enabled for this org. THAT IS A DIFFERENT STATE FROM "the key is unavailable"
// and the two must never be collapsed; see BackupKeyDecision.
type BackupKeyAd struct {
	ID      int64  `json:"id"`
	KeyID   string `json:"key_id"`
	Version int    `json:"version"`
	Scope   string `json:"scope"` // "org" | "agent"

	// MasterFingerprint / MasterPublicPEM identify the org's RSA master
	// keypair. The PUBLIC half only — it is the escrow primitive PBS uses for
	// rsa-encrypted.key.blob, and it rides along on every check-in so a master
	// rotation propagates without a separate fetch.
	MasterFingerprint string `json:"master_fingerprint"`
	MasterPublicPEM   string `json:"master_public_pem"`
	MasterVersion     int    `json:"master_version"`
}

// BackupKeyMaterial is the response from POST /api/agent/v1/backup-key.
//
// KeyB64 is base64 because JSON has no byte-string type and a raw AES key
// spliced into a JSON string is a mojibake bug waiting for the first key whose
// bytes are not valid UTF-8 — which is most of them.
type BackupKeyMaterial struct {
	KeyB64            string `json:"key"`
	KeyID             string `json:"key_id"`
	Version           int    `json:"version"`
	Scope             string `json:"scope"`
	EscrowBlobB64     string `json:"escrow_blob"`
	MasterFingerprint string `json:"master_fingerprint"`
	MasterPublicPEM   string `json:"master_public_pem"`
}

// KeyStorageStatus is the agent's verdict on its own secure storage. The set is
// closed because it drives a fleet-health display and eventually an alert; free
// text would make "how many machines are in trouble" unanswerable.
type KeyStorageStatus string

const (
	// KeyStorageOK: the key is stored, retrievable, and matches what the
	// server assigned.
	KeyStorageOK KeyStorageStatus = "ok"
	// KeyStorageUnavailable: secure storage itself failed — DPAPI refused, the
	// TPM was absent, the file could not be read. The key may well be correct;
	// we cannot prove it.
	KeyStorageUnavailable KeyStorageStatus = "unavailable"
	// KeyStorageMismatch: we hold A key and it is not the assigned one.
	KeyStorageMismatch KeyStorageStatus = "mismatch"
	// KeyStorageAbsent: nothing is stored. Normal before the first fetch.
	KeyStorageAbsent KeyStorageStatus = "absent"
)

// KeyStatusReport is POST /api/agent/v1/key-status.
type KeyStatusReport struct {
	Status KeyStorageStatus `json:"status"`
	KeyID  string           `json:"key_id,omitempty"`
	Detail string           `json:"detail,omitempty"`
}

// KeyStatusResponse — whether the server agrees we hold the right key.
type KeyStatusResponse struct {
	Matches bool `json:"matches"`
}

// BackupKeyAction is what the agent should do next.
type BackupKeyAction int

const (
	// BackupKeyProceedPlain: encryption is not enabled for this org. Back up
	// in the clear. This is a legitimate, configured state.
	BackupKeyProceedPlain BackupKeyAction = iota
	// BackupKeyProceedEncrypted: we hold the assigned key. Back up encrypted.
	BackupKeyProceedEncrypted
	// BackupKeyFetch: encryption is on and we do not hold the right key.
	// Fetch it, then decide again.
	BackupKeyFetch
	// BackupKeyRefuse: encryption is on, we cannot obtain or store the key,
	// and a backup MUST NOT START.
	BackupKeyRefuse
)

func (a BackupKeyAction) String() string {
	switch a {
	case BackupKeyProceedPlain:
		return "proceed-unencrypted"
	case BackupKeyProceedEncrypted:
		return "proceed-encrypted"
	case BackupKeyFetch:
		return "fetch-key"
	case BackupKeyRefuse:
		return "refuse"
	}
	return "unknown"
}

// BackupKeyDecision is the verification gate (V4-SPEC §11).
//
// THE ENTIRE POINT IS THAT "NOT ENCRYPTED" AND "COULD NOT ENCRYPT" ARE
// DIFFERENT ANSWERS. Backing up in the clear because a key was unavailable
// silently downgrades a customer who believes their data is encrypted, and on
// every dashboard it looks identical to a customer who chose not to encrypt.
// Nobody finds out until someone reads a snapshot they assumed was protected.
//
// So the refusal is deliberate and it is the SAFE direction here, which is not
// true everywhere in this codebase: restrict_unmanaged_backups defaults
// permissive precisely because a restrictive default silently STOPS backups.
// The difference is that a refusal is loud — it reports a status, surfaces a
// detail string and shows up as a failed run — whereas a silent downgrade is
// not observable at all.
//
//   - ad == nil                     -> proceed unencrypted (configured state)
//   - storage failed                -> REFUSE (we cannot prove what we hold)
//   - nothing stored, or wrong key  -> fetch
//   - stored key matches            -> proceed encrypted
//
// storedKeyID is what our secure storage reports holding ("" for nothing).
// storageErr is non-nil when storage itself could not be consulted — which is
// NOT the same as holding nothing, and is why they are separate parameters
// rather than one string.
//
// alreadyFetched says a fetch was already attempted in this cycle. Without it
// the caller can loop: fetch, still mismatch, fetch again. A second failure to
// arrive at the right key is a refusal, not another attempt.
func BackupKeyDecision(ad *BackupKeyAd, storedKeyID string, storageErr error, alreadyFetched bool) (BackupKeyAction, string) {
	if ad == nil {
		// Encryption is off for this org. Note that a storage error is NOT
		// consulted here: if the org does not encrypt, a broken key store is
		// not a reason to stop backing it up.
		return BackupKeyProceedPlain, "encryption is not enabled for this organization"
	}

	if strings.TrimSpace(ad.KeyID) == "" {
		// The server advertised a key block with no identifier. Refuse rather
		// than guess: we cannot verify anything we store against it, so
		// "encrypted" would be a claim we cannot check.
		return BackupKeyRefuse, "the server advertised a backup key with no key_id"
	}

	if storageErr != nil {
		return BackupKeyRefuse, fmt.Sprintf(
			"encrypted backup is required but this machine's secure key storage is unavailable: %v", storageErr)
	}

	if storedKeyID == ad.KeyID {
		return BackupKeyProceedEncrypted, ""
	}

	if alreadyFetched {
		// We asked and still do not have it. Distinguish the two reasons,
		// because they send an operator to different places: nothing stored
		// means the fetch or the write failed, while a different key stored
		// means the rotation did not take.
		if storedKeyID == "" {
			return BackupKeyRefuse, "encrypted backup is required but the backup key could not be obtained from the server"
		}
		return BackupKeyRefuse, fmt.Sprintf(
			"encrypted backup is required but this machine holds key %s while the server assigned %s",
			shortKeyID(storedKeyID), shortKeyID(ad.KeyID))
	}

	return BackupKeyFetch, ""
}

// KeyStatusFor turns the same inputs into the report we send the server.
//
// Derived from the same three inputs as the decision, deliberately: a status
// that could disagree with the action taken would make the fleet view a
// fiction. What the server does with it is compare key_id and record it — it
// cannot recompute any of this, because DPAPI, the TPM and the disk are facts
// only this machine has.
func KeyStatusFor(ad *BackupKeyAd, storedKeyID string, storageErr error) KeyStatusReport {
	switch {
	case storageErr != nil:
		return KeyStatusReport{
			Status: KeyStorageUnavailable,
			// No key_id: we do not know what we hold. Sending the last one we
			// remember would report a key we cannot actually produce.
			Detail: truncateDetail(storageErr.Error()),
		}
	case storedKeyID == "":
		return KeyStatusReport{Status: KeyStorageAbsent}
	case ad != nil && storedKeyID != ad.KeyID:
		return KeyStatusReport{
			Status: KeyStorageMismatch,
			KeyID:  storedKeyID,
			Detail: "stored key does not match the key assigned by the server",
		}
	default:
		return KeyStatusReport{Status: KeyStorageOK, KeyID: storedKeyID}
	}
}

// VerifyKeyMaterial checks that delivered bytes are the key they claim to be.
//
// key_id is sha256 of the raw key, so this costs nothing and is the difference
// between storing a key and storing whatever arrived. A truncated response, a
// proxy that mangled the base64, or a mixed-up rotation all produce material
// that fails here and would otherwise be written to disk and used to encrypt a
// backup nobody can read.
//
// NOT a security boundary — an attacker who could alter the response could
// alter the key_id with it. This catches corruption and mistakes, which are far
// more likely and just as destructive.
func VerifyKeyMaterial(raw []byte, claimedKeyID string) error {
	if len(raw) != 32 {
		return fmt.Errorf("backup key is %d bytes, expected 32", len(raw))
	}
	sum := sha256.Sum256(raw)
	got := hex.EncodeToString(sum[:])
	if got != strings.ToLower(strings.TrimSpace(claimedKeyID)) {
		return fmt.Errorf("delivered backup key does not match its stated key_id")
	}
	return nil
}

// shortKeyID trims a key_id for a human-facing message. Full 64-hex strings in
// a log line are unreadable, and the first bytes are enough to tell two apart.
func shortKeyID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12] + "…"
}

// truncateDetail bounds what we send as `detail`. The server caps this at 500
// and strips control characters; doing it here too keeps the field readable and
// avoids relying on the far side to make our message sensible.
func truncateDetail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return s[:400]
	}
	return s
}
