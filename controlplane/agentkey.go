package controlplane

import (
	"encoding/base64"
	"errors"
	"fmt"
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
// WHAT IS DELIBERATELY NOT HERE: challenge/prove/promote, the rotation
// ceremony. The server implements all three and docs/AGENT-API.md describes
// them fully, but this client performs none of them yet -- it registers one
// key and keeps it. Wire helpers for a ceremony nothing drives would be
// exactly the dead wiring this branch exists to remove, and the ceremony is
// not a set of calls anyway: it needs a store that holds two keys at once,
// because the server keeps sealing to the OLD one until promotion. That
// belongs in the change that builds it.

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
