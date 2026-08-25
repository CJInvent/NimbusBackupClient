package main

// Durable storage for the org's backup key (V4-SPEC §11, phase F item 1).
//
// THIS IS THE SAME DEK AND THE SAME PROTECTOR CHAIN AS gui/secrets.go, on
// purpose: §6 of the spec says extend that store, never stand up a second
// secret store beside it. A second store means a second protector-upgrade
// path, a second thing to migrate, and two answers to "what protects this
// machine's secrets".
//
// WHAT IS DELIBERATELY *NOT* SHARED IS THE FAILURE BEHAVIOUR, and that is the
// whole reason this file exists rather than a call to encryptSecret:
//
//   - encryptSecret FALLS BACK TO STORING PLAINTEXT when the DEK is
//     unavailable. Correct for a PBS API token — the worst case is that a user
//     re-enters a credential they still have. Catastrophic for a backup key:
//     the value that decrypts every snapshot would be written in the clear
//     next to the config, and nothing would fail.
//   - decryptSecret returns "" on any failure, so the caller cannot tell "no
//     key stored" from "the key is there and we could not open it". For a
//     token those collapse harmlessly into "ask the user again". For a backup
//     key they are opposite instructions: the first says fetch, the second
//     says REFUSE, because we cannot tell what is sitting in that file.
//
// So: every path here fails loudly, and a machine whose only protector is
// `plaintext` is refused a durable store outright. That machine is not stuck —
// it takes the ephemeral path (BackupKeyFetchEphemeral), which is exactly why
// the plaintext fallback is no longer needed for key material at all.
//
// THE ESCROW BLOB IS STORED ALONGSIDE THE KEY. It arrives only in the fetch
// response, and a durable agent fetches roughly twice in a machine's lifetime,
// so an agent that kept only the key would have nothing to write as
// rsa-encrypted.key.blob on any of the thousands of snapshots in between — the
// recovery path would be missing from almost every backup it protects. It is
// RSA-encrypted under the org's master PUBLIC key, so it is not secret and is
// stored as-is; sealing it would only make it depend on the DEK it is meant to
// outlive.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"controlplane"
)

// backupKeyFileName sits next to config.json and master.key.
const backupKeyFileName = "backup-key.json"

// storedBackupKey is the on-disk record. The key material is the ONLY sealed
// field; everything else is an identifier or already-encrypted ciphertext, and
// keeping them readable means a machine can report what it holds even when the
// DEK cannot be unwrapped.
type storedBackupKey struct {
	// Encryption is the org's answer as of the last check-in: encStateOn or
	// encStateOff. IT IS WRITTEN EVEN WHEN THERE IS NO KEY, and that is the
	// point — without a persisted "off" a machine that reboots during a
	// control-plane outage cannot tell an org that does not encrypt from an org
	// whose key it simply has not heard about yet. Those are opposite
	// instructions, and guessing either way is a silent downgrade or a stopped
	// backup.
	Encryption string `json:"encryption"`
	// EncryptionSource says WHO told us: encSourceCheckin (the control server,
	// authoritative) or encSourceProvisioning (a preconfigured MSI's profile,
	// provisional and unauthenticated).
	//
	// It decides nothing. It exists so a later contradiction is legible: a
	// machine that backed up in the clear because its profile said "off", on
	// an org that turns out to encrypt, has a real if narrow downgrade window,
	// and an operator reading the log should be able to see that it happened
	// rather than infer it. Empty means checkin, which is what every record
	// written before this field existed was.
	EncryptionSource string `json:"encryption_source,omitempty"`
	// Protector records which protector was in force when this was written, so
	// a key sealed under a DEK that has since been re-wrapped is still
	// explicable in a log line. Not used to decide anything — the DEK is the
	// same key across a protector upgrade.
	Protector string `json:"protector,omitempty"`
	KeyID     string `json:"key_id,omitempty"`
	Version   int    `json:"version,omitempty"`
	Scope     string `json:"scope,omitempty"`
	// Sealed is base64(nonce ‖ AES-256-GCM ciphertext) of the 32-byte key.
	Sealed string `json:"sealed"`
	// EscrowBlob is base64 of the server-produced rsa-encrypted.key.blob bytes.
	EscrowBlob        string `json:"escrow_blob,omitempty"`
	MasterFingerprint string `json:"master_fingerprint,omitempty"`
}

// The persisted encryption states. Deliberately three, not a bool: `unknown`
// (no record at all) is a real state with its own handling, and collapsing it
// into `off` is how a managed machine ends up backing up in the clear for an
// org that mandates encryption.
const (
	encStateUnknown = ""
	encStateOn      = "on"
	encStateOff     = "off"
)

// Where a recorded encryption answer came from. See storedBackupKey.
const (
	encSourceCheckin      = "checkin"
	encSourceProvisioning = "provisioning"
)

var backupKeyMu sync.Mutex

// errNoDurableStorage is returned when this machine has no protector worth
// writing a key under. It is not a failure — it is the condition that sends the
// machine down the ephemeral path — so callers must distinguish it.
var errNoDurableStorage = errors.New("no durable key protector on this machine (plaintext fallback only)")

func backupKeyPath() (string, error) {
	dir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, backupKeyFileName), nil
}

// dekSource is the DEK/protector pair this file builds on. A variable purely so
// tests can supply a real protector name: CI's Linux jobs have neither DPAPI nor
// a TPM, so getDEK() there always answers `plaintext` and every durable path
// below would be unreachable — the seal, the open, the key_id re-verification
// and the corrupt-record refusals would all sit untested behind a single early
// return. The Windows smoke job (S7) still runs the real chain through
// getDEK().
var dekSource = getDEK

// durableDEK returns the DEK only when it is wrapped by a protector a stolen
// disk cannot defeat. `plaintext` means the wrapping key lies beside the thing
// it wraps, which protects a backup key from nobody.
func durableDEK() ([]byte, string, error) {
	dek, protector, err := dekSource()
	if err != nil {
		return nil, "", err
	}
	if protectorRank(protector) <= 0 {
		return nil, protector, errNoDurableStorage
	}
	return dek, protector, nil
}

// backupKeyStorage reports what this machine can be trusted to keep, in the
// vocabulary the gate reads (controlplane.KeyStorage).
//
// THE THREE OUTCOMES ARE NOT INTERCHANGEABLE:
//
//   - Err set        — storage could not be CONSULTED. The gate refuses,
//     because a key may be sitting there and we cannot say
//     whether it is the right one.
//   - Durable false  — consulted, and weak. The gate fetches per run.
//   - Durable true   — StoredKeyID is what we actually hold ("" for nothing).
//
// A missing file is NOT an error: it is the ordinary state of a machine that
// has not fetched yet, and reporting it as unreadable storage would refuse
// every first backup.
func backupKeyStorage() controlplane.KeyStorage {
	backupKeyMu.Lock()
	defer backupKeyMu.Unlock()

	_, _, err := durableDEK()
	if errors.Is(err, errNoDurableStorage) {
		// Nothing is ever written here, so there is nothing to report holding.
		return controlplane.KeyStorage{Durable: false}
	}
	if err != nil {
		return controlplane.KeyStorage{Err: fmt.Errorf("master key unavailable: %w", err)}
	}

	rec, err := readBackupKeyRecord()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return controlplane.KeyStorage{Durable: true}
		}
		return controlplane.KeyStorage{Durable: true, Err: err}
	}
	return controlplane.KeyStorage{Durable: true, StoredKeyID: rec.KeyID}
}

func readBackupKeyRecord() (*storedBackupKey, error) {
	path, err := backupKeyPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is derived from getConfigDir, not user input
	if err != nil {
		return nil, err
	}
	var rec storedBackupKey
	if err := json.Unmarshal(data, &rec); err != nil {
		// Loud on purpose: a corrupt record is storage we cannot read, which
		// the gate must treat as a refusal rather than as "nothing stored".
		return nil, fmt.Errorf("stored backup key record is corrupt: %w", err)
	}
	switch rec.Encryption {
	case encStateOff:
		return &rec, nil
	case encStateOn:
		if rec.KeyID == "" || rec.Sealed == "" {
			return nil, fmt.Errorf("stored backup key record claims a key but carries none")
		}
		return &rec, nil
	default:
		return nil, fmt.Errorf("stored backup key record has an unknown encryption state %q", rec.Encryption)
	}
}

// persistedEncryption is what this machine last knew about the org's
// encryption setting, WITHOUT needing the DEK. Used only when the control plane
// has not been reached this process — see resolveBackupKeyForRun.
//
// Returns encStateUnknown with no error when nothing has ever been recorded;
// that is the ordinary state of a machine that has not checked in yet, not a
// storage fault.
func persistedEncryption() (state string, keyID string, err error) {
	state, keyID, _, err = persistedEncryptionWithSource()
	return
}

// persistedEncryptionWithSource is persistedEncryption plus who said so.
func persistedEncryptionWithSource() (state string, keyID string, source string, err error) {
	backupKeyMu.Lock()
	defer backupKeyMu.Unlock()
	rec, err := readBackupKeyRecord()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return encStateUnknown, "", "", nil
		}
		return encStateUnknown, "", "", err
	}
	src := rec.EncryptionSource
	if src == "" {
		// Records written before the field existed came from a check-in.
		src = encSourceCheckin
	}
	return rec.Encryption, rec.KeyID, src, nil
}

// seedEncryptionFromProvisioning records the org's encryption answer from a
// preconfigured MSI's profile, so a machine that has been imaged but has never
// reached its control server can still decide what to do.
//
// IT ONLY EVER FILLS A VOID. If anything has been recorded already — by a
// check-in or by an earlier profile — this does nothing and says so. A profile
// is unauthenticated and provisional; it may answer a question nobody has
// answered yet, and it may never overrule one that has been.
//
// state must be encStateOn or encStateOff; anything else is refused rather
// than written, because a third value here would put the machine back in the
// unknown state this exists to leave.
func seedEncryptionFromProvisioning(state string) (seeded bool, err error) {
	if state != encStateOn && state != encStateOff {
		return false, fmt.Errorf("refusing to seed an unknown encryption state %q", state)
	}

	backupKeyMu.Lock()
	defer backupKeyMu.Unlock()

	if _, err := readBackupKeyRecord(); err == nil {
		return false, nil // already answered; a profile does not overrule it
	} else if !errors.Is(err, os.ErrNotExist) {
		// Unreadable is NOT empty. Overwriting a record we could not parse
		// would destroy a sealed key, so refuse and let the caller report it.
		return false, err
	}

	path, err := backupKeyPath()
	if err != nil {
		return false, err
	}
	// NO KEY IS SEEDED, EVER, even for "on". The profile carries no material
	// and could not be trusted with any; "on" here means "expect to need a
	// key", which makes the machine fetch one — or refuse — rather than back
	// up in the clear.
	data, err := json.MarshalIndent(storedBackupKey{
		Encryption:       state,
		EncryptionSource: encSourceProvisioning,
	}, "", "  ")
	if err != nil {
		return false, err
	}
	if err := atomicWriteFile(path, data, 0600); err != nil {
		return false, fmt.Errorf("recording the provisioned encryption answer failed: %w", err)
	}
	return true, nil
}

// recordEncryptionOff writes down that the org does not encrypt, and drops any
// material we were holding.
//
// NO DEK IS REQUIRED. The marker is not secret, and requiring one would mean a
// machine with weak storage — exactly the machine that most needs to know it
// should stop trying to fetch a key — could never record the answer.
func recordEncryptionOff() error {
	backupKeyMu.Lock()
	defer backupKeyMu.Unlock()

	path, err := backupKeyPath()
	if err != nil {
		return err
	}
	if rec, err := readBackupKeyRecord(); err == nil && rec.Encryption == encStateOff &&
		rec.EncryptionSource != encSourceProvisioning {
		return nil // already recorded; do not rewrite the file every check-in
	}
	// A provisioning-seeded "off" IS rewritten, once, so the record stops
	// claiming a provisional source after the server has confirmed it. Without
	// that, a machine looks permanently un-confirmed and any future report
	// about provisional answers would be wrong about this one forever.
	data, err := json.MarshalIndent(storedBackupKey{
		Encryption:       encStateOff,
		EncryptionSource: encSourceCheckin,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("recording that encryption is disabled failed: %w", err)
	}
	return nil
}

// storeBackupKey seals delivered material and writes it. Every failure is
// returned; nothing degrades.
//
// The material is verified against its stated key_id BEFORE anything is
// written, so a truncated or mangled response never becomes the key this
// machine encrypts with.
func storeBackupKey(m *controlplane.BackupKeyMaterial) error {
	if m == nil {
		return fmt.Errorf("no backup key material to store")
	}
	raw, err := base64.StdEncoding.DecodeString(m.KeyB64)
	if err != nil {
		return fmt.Errorf("delivered backup key is not valid base64: %w", err)
	}
	if err := controlplane.VerifyKeyMaterial(raw, m.KeyID); err != nil {
		return err
	}
	escrow, err := base64.StdEncoding.DecodeString(m.EscrowBlobB64)
	if err != nil {
		return fmt.Errorf("delivered escrow blob is not valid base64: %w", err)
	}
	if len(escrow) == 0 {
		// A key with no escrow blob encrypts perfectly well and is
		// unrecoverable the day the control plane is gone — and it looks
		// entirely normal until then. The server computes this at issue time
		// precisely so it is never optional.
		return fmt.Errorf("delivered backup key carries no escrow blob")
	}

	backupKeyMu.Lock()
	defer backupKeyMu.Unlock()

	dek, protector, err := durableDEK()
	if err != nil {
		return fmt.Errorf("refusing to store the backup key: %w", err)
	}
	sealed, err := sealWithDEK(dek, raw)
	if err != nil {
		return err
	}

	path, err := backupKeyPath()
	if err != nil {
		return err
	}
	rec := storedBackupKey{
		Encryption:        encStateOn,
		Protector:         protector,
		KeyID:             m.KeyID,
		Version:           m.Version,
		Scope:             m.Scope,
		Sealed:            base64.StdEncoding.EncodeToString(sealed),
		EscrowBlob:        base64.StdEncoding.EncodeToString(escrow),
		MasterFingerprint: m.MasterFingerprint,
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing the backup key failed: %w", err)
	}
	// The GUI links no backup engine, so unlike the API token file this one has
	// no reason to be readable by INTERACTIVE. Best-effort: a failure here
	// leaves a 0600 file on a permissive parent, which is worth a warning and
	// not worth refusing a backup over.
	if err := restrictToServiceOnly(path); err != nil {
		writeWarnLog(fmt.Sprintf("[BackupKey] WARNING: could not restrict the backup key file's ACL: %v", err))
	}
	writeBackupLog(fmt.Sprintf("[BackupKey] stored backup key %s (scope %s, version %d) under the %s protector",
		shortKeyID(m.KeyID), rec.Scope, rec.Version, protector))
	return nil
}

// loadBackupKey opens the stored key. Returns the raw 32 bytes and the escrow
// blob.
//
// A failure to open is an ERROR, never an empty key: the caller's next move on
// "" would be to encrypt under nothing or to silently back up in the clear.
// The stored key_id is re-verified against the material, so a record whose
// identifier and bytes disagree is caught here rather than at restore time.
func loadBackupKey() (raw []byte, escrow []byte, keyID string, err error) {
	backupKeyMu.Lock()
	defer backupKeyMu.Unlock()

	dek, _, err := durableDEK()
	if err != nil {
		return nil, nil, "", fmt.Errorf("cannot open the stored backup key: %w", err)
	}
	rec, err := readBackupKeyRecord()
	if err != nil {
		return nil, nil, "", err
	}
	sealed, err := base64.StdEncoding.DecodeString(rec.Sealed)
	if err != nil {
		return nil, nil, "", fmt.Errorf("stored backup key is corrupt: %w", err)
	}
	raw, err = openWithDEK(dek, sealed)
	if err != nil {
		return nil, nil, "", err
	}
	if err := controlplane.VerifyKeyMaterial(raw, rec.KeyID); err != nil {
		return nil, nil, "", fmt.Errorf("stored backup key does not match its recorded key_id: %w", err)
	}
	escrow, err = base64.StdEncoding.DecodeString(rec.EscrowBlob)
	if err != nil {
		return nil, nil, "", fmt.Errorf("stored escrow blob is corrupt: %w", err)
	}
	if len(escrow) == 0 {
		return nil, nil, "", fmt.Errorf("stored backup key has no escrow blob")
	}
	return raw, escrow, rec.KeyID, nil
}

func sealWithDEK(dek, plain []byte) ([]byte, error) {
	gcm, err := gcmFor(dek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce generation failed: %w", err)
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

func openWithDEK(dek, sealed []byte) ([]byte, error) {
	gcm, err := gcmFor(dek)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, fmt.Errorf("stored backup key is truncated")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	if err != nil {
		// No detail from the AEAD: it distinguishes nothing useful and the
		// message is what an operator reads. What matters is that this is an
		// error and not an empty key.
		return nil, fmt.Errorf("the stored backup key could not be decrypted with this machine's master key")
	}
	return plain, nil
}

func gcmFor(dek []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("cipher init failed: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm init failed: %w", err)
	}
	return gcm, nil
}

// shortKeyID trims a key_id for a log line, matching controlplane's rendering
// so the same key reads the same on both sides of the wire.
func shortKeyID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12] + "…"
}
