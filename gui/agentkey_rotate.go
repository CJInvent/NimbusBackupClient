package main

import (
	"errors"
	"fmt"

	"controlplane"
)

// The rotation ceremony, client side (T4 -- NimbusControl
// V4-SECURITY-ROADMAP §3, and its docs/AGENT-API.md).
//
// WHAT TRIGGERS IT IS NOT A TIMER. The server answers `keys/register` with the
// state it assigned: the FIRST key for an agent comes back `active`, and every
// later one comes back `pending`. So an agent that registers and is told
// `pending` has just learned something specific and actionable -- this server
// already holds an active key for this machine, it is sealing secrets to that
// key, and this machine cannot open them. That is the whole trigger, and it is
// a state a machine reaches for ordinary reasons: its key file was lost with a
// rebuilt profile, or its DEK was re-created so the private half no longer
// opens, or it was restored from an image taken before the key existed.
//
// Before this existed, such a machine was bricked in a way nothing reported:
// it authenticated, checked in, took commands, and every secret endpoint
// handed it a payload sealed to a key it did not have.
//
// THE ORDER IS THE SAFETY PROPERTY. register (pending) -> challenge sealed to
// the pending key -> prove we opened it AND that it is durably stored ->
// promote. The server seals secrets to the OLD key for the entire ceremony,
// including after `proven`, so there is never an instant where the agent
// cannot open what it is sent -- provided it holds both halves, which is what
// the two-slot store exists for.
//
// EVERY STEP IS RESUMABLE, because every step is idempotent server-side and
// the pending key is on disk before it is registered. A crash anywhere leaves
// a state the next attempt walks forward from rather than a state that needs
// unpicking.

// ErrRotationNotOurs is the one dead end. The server has a rotation in flight
// for a key this machine does not hold, so it cannot answer the challenge:
// the nonce is sealed to a public key whose private half is gone. There is no
// "abandon rotation" endpoint by design (one in flight, enforced by a partial
// unique index), so this needs a person -- and saying so plainly beats
// retrying something that cannot succeed.
var ErrRotationNotOurs = errors.New(
	"the control plane has a key rotation in flight for a key this machine does not hold")

// rotateAgentKey runs the ceremony to completion, or reports why it stopped.
//
// reason is logged, not sent: the server records what happened from its own
// side, and a client-supplied explanation of why it rotated is exactly the
// sort of unverifiable field that reads as evidence later.
func rotateAgentKey(c *controlplane.Client, reason string) error {
	if c == nil {
		return errors.New("no control server is configured on this machine")
	}

	keyID, pub, err := beginAgentKeyRotation()
	if err != nil {
		return fmt.Errorf("this machine has no usable key to rotate to: %w", err)
	}
	writeBackupLog(fmt.Sprintf("[AgentKey] rotating to key %s (%s)", keyID, reason))

	// Registering the same id and bytes again is a retry server-side, not a
	// second rotation, so a resumed ceremony passes through here harmlessly.
	if _, err := c.RegisterKey(keyID, pub); err != nil {
		if errors.Is(err, controlplane.ErrRotationInFlight) {
			// A rotation exists and it is not the key we just offered. We
			// cannot open a challenge sealed to somebody else's public half.
			return fmt.Errorf("%w (this machine holds %s)", ErrRotationNotOurs, keyID)
		}
		return fmt.Errorf("registering the rotating key failed: %w", err)
	}

	challengeFor, sealed, err := c.KeyChallenge()
	if err != nil {
		return fmt.Errorf("requesting a challenge failed: %w", err)
	}
	if challengeFor != keyID {
		// The server is challenging a different in-flight key than the one we
		// just registered. Refuse rather than try to open it: a nonce we
		// cannot open would SPEND the challenge, and a wrong answer spends it
		// too, so guessing here costs the one thing we need.
		return fmt.Errorf("%w (it is challenging %s, this machine holds %s)",
			ErrRotationNotOurs, challengeFor, keyID)
	}

	nonce, err := openSealedToAgent(sealed, keyID)
	if err != nil {
		return fmt.Errorf("the challenge could not be opened with the rotating key: %w", err)
	}

	detail := agentKeyStorageDetail(keyID)
	if detail == "" {
		// Not a formality. The server retires the old key on the strength of
		// this claim, and a claim we cannot substantiate from our own record
		// is one we must not make.
		return fmt.Errorf("refusing to attest to storage for key %s: it is not in this machine's record", keyID)
	}

	proved, err := c.ProveKey(keyID, nonce, detail)
	if err != nil {
		return fmt.Errorf("proving the rotating key failed: %w", err)
	}
	if proved.State != controlplane.KeyStateProven {
		return fmt.Errorf("the server left key %s in state %q rather than proven", keyID, proved.State)
	}

	promoted, err := c.PromoteKey()
	if err != nil {
		return fmt.Errorf("promoting the rotating key failed: %w", err)
	}
	if !promoted.Promoted {
		// Nothing was in flight. Either a previous attempt promoted it and
		// lost the response, or something else finished the rotation. Both are
		// reconciled the same way: believe the server about which key is
		// active.
		if promoted.ActiveKeyID != keyID {
			return fmt.Errorf("the server reports %s active while this machine rotated to %s",
				orNone(promoted.ActiveKeyID), keyID)
		}
	}

	if err := promoteLocalAgentKey(keyID); err != nil {
		// The server has already promoted. Leaving the local record behind is
		// recoverable -- the next run promotes again and is told there is
		// nothing in flight -- but it must be reported, because until it is
		// fixed this machine is opening payloads with a key it thinks is
		// pending.
		return fmt.Errorf("the server promoted key %s but this machine could not record it: %w", keyID, err)
	}
	return nil
}
