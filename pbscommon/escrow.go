package pbscommon

// The escrow blob: this snapshot's backup key, RSA-wrapped to the org's master
// public key, written beside the snapshot as rsa-encrypted.key.blob.
//
// It is the recovery path that outlives us. A customer holding their exported
// private master key decrypts this blob with stock proxmox-backup-client and
// gets the key that reads the snapshot — no Nimbus server, no database, no
// vault code anywhere in the path. Everything here exists to make sure that
// blob is present and correct on every encrypted snapshot, because every
// plausible mistake is invisible until the day somebody needs it.
//
// The bytes are produced by the SERVER at key-issue time and delivered with
// the key; this client never performs the RSA operation and never holds a
// master key.

import "errors"

// SetEscrowBlob records the escrow bytes for the key set by SetCryptKey.
//
// STORED ON THE KEY, NOT ON THE CLIENT. The blob is the same secret in another
// envelope, so it belongs with the key rather than as a second field on
// PBSClient that only means anything when `crypt` is set. The alternative
// considered — threading the bytes through
// backupDirectory/backupReal/uploadWorker as a parameter — would put the
// escrow blob and the key it escrows in different places, which is how the two
// come to disagree.
//
// Refuses an empty blob, and refuses an unencrypted client: both would produce
// a snapshot that looks recoverable and is not.
func (pbs *PBSClient) SetEscrowBlob(escrow []byte) error {
	if pbs.crypt == nil {
		return errors.New("refusing to hold an escrow blob for an unencrypted client")
	}
	if len(escrow) == 0 {
		return errors.New("escrow blob is empty: refusing to record a recovery path that does not work")
	}
	// Copied, not aliased: the caller's slice is a decoded buffer it may reuse,
	// and the recovery path must not depend on what it does next.
	pbs.crypt.escrowBlob = append([]byte(nil), escrow...)
	return nil
}

// EscrowBlob returns the recorded escrow bytes, or nil when this client is
// unencrypted or nothing has been recorded.
func (pbs *PBSClient) EscrowBlob() []byte {
	if pbs.crypt == nil {
		return nil
	}
	return pbs.crypt.escrowBlob
}

// UploadEscrowBlobIfEncrypted writes rsa-encrypted.key.blob when this client is
// encrypting, and does nothing when it is not.
//
// THE CALL SITES ARE THE TWO ENGINES' PER-SESSION FINALIZERS, immediately
// before UploadManifest, and this wrapper exists so both make the same two
// decisions the same way rather than each writing its own `if`.
//
// An encrypted snapshot whose escrow blob is missing is recoverable only
// through our control plane — the single dependency the escrow design exists
// to remove — so a failure here FAILS THE BACKUP rather than warning the way
// the ACL and status sidecars beside it do.
func (pbs *PBSClient) UploadEscrowBlobIfEncrypted() error {
	if pbs.crypt == nil {
		return nil
	}
	if len(pbs.crypt.escrowBlob) == 0 {
		return errors.New("this backup is encrypted but no escrow blob was provided: " +
			"the snapshot would be unrecoverable without the control server")
	}
	return pbs.UploadEscrowBlob(pbs.crypt.escrowBlob)
}
