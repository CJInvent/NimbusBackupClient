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

// storedAgentKey is the on-disk record. The PUBLIC half is stored in the clear
// on purpose: it is not a secret, and having it beside the sealed private half
// is what lets a load VERIFY the pair rather than assume it.
type storedAgentKey struct {
	KeyID     string `json:"key_id"`
	Public    string `json:"public"`    // base64, 32 raw bytes
	Protector string `json:"protector"` // which protector sealed the private half
	Sealed    string `json:"sealed"`    // private half, sealed under the DEK
	CreatedAt string `json:"created_at"`
}

var agentKeyMu sync.Mutex

// errNoAgentKey means nothing is stored yet -- the ordinary state of a machine
// that has not registered. Distinct from a record that exists and cannot be
// opened, which is a machine whose identity is LOST and which has to rotate
// rather than re-create.
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

// loadOrCreateAgentKey returns this machine's key id and public half,
// generating and storing a keypair on first use.
//
// The write path is write -> flush -> READ BACK AND VERIFY. The read-back is
// not ceremony: it is the difference between an agent that holds a key and an
// agent that will discover at its next reboot that it does not -- by which
// time the server has been sealing secrets to it. A key that cannot be read
// back fails HERE, before anything has been registered against it. (It is
// also the claim the rotation ceremony's storage attestation will make, when
// that is built; the server retires an old key on the strength of it.)
func loadOrCreateAgentKey() (keyID string, pub []byte, err error) {
	agentKeyMu.Lock()
	defer agentKeyMu.Unlock()

	if rec, rerr := readAgentKeyRecord(); rerr == nil {
		pub, perr := base64.StdEncoding.DecodeString(rec.Public)
		if perr != nil || len(pub) != 32 {
			return "", nil, fmt.Errorf("the stored agent key record is corrupt: public half is not 32 bytes")
		}
		return rec.KeyID, pub, nil
	} else if !errors.Is(rerr, errNoAgentKey) {
		return "", nil, rerr
	}

	pubArr, privArr, gerr := box.GenerateKey(rand.Reader)
	if gerr != nil {
		return "", nil, fmt.Errorf("generating this machine's key failed: %w", gerr)
	}

	dek, protector, derr := dekSource()
	if derr != nil {
		return "", nil, fmt.Errorf("refusing to create an agent key without a master key: %w", derr)
	}
	sealed, serr := sealWithDEK(dek, privArr[:])
	if serr != nil {
		return "", nil, serr
	}

	rec := storedAgentKey{
		KeyID:     agentKeyID(pubArr[:]),
		Public:    base64.StdEncoding.EncodeToString(pubArr[:]),
		Protector: protector,
		Sealed:    base64.StdEncoding.EncodeToString(sealed),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, merr := json.MarshalIndent(rec, "", "  ")
	if merr != nil {
		return "", nil, merr
	}
	path, perr := agentKeyPath()
	if perr != nil {
		return "", nil, perr
	}
	if err := atomicWriteFile(path, data, 0600); err != nil {
		return "", nil, fmt.Errorf("writing this machine's key failed: %w", err)
	}
	if err := restrictToServiceOnly(path); err != nil {
		writeWarnLog(fmt.Sprintf("[AgentKey] WARNING: could not restrict the agent key file's ACL: %v", err))
	}

	// Read it back from disk and prove the pair. Nothing is registered against
	// a key we have not re-opened: a key the server seals to and this machine
	// cannot open is an agent that receives nothing and cannot say why.
	if err := verifyStoredAgentKey(pubArr[:]); err != nil {
		return "", nil, fmt.Errorf("this machine's key did not survive being written: %w", err)
	}

	writeBackupLog(fmt.Sprintf(
		"[AgentKey] created key %s under the %s protector (written, flushed, read back)",
		rec.KeyID, protector))
	return rec.KeyID, pubArr[:], nil
}

// verifyStoredAgentKey re-opens the record from disk and confirms the stored
// private half really is the private half of the stored public one.
//
// The check is a round trip through the actual primitive -- seal to the public
// key, open with the private key -- rather than a scalar-multiplication
// comparison. It is the same operation the server will perform, so it fails
// here on exactly what would fail there.
func verifyStoredAgentKey(expectPub []byte) error {
	rec, err := readAgentKeyRecord()
	if err != nil {
		return err
	}
	pub, err := base64.StdEncoding.DecodeString(rec.Public)
	if err != nil || len(pub) != 32 {
		return fmt.Errorf("public half is not 32 bytes")
	}
	if expectPub != nil && string(pub) != string(expectPub) {
		return fmt.Errorf("the stored public key is not the one just generated")
	}
	priv, err := openStoredAgentPrivate(rec)
	if err != nil {
		return err
	}

	var pubArr, privArr [32]byte
	copy(pubArr[:], pub)
	copy(privArr[:], priv)

	probe := []byte("nimbus agent key round trip")
	sealed, err := box.SealAnonymous(nil, probe, &pubArr, rand.Reader)
	if err != nil {
		return fmt.Errorf("sealing a probe to the stored key failed: %w", err)
	}
	opened, ok := box.OpenAnonymous(nil, sealed, &pubArr, &privArr)
	if !ok || string(opened) != string(probe) {
		return fmt.Errorf("the stored private half does not open what its public half seals")
	}
	return nil
}

// OpenSealedToAgent opens a payload the control plane sealed to this machine.
//
// A failure is an ERROR and never an empty result: every caller's next move on
// "" would be to treat an unopened secret as an absent one -- back up in the
// clear, or store nothing and refetch forever.
func openSealedToAgent(sealed []byte) ([]byte, error) {
	agentKeyMu.Lock()
	defer agentKeyMu.Unlock()

	rec, err := readAgentKeyRecord()
	if err != nil {
		return nil, err
	}
	pub, err := base64.StdEncoding.DecodeString(rec.Public)
	if err != nil || len(pub) != 32 {
		return nil, fmt.Errorf("the stored agent key record is corrupt: public half is not 32 bytes")
	}
	priv, err := openStoredAgentPrivate(rec)
	if err != nil {
		return nil, err
	}

	var pubArr, privArr [32]byte
	copy(pubArr[:], pub)
	copy(privArr[:], priv)

	out, ok := box.OpenAnonymous(nil, sealed, &pubArr, &privArr)
	if !ok {
		// The payload was sealed to a DIFFERENT key of ours, or is damaged.
		// Both are worth the same words to an operator, and the recovery for
		// both is the same: register the key this machine actually holds.
		return nil, fmt.Errorf(
			"a payload sealed to key %s could not be opened by this machine's stored key", rec.KeyID)
	}
	return out, nil
}

func readAgentKeyRecord() (storedAgentKey, error) {
	var rec storedAgentKey
	path, err := agentKeyPath()
	if err != nil {
		return rec, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return rec, errNoAgentKey
	}
	if err != nil {
		return rec, fmt.Errorf("reading this machine's key failed: %w", err)
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("this machine's key record is not valid JSON: %w", err)
	}
	if rec.KeyID == "" || rec.Public == "" || rec.Sealed == "" {
		return rec, fmt.Errorf("this machine's key record is incomplete")
	}
	return rec, nil
}

func openStoredAgentPrivate(rec storedAgentKey) ([]byte, error) {
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
