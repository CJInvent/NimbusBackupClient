package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/nacl/box"
)

// This machine's X25519 identity, and the private half's custody (T4 --
// NimbusControl V4-SECURITY-ROADMAP §3, wire contract in its docs/AGENT-API.md).
//
// Every secret the control plane returns is sealed to the public key
// registered here. Without one, this agent authenticates perfectly well and
// receives NOTHING: each secret endpoint answers 409 until a key exists. So
// this is not a hardening extra to be added later -- it is the thing that
// makes the credential and backup-key endpoints work at all.
//
// TWO SLOTS, AND THAT IS THE WHOLE SHAPE OF THE FILE. During a rotation this
// machine legitimately holds two private halves, because the server keeps
// sealing to the OLD key until the new one is promoted -- so there is never an
// instant where the agent cannot open what it is sent. A store with one slot
// would force a choice between losing the ability to open in-flight payloads
// and registering a key it cannot yet use. Neither is acceptable, so it holds
// both and the opener picks (openSealedToAgent).
//
// SAME STORE, SAME PROTECTOR CHAIN as gui/secrets.go and the backup key. A
// second secret store means a second protector-upgrade path and two answers to
// "what protects this machine's secrets" (backupkey_store.go's opening
// comment, and it applies with equal force here).
//
// WHERE THIS DELIBERATELY DIFFERS FROM THE BACKUP KEY: the backup key refuses
// a weak protector outright and sends that machine down the ephemeral path,
// because a backup key written beside an unprotected DEK is the key to every
// snapshot. There is no ephemeral path for an IDENTITY. A machine that cannot
// store this private key across a restart cannot receive a secret after one,
// which is not a degraded mode, it is a machine that does not work. So this
// key is stored under whatever protector the machine has -- exactly like the
// control-plane secret in secrets.go, which is stored on the same terms and
// is worth as much to an attacker -- and the honest report of how weak that
// is goes to the server as credential_storage (§6, credential_storage.go).

const agentKeyFileName = "agent-key.json"

// storedAgentKey is one keypair on disk. The PUBLIC half is stored in the
// clear on purpose: it is not a secret, and having it beside the sealed
// private half is what lets a load VERIFY the pair rather than assume it.
type storedAgentKey struct {
	KeyID     string `json:"key_id"`
	Public    string `json:"public"`    // base64, 32 raw bytes
	Protector string `json:"protector"` // which protector sealed the private half
	Sealed    string `json:"sealed"`    // private half, sealed under the DEK
	CreatedAt string `json:"created_at"`
}

// agentKeyFile is the record: the key secrets are sealed to today, and the one
// being rotated in, if a rotation is under way.
//
// Pending is emptied by promotion, never by generating a replacement: a
// pending key that is registered server-side but forgotten here is a rotation
// nobody can finish (the challenge would be sealed to a key this machine no
// longer holds), and the server offers no way to abandon one.
type agentKeyFile struct {
	Active  *storedAgentKey `json:"active,omitempty"`
	Pending *storedAgentKey `json:"pending,omitempty"`
}

var agentKeyMu sync.Mutex

// errNoAgentKey means nothing is stored yet -- the ordinary state of a machine
// that has not registered.
var errNoAgentKey = errors.New("no agent key stored on this machine")

func agentKeyPath() (string, error) {
	dir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, agentKeyFileName), nil
}

// agentKeyID derives the key's name from the key itself: the date it was made
// plus eight hex of SHA-256 over the public bytes.
//
// DERIVED, not chosen, for two reasons. Nothing is filled in by hand anywhere
// else in this product, and more usefully: the server refuses "the same id
// with different bytes" as a rotation gone wrong, and an id that is a function
// of the bytes cannot produce that collision by accident. Re-registering the
// same key is then naturally idempotent on both sides.
func agentKeyID(pub []byte) string {
	sum := sha256.Sum256(pub)
	return time.Now().UTC().Format("2006-01-02") + "-" + hex.EncodeToString(sum[:4])
}

// loadOrCreateAgentKey returns the key secrets are sealed to today, generating
// one on first use.
//
// It never touches the pending slot. A machine mid-rotation still has exactly
// one key the server is sealing to, and that is this one.
func loadOrCreateAgentKey() (keyID string, pub []byte, err error) {
	agentKeyMu.Lock()
	defer agentKeyMu.Unlock()

	f, rerr := readAgentKeyFile()
	if rerr != nil && !errors.Is(rerr, errNoAgentKey) {
		return "", nil, rerr
	}
	if f.Active != nil {
		raw, perr := decodePublic(f.Active)
		if perr != nil {
			return "", nil, perr
		}
		return f.Active.KeyID, raw, nil
	}
	// No active key but a rotation under way: the pending key is the only
	// identity this machine has, and minting another would be the one
	// unrecoverable move available here -- the server permits a single
	// rotation in flight, so the replacement would be refused and this
	// machine would hold a key nobody can be told about.
	if f.Pending != nil {
		raw, perr := decodePublic(f.Pending)
		if perr != nil {
			return "", nil, perr
		}
		return f.Pending.KeyID, raw, nil
	}

	rec, pubBytes, gerr := generateStoredAgentKey()
	if gerr != nil {
		return "", nil, gerr
	}
	f.Active = rec
	if err := writeAgentKeyFile(f); err != nil {
		return "", nil, err
	}
	if err := verifyStoredPair(f.Active); err != nil {
		return "", nil, fmt.Errorf("this machine's key did not survive being written: %w", err)
	}
	writeBackupLog(fmt.Sprintf(
		"[AgentKey] created key %s under the %s protector (written, flushed, read back)",
		rec.KeyID, rec.Protector))
	return rec.KeyID, pubBytes, nil
}

// beginAgentKeyRotation returns the key being rotated IN, generating and
// storing one if no rotation is under way.
//
// RESUMES rather than restarts. A crash between registering a pending key and
// promoting it must not produce a second one: the server permits only one
// rotation in flight and would refuse the replacement, leaving a machine that
// holds a key the server has never heard of and cannot be told about.
func beginAgentKeyRotation() (keyID string, pub []byte, err error) {
	agentKeyMu.Lock()
	defer agentKeyMu.Unlock()

	f, rerr := readAgentKeyFile()
	if rerr != nil && !errors.Is(rerr, errNoAgentKey) {
		return "", nil, rerr
	}
	if f.Pending != nil {
		raw, perr := decodePublic(f.Pending)
		if perr != nil {
			return "", nil, perr
		}
		return f.Pending.KeyID, raw, nil
	}

	rec, pubBytes, gerr := generateStoredAgentKey()
	if gerr != nil {
		return "", nil, gerr
	}
	f.Pending = rec
	if err := writeAgentKeyFile(f); err != nil {
		return "", nil, err
	}
	if err := verifyStoredPair(f.Pending); err != nil {
		return "", nil, fmt.Errorf("the rotating key did not survive being written: %w", err)
	}
	writeBackupLog(fmt.Sprintf(
		"[AgentKey] rotation: created key %s under the %s protector (written, flushed, read back)",
		rec.KeyID, rec.Protector))
	return rec.KeyID, pubBytes, nil
}

// promoteLocalAgentKey moves the pending key into the active slot, once the
// SERVER has promoted it.
//
// Order matters and it is this way round on purpose: the server promotes
// first, then this. If the process dies in between, the next run finds a
// pending key the server calls active, asks to promote again, is told
// `promoted: false` (nothing in flight), and reconciles from that -- which is
// recoverable. The reverse order would drop the only key able to open payloads
// still being sealed to the old one.
func promoteLocalAgentKey(keyID string) error {
	agentKeyMu.Lock()
	defer agentKeyMu.Unlock()

	f, err := readAgentKeyFile()
	if err != nil {
		return err
	}
	if f.Pending == nil {
		if f.Active != nil && f.Active.KeyID == keyID {
			return nil // already promoted; a repeat of a lost response
		}
		return fmt.Errorf("no rotation is under way on this machine")
	}
	if f.Pending.KeyID != keyID {
		return fmt.Errorf("the server promoted key %s but this machine is rotating %s", keyID, f.Pending.KeyID)
	}

	retired := ""
	if f.Active != nil {
		retired = f.Active.KeyID
	}
	f.Active, f.Pending = f.Pending, nil
	if err := writeAgentKeyFile(f); err != nil {
		return err
	}
	writeBackupLog(fmt.Sprintf("[AgentKey] rotation complete: %s is now this machine's key (retired %s)",
		f.Active.KeyID, orNone(retired)))
	return nil
}

// markAgentKeyPending records the SERVER'S answer about which of our keys is
// in flight.
//
// A machine that has lost its key file registers a fresh key and is told
// `pending`, because the server still holds an active key for it -- one whose
// private half is gone. Locally that key was filed as active, and the ceremony
// reads the pending slot, so without this the rotation would generate a SECOND
// key, offer it, and be refused: one rotation in flight. The fix is not to
// guess but to believe the server, which is the only party that knows what it
// is sealing to.
//
// The active slot is left EMPTY by this, and that is accurate rather than
// lossy: whatever the server is sealing to, it is not a key this machine
// holds.
func markAgentKeyPending(keyID string) error {
	agentKeyMu.Lock()
	defer agentKeyMu.Unlock()

	f, err := readAgentKeyFile()
	if err != nil {
		return err
	}
	if f.Pending != nil && f.Pending.KeyID == keyID {
		return nil
	}
	if f.Pending != nil {
		return fmt.Errorf("this machine is already rotating %s; the server calls %s in flight",
			f.Pending.KeyID, keyID)
	}
	if f.Active == nil || f.Active.KeyID != keyID {
		return fmt.Errorf("key %s is not in this machine's record", keyID)
	}
	f.Pending, f.Active = f.Active, nil
	return writeAgentKeyFile(f)
}

// pendingAgentKeyID names the rotation under way, or "" for none.
func pendingAgentKeyID() string {
	agentKeyMu.Lock()
	defer agentKeyMu.Unlock()
	f, err := readAgentKeyFile()
	if err != nil || f.Pending == nil {
		return ""
	}
	return f.Pending.KeyID
}

// openSealedToAgent opens a payload the control plane sealed to this machine.
//
// sealedTo names which of our keys the server used; during a rotation that is
// the OLD one, by design. It is a HINT, not a requirement: an empty or unknown
// value falls back to trying every private half we hold, because failing to
// open a payload we could have opened is worse than one extra scalar
// multiplication.
//
// A failure is an ERROR and never an empty result: every caller's next move on
// "" would be to treat an unopened secret as an absent one -- back up in the
// clear, or store nothing and refetch forever.
func openSealedToAgent(sealed []byte, sealedTo string) ([]byte, error) {
	agentKeyMu.Lock()
	defer agentKeyMu.Unlock()

	f, err := readAgentKeyFile()
	if err != nil {
		return nil, err
	}

	// Named key first, then everything else we hold.
	var order []*storedAgentKey
	for _, k := range []*storedAgentKey{f.Active, f.Pending} {
		if k != nil && sealedTo != "" && k.KeyID == sealedTo {
			order = append([]*storedAgentKey{k}, order...)
		} else if k != nil {
			order = append(order, k)
		}
	}
	if len(order) == 0 {
		return nil, errNoAgentKey
	}

	held := make([]string, 0, len(order))
	for _, rec := range order {
		held = append(held, rec.KeyID)
		out, ok, oerr := openWithStoredKey(rec, sealed)
		if oerr != nil {
			return nil, oerr
		}
		if ok {
			return out, nil
		}
	}

	// Named, so an operator can tell "sealed to a key this machine no longer
	// has" from "this payload is damaged". The first is a rotation that went
	// wrong and is fixed by rotating again; the second is not.
	return nil, fmt.Errorf(
		"a payload sealed to key %s could not be opened by this machine (holding %v)",
		orNone(sealedTo), held)
}

// agentKeyStorageDetail is the attestation sent with a proof: what this code
// ACTUALLY did with the private half.
//
// Built from the record rather than from a constant, so it cannot keep
// claiming a TPM after a machine has fallen back to something weaker. Empty
// when there is nothing to attest to -- ProveKey refuses that rather than
// sending a claim nobody made.
func agentKeyStorageDetail(keyID string) string {
	agentKeyMu.Lock()
	defer agentKeyMu.Unlock()
	f, err := readAgentKeyFile()
	if err != nil {
		return ""
	}
	for _, rec := range []*storedAgentKey{f.Pending, f.Active} {
		if rec != nil && rec.KeyID == keyID {
			return fmt.Sprintf(
				"private key sealed under the %s protector, written, flushed and read back from %s",
				rec.Protector, agentKeyFileName)
		}
	}
	return ""
}

// ---------------------------------------------------------------- internals

func generateStoredAgentKey() (*storedAgentKey, []byte, error) {
	pubArr, privArr, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating this machine's key failed: %w", err)
	}
	dek, protector, derr := dekSource()
	if derr != nil {
		return nil, nil, fmt.Errorf("refusing to create an agent key without a master key: %w", derr)
	}
	sealed, serr := sealWithDEK(dek, privArr[:])
	if serr != nil {
		return nil, nil, serr
	}
	return &storedAgentKey{
		KeyID:     agentKeyID(pubArr[:]),
		Public:    base64.StdEncoding.EncodeToString(pubArr[:]),
		Protector: protector,
		Sealed:    base64.StdEncoding.EncodeToString(sealed),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}, pubArr[:], nil
}

// verifyStoredPair re-opens a slot FROM DISK and confirms the stored private
// half really is the private half of the stored public one.
//
// The check is a round trip through the actual primitive -- seal to the public
// key, open with the private key -- rather than a scalar-multiplication
// comparison. It is the same operation the server will perform, so it fails
// here on exactly what would fail there, and nothing is registered against a
// key this machine has not re-read.
func verifyStoredPair(expect *storedAgentKey) error {
	f, err := readAgentKeyFile()
	if err != nil {
		return err
	}
	var rec *storedAgentKey
	for _, k := range []*storedAgentKey{f.Active, f.Pending} {
		if k != nil && k.KeyID == expect.KeyID {
			rec = k
		}
	}
	if rec == nil {
		return fmt.Errorf("key %s is not in the record that was just written", expect.KeyID)
	}
	if rec.Public != expect.Public || rec.Sealed != expect.Sealed {
		return fmt.Errorf("key %s came back from disk different to what was written", expect.KeyID)
	}

	pub, err := decodePublic(rec)
	if err != nil {
		return err
	}
	probe := []byte("nimbus agent key round trip")
	var pubArr [32]byte
	copy(pubArr[:], pub)
	sealed, err := box.SealAnonymous(nil, probe, &pubArr, rand.Reader)
	if err != nil {
		return fmt.Errorf("sealing a probe to the stored key failed: %w", err)
	}
	out, ok, err := openWithStoredKey(rec, sealed)
	if err != nil {
		return err
	}
	if !ok || string(out) != string(probe) {
		return fmt.Errorf("the stored private half does not open what its public half seals")
	}
	return nil
}

// openWithStoredKey reports (plaintext, opened, error). `opened=false` with no
// error means this key is simply not the one it was sealed to -- an ordinary
// answer during a rotation, and NOT a storage fault. An error means the key
// could not be CONSULTED, which is.
func openWithStoredKey(rec *storedAgentKey, sealed []byte) ([]byte, bool, error) {
	pub, err := decodePublic(rec)
	if err != nil {
		return nil, false, err
	}
	priv, err := openStoredAgentPrivate(rec)
	if err != nil {
		return nil, false, err
	}
	var pubArr, privArr [32]byte
	copy(pubArr[:], pub)
	copy(privArr[:], priv)
	out, ok := box.OpenAnonymous(nil, sealed, &pubArr, &privArr)
	return out, ok, nil
}

func decodePublic(rec *storedAgentKey) ([]byte, error) {
	pub, err := base64.StdEncoding.DecodeString(rec.Public)
	if err != nil || len(pub) != 32 {
		return nil, fmt.Errorf("the stored agent key record is corrupt: public half is not 32 bytes")
	}
	return pub, nil
}

func readAgentKeyFile() (agentKeyFile, error) {
	var f agentKeyFile
	path, err := agentKeyPath()
	if err != nil {
		return f, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path derives from getConfigDir, not user input
	if errors.Is(err, os.ErrNotExist) {
		return f, errNoAgentKey
	}
	if err != nil {
		return f, fmt.Errorf("reading this machine's key failed: %w", err)
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("this machine's key record is not valid JSON: %w", err)
	}
	for _, k := range []*storedAgentKey{f.Active, f.Pending} {
		if k != nil && (k.KeyID == "" || k.Public == "" || k.Sealed == "") {
			return agentKeyFile{}, fmt.Errorf("this machine's key record is incomplete")
		}
	}
	if f.Active == nil && f.Pending == nil {
		return f, errNoAgentKey
	}
	return f, nil
}

func writeAgentKeyFile(f agentKeyFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	path, err := agentKeyPath()
	if err != nil {
		return err
	}
	if err := atomicWriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing this machine's key failed: %w", err)
	}
	if err := restrictToServiceOnly(path); err != nil {
		writeWarnLog(fmt.Sprintf("[AgentKey] WARNING: could not restrict the agent key file's ACL: %v", err))
	}
	return nil
}

func openStoredAgentPrivate(rec *storedAgentKey) ([]byte, error) {
	dek, _, err := dekSource()
	if err != nil {
		return nil, fmt.Errorf("cannot open this machine's key: %w", err)
	}
	sealed, err := base64.StdEncoding.DecodeString(rec.Sealed)
	if err != nil {
		return nil, fmt.Errorf("this machine's stored key is corrupt: %w", err)
	}
	priv, err := openWithDEK(dek, sealed)
	if err != nil {
		return nil, err
	}
	if len(priv) != 32 {
		return nil, fmt.Errorf("this machine's stored private key is not 32 bytes")
	}
	return priv, nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
