package controlplane

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// The agent-key half of the wire contract (T4, NimbusControl
// V4-SECURITY-ROADMAP §3 and its docs/AGENT-API.md).
//
// WHY THIS EXISTS AT ALL. Every secret the control plane returns -- the PBS
// token, backup key material -- is sealed to this agent's X25519 public key
// with crypto_box_seal, so TLS is defense in depth rather than the only thing
// standing between a customer's PBS credential and whoever terminates the
// connection. The sealing is ANONYMOUS: it needs only our public key and
// generates an ephemeral keypair per message, so there is no server keypair to
// protect and nothing shared to rotate on both sides. The server cannot
// decrypt what it just sealed.
//
// REGISTERING IS PART OF ENROLLING, not an optimization. Every secret endpoint
// answers 409 until a key is registered, so an agent that skips this can
// authenticate perfectly well and obtain nothing.
//
// This file is wire types and calls only. The keypair itself, its storage and
// the opening of sealed payloads live in the gui package, next to the DEK and
// protector chain that has to hold the private half -- and keeping them there
// is what lets this module stay dependency-free.
//
// THE ROTATION CEREMONY IS FOUR CALLS AND THEY ARE NOT INTERCHANGEABLE:
// register a new key (it arrives `pending`), take a challenge sealed to it,
// prove BOTH that we can open that challenge and that the private half is
// durably stored, then promote. The server seals secrets to the OLD key for
// the whole of it, including after `proven`, so there is never an instant
// where the agent cannot open what it is sent -- which is also why the client
// has to hold two private halves at once (gui/agentkey_store.go).

// Key states, as the server names them.
const (
	// KeyStatePending: registered, in flight, not yet proven either way.
	KeyStatePending = "pending"
	// KeyStateProven: the agent answered a challenge with this key AND
	// attested that the private half is durably stored.
	KeyStateProven = "proven"
	// KeyStateActive: what secrets are sealed to. Exactly one per agent.
	KeyStateActive = "active"
	// KeyStateRetired: superseded. The id can never be reused.
	KeyStateRetired = "retired"
)

// ErrKeyRequired is what a secret endpoint answers with until this agent has
// registered a public key. It is a state to FIX by registering, not a failure
// to report and retry, so it is worth telling apart from every other 409.
var ErrKeyRequired = errors.New("controlplane: this machine has no registered public key")

// ErrRotationInFlight: the server already has a pending key for this agent.
// Finish or abandon that rotation; a second one is refused by a partial unique
// index rather than by code, so this cannot be raced past.
var ErrRotationInFlight = errors.New("controlplane: a key rotation is already in flight")

// KeyRegisterRequest is POST /api/agent/v1/keys/register.
//
// PublicKey is base64 of exactly 32 raw X25519 bytes -- not PEM, not a
// certificate. The server checks the length before anything else.
type KeyRegisterRequest struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
}

// KeyRegisterResponse — the first key comes back `active` (there is nothing to
// rotate from, and demanding a ceremony at enrollment would leave a fresh
// agent unable to receive anything); every later key comes back `pending`.
type KeyRegisterResponse struct {
	KeyID string `json:"key_id"`
	State string `json:"state"`
}

// KeyChallengeResponse -- a 32-byte nonce sealed to the IN-FLIGHT key.
//
// Spendable once. Re-issuing replaces the previous nonce, so an agent that
// retries cannot answer an older one, and a WRONG answer spends the challenge
// rather than allowing a second guess: ask for a new one.
type KeyChallengeResponse struct {
	KeyID  string `json:"key_id"`
	Sealed string `json:"sealed"`
}

// KeyProveRequest carries BOTH directions of proof, because one-sided proof is
// how an agent gets bricked.
//
// Nonce is the opened challenge: it proves we hold the new private half AND
// that the server's sealing works against this exact public key -- a
// well-formed but wrong key fails here rather than on the first real payload.
//
// StorageDetail is our attestation that the private key was written, flushed
// and READ BACK. The server cannot verify another machine's fsync; what it can
// do is refuse to retire the old key until we have claimed this, so a client
// that skips the read-back has to lie deliberately rather than merely forget.
// It is REQUIRED and must not be empty -- an optional field defaulting to ""
// is not a claim, and the server refuses one (it used to accept it).
type KeyProveRequest struct {
	KeyID         string `json:"key_id"`
	Nonce         string `json:"nonce"`
	StorageDetail string `json:"storage_detail"`
}

// KeyProveResponse -- `proven` once both directions are in.
type KeyProveResponse struct {
	KeyID string `json:"key_id"`
	State string `json:"state"`
}

// KeyPromoteResponse -- Promoted false means nothing was in flight, which is
// the ordinary answer for an agent retrying after a lost response, not an
// error.
type KeyPromoteResponse struct {
	Promoted    bool   `json:"promoted"`
	ActiveKeyID string `json:"active_key_id"`
}

// RegisterKey publishes a public key for this agent.
//
// Idempotent by design on the server: the same id with the same bytes returns
// the existing row rather than starting a second rotation, which is what makes
// "register on every start" a safe thing for a client to do. The same id with
// DIFFERENT bytes is refused, and so is a retired id -- both are conditions a
// caller should surface rather than retry.
func (c *Client) RegisterKey(keyID string, publicKey []byte) (*KeyRegisterResponse, error) {
	if len(publicKey) != 32 {
		return nil, fmt.Errorf("controlplane: an X25519 public key is exactly 32 bytes, got %d", len(publicKey))
	}
	var out KeyRegisterResponse
	err := c.post("/api/agent/v1/keys/register", KeyRegisterRequest{
		KeyID:     keyID,
		PublicKey: base64.StdEncoding.EncodeToString(publicKey),
	}, &out, true)
	if err != nil {
		if he := (*httpError)(nil); asHTTPError(err, &he) && he.status == 409 {
			return nil, fmt.Errorf("%w: %s", ErrRotationInFlight, he.msg)
		}
		return nil, err
	}
	return &out, nil
}

// KeyChallenge asks for a nonce sealed to the in-flight key.
//
// Returns the raw sealed bytes: opening them needs the private half, which
// this module deliberately never sees.
func (c *Client) KeyChallenge() (keyID string, sealed []byte, err error) {
	var out KeyChallengeResponse
	if err := c.post("/api/agent/v1/keys/challenge", struct{}{}, &out, true); err != nil {
		return "", nil, err
	}
	raw, derr := base64.StdEncoding.DecodeString(out.Sealed)
	if derr != nil {
		return "", nil, fmt.Errorf("controlplane: the challenge is not valid base64: %w", derr)
	}
	if len(raw) == 0 {
		return "", nil, fmt.Errorf("controlplane: the challenge carried no sealed bytes")
	}
	return out.KeyID, raw, nil
}

// ProveKey answers the challenge and attests to durable storage.
//
// storageDetail must describe what was ACTUALLY done -- written, flushed, read
// back -- because it is the only part of this exchange the server has to take
// on trust, and the old key is retired on the strength of it. An empty one is
// refused here rather than sent: a client that has nothing to say about its
// own storage has no business retiring the key it can still open.
func (c *Client) ProveKey(keyID string, nonce []byte, storageDetail string) (*KeyProveResponse, error) {
	if len(nonce) == 0 {
		return nil, fmt.Errorf("controlplane: refusing to prove a key with an empty nonce")
	}
	if strings.TrimSpace(storageDetail) == "" {
		return nil, fmt.Errorf("controlplane: refusing to attest to storage with an empty description")
	}
	var out KeyProveResponse
	if err := c.post("/api/agent/v1/keys/prove", KeyProveRequest{
		KeyID:         keyID,
		Nonce:         base64.StdEncoding.EncodeToString(nonce),
		StorageDetail: storageDetail,
	}, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// PromoteKey retires the old key and activates the proven one, server-side, in
// ONE transaction. Safe to repeat: a crash leaves exactly the state it started
// from and the sequence resumes by calling this again.
func (c *Client) PromoteKey() (*KeyPromoteResponse, error) {
	var out KeyPromoteResponse
	if err := c.post("/api/agent/v1/keys/promote", struct{}{}, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// asKeyRequired translates the 409 every secret endpoint answers with before a
// key exists into ErrKeyRequired, and leaves every other error alone.
//
// The server's message is what distinguishes it: 409 on those endpoints means
// exactly one thing, but the status alone would also match a rotation
// conflict elsewhere, and a caller that treated "register first" as "retry
// later" would wait forever for a state that only its own action can reach.
func asKeyRequired(err error) error {
	if err == nil {
		return nil
	}
	if he := (*httpError)(nil); asHTTPError(err, &he) && he.status == 409 {
		return fmt.Errorf("%w: %s", ErrKeyRequired, he.msg)
	}
	return err
}
