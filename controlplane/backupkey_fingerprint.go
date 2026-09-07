package controlplane

// Asking the server for the key a SNAPSHOT needs (V4-SPEC §18, phase G).
//
// /backup-key answers "what should I encrypt with now" and serves the ACTIVE
// key only. That is right for backup and half of what restore needs: the
// moment a key is rotated, every snapshot older than the rotation needs a key
// this machine no longer holds and the server will no longer issue. The data
// is intact, the key is in the vault, and until this endpoint existed nothing
// could put the two together.
//
// THE QUESTION IS ASKED IN PBS's VOCABULARY, not ours. A snapshot's manifest
// carries `unprotected.key-fingerprint`, which is PBS's identifier for a key
// and is derived from the key material itself; our `key_id` is a different
// derivation for a different job and appears in no snapshot anywhere. So the
// agent asks by fingerprint, and the server derives the same value to answer
// (NimbusControl `Vault\BackupKeys::pbsFingerprint`). The two derivations are
// pinned against one shared vector in both repos, because each is
// self-consistent and a shared mistake would surface only as "no source could
// supply the key" on a machine that is holding it.

import "errors"

// BackupKeyByFingerprintRequest is the body of
// POST /api/agent/v1/backup-key/for-fingerprint.
//
// No mode and no run uuid, unlike BackupKeyRequest: there is exactly one
// reason to call this and the server records it as a `restore` release without
// being told. A field the client could set to something else would be a field
// an attacker could set to something else.
type BackupKeyByFingerprintRequest struct {
	// Fingerprint is PBS's colon-separated lowercase hex. Bare hex is accepted
	// by the server too — upstream's own parser strips separators — but the
	// colon form is what a manifest contains, so it is what we send.
	Fingerprint string `json:"fingerprint"`
}

// ErrNoSuchKey is a 404 from the fingerprint endpoint: this org holds no key
// with that name.
//
// A DISTINCT ERROR BECAUSE IT IS A DISTINCT ANSWER. "The server does not have
// this key" is an ordinary outcome — the snapshot may belong to another
// tenant, or predate this server — and the restore should carry on to the next
// key source. "The server could not be reached" must not be quietly treated
// the same way, or an outage would read as a missing key and send an operator
// hunting through the vault for something that is sitting there.
var ErrNoSuchKey = errors.New("the management server holds no key with that fingerprint")

// FetchBackupKeyByFingerprint retrieves the key a snapshot was encrypted with.
//
// It can return a RETIRED key — that is the point — and says which
// (Status). Nothing in the restore path depends on that field; it is there so
// a support log can distinguish "this restore reached back past a rotation,
// as designed" from "this machine has somehow fallen behind".
func (c *Client) FetchBackupKeyByFingerprint(fingerprint string) (*BackupKeyMaterialForRestore, error) {
	if fingerprint == "" {
		return nil, errors.New("a key fingerprint is required")
	}
	var out BackupKeyMaterialForRestore
	err := c.post("/api/agent/v1/backup-key/for-fingerprint",
		BackupKeyByFingerprintRequest{Fingerprint: fingerprint}, &out, true)
	if err != nil {
		if he := (*httpError)(nil); asHTTPError(err, &he) && he.status == 404 {
			return nil, ErrNoSuchKey
		}
		return nil, asKeyRequired(err)
	}
	return &out, nil
}

// BackupKeyMaterialForRestore is the fingerprint endpoint's response: the same
// shape as BackupKeyMaterial plus the key's status.
//
// A separate type rather than a field added to BackupKeyMaterial, because the
// two endpoints answer different questions and only this one can hand back a
// key that must never be used for a backup. A shared struct with a
// sometimes-populated Status is one refactor away from a caller reading it as
// "active" because the field was empty.
type BackupKeyMaterialForRestore struct {
	BackupKeyMaterial
	// Status is "active" or "retired", as the server holds it.
	Status string `json:"status"`
}
