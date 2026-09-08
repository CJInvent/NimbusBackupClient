package main

import (
	"errors"
	"fmt"
	"sync"

	"controlplane"
)

// Registering this machine's public key with the control plane (T4).
//
// REGISTERING IS PART OF ENROLLING. Every secret endpoint answers 409 until a
// key exists, so an agent that never registers authenticates perfectly well,
// checks in, takes commands -- and cannot obtain a PBS credential or a backup
// key. That failure has no obvious symptom at the point it happens, which is
// why the registration is not left to whoever remembers to call it: the two
// paths that fetch secrets ensure it themselves, and treat "no registered key"
// as a state to fix rather than an error to report.

var (
	agentKeyRegMu      sync.Mutex
	agentKeyRegistered bool
)

// ensureAgentKeyRegistered publishes this machine's public key, once per
// process.
//
// Cheap to call and safe to repeat: the server is idempotent for the same id
// and bytes (it returns the existing row rather than starting a second
// rotation), and the in-memory flag keeps the ordinary case from spending a
// request on every check-in for a fleet's worth of machines.
//
// The flag lives in memory rather than on disk on purpose. What is durable is
// the KEY; whether this particular server has already been told about it is
// not a fact worth persisting, and a stale "already registered" on disk --
// after a restore onto a rebuilt control plane, say -- would be a machine that
// never registers again and can never be told why.
// LOCK ORDER: agentKeyRegMu (this file) then agentKeyMu (the store), never
// the reverse. The rotation this can call reaches the store repeatedly and
// never reaches back here.
func ensureAgentKeyRegistered(c *controlplane.Client) error {
	if c == nil {
		return errors.New("no control server is configured on this machine")
	}

	agentKeyRegMu.Lock()
	defer agentKeyRegMu.Unlock()
	if agentKeyRegistered {
		return nil
	}

	keyID, pub, err := loadOrCreateAgentKey()
	if err != nil {
		return fmt.Errorf("this machine has no usable key to register: %w", err)
	}

	resp, err := c.RegisterKey(keyID, pub)
	if err != nil {
		if errors.Is(err, controlplane.ErrRotationInFlight) {
			// A rotation is already under way. Finish it rather than report a
			// conflict: the machine cannot receive a secret until one key is
			// active and it holds that key's private half.
			return rotateAgentKey(c, "a rotation was already in flight")
		}
		return fmt.Errorf("registering this machine's key failed: %w", err)
	}

	// THE STATE IS THE INSTRUCTION. `active` means this key is what secrets
	// are sealed to and there is nothing more to do. `pending` means the
	// server already holds a DIFFERENT active key for this machine -- it is
	// sealing to that one, and this machine cannot open it. That happens for
	// ordinary reasons (a lost key file, a re-created DEK, an image restored
	// from before the key existed) and before the ceremony existed it left an
	// agent silently unable to receive anything.
	if resp.State != controlplane.KeyStateActive {
		writeWarnLog(fmt.Sprintf(
			"[AgentKey] key %s registered as %s — this server holds another active key for this machine; rotating",
			resp.KeyID, resp.State))
		// File it where the server says it is before driving the ceremony:
		// the key we just registered is the in-flight one, whatever slot it
		// started in.
		if err := markAgentKeyPending(resp.KeyID); err != nil {
			return fmt.Errorf("this machine cannot record key %s as in flight: %w", resp.KeyID, err)
		}
		if err := rotateAgentKey(c, "registration returned "+resp.State); err != nil {
			return fmt.Errorf("this machine's key is registered but not active: %w", err)
		}
		agentKeyRegistered = true
		return nil
	}

	agentKeyRegistered = true
	writeBackupLog(fmt.Sprintf("[AgentKey] registered key %s with the control plane (%s)",
		resp.KeyID, resp.State))
	return nil
}

// forgetAgentKeyRegistration drops the in-memory belief that this server
// already knows our key.
//
// Called when a secret endpoint answers ErrKeyRequired, which means the server
// disagrees with that belief -- the honest reading is that the server is right
// and we are stale (a rebuilt control plane, a restored database, a machine
// re-adopted into a different one).
func forgetAgentKeyRegistration() {
	agentKeyRegMu.Lock()
	agentKeyRegistered = false
	agentKeyRegMu.Unlock()
}

// withAgentKey runs a call that needs a registered key, registering first and
// -- if the server says otherwise anyway -- registering again and retrying it
// exactly once.
//
// ONCE, not in a loop. A second ErrKeyRequired after a successful registration
// is not a race this client can win by trying harder; it is a disagreement
// worth surfacing to whoever reads the log.
func withAgentKey[T any](c *controlplane.Client, do func() (T, error)) (T, error) {
	var zero T
	if err := ensureAgentKeyRegistered(c); err != nil {
		return zero, err
	}
	out, err := do()
	if !errors.Is(err, controlplane.ErrKeyRequired) {
		return out, err
	}

	writeWarnLog("[AgentKey] the control plane has no public key for this machine; registering again")
	forgetAgentKeyRegistration()
	if rerr := ensureAgentKeyRegistered(c); rerr != nil {
		return zero, rerr
	}
	return do()
}
