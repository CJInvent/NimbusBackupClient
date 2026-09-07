package main

// The ephemeral key holder (V4-SPEC §11, phase F item 2): fetch, hold for the
// run, drop.
//
// This is the mode for a machine whose only DEK protector is `plaintext` —
// where writing the key down would put the thing that decrypts every backup
// beside the config it is supposed to protect. Such a machine keeps encryption
// and gives up outage tolerance instead: the key is fetched per run and never
// touches the disk.
//
// BE HONEST ABOUT WHAT "RAM ONLY" BUYS. In Go on Windows it is weaker than the
// phrase suggests, and customer-facing material must not imply otherwise:
//
//   - the garbage collector copies values, so a []byte can leave behind copies
//     no code holds a reference to and nothing can find to overwrite;
//   - there is no reliable zeroing — a wipe on the slice we hold does not reach
//     those copies, which is why this file does not perform one and pretend;
//   - the pagefile, a crash dump and hibernation all write process memory to
//     the very disk the key was kept off.
//
// So it defends a stolen DISK and a stolen MACHINE, and it does not defend a
// live box against someone who can read process memory. That is still a real
// improvement over a key file next to the config, and it is the honest ceiling.
//
// THERE IS NO HOLDER OBJECT, and that is the design. An earlier shape kept a
// package-level cache of the current ephemeral key with a "drop it when the run
// ends" hook — which is a key that outlives its run whenever the hook does not
// fire (a panic, a cancelled context, an engine that returns down a path nobody
// updated). The lifetime is instead the pipeline's local variable: the key
// exists as an argument to one backup and becomes unreachable when that call
// returns, with nothing to remember to clean up.

import (
	"controlplane"
	"encoding/base64"
	"errors"
	"fmt"
)

// ephemeralKeyFromMaterial turns a delivery into key bytes for exactly one run.
//
// Identical verification to the durable path, deliberately: a truncated or
// mangled response encrypts a backup nobody can read whether or not it was
// written to disk first, and the ephemeral path is the one where nothing on
// disk will ever reveal the mistake afterwards.
func ephemeralKeyFromMaterial(m *controlplane.BackupKeyMaterial) (key []byte, escrow []byte, err error) {
	if m == nil {
		return nil, nil, errors.New("no backup key material was delivered")
	}
	raw, err := openDeliveredKey(m)
	if err != nil {
		return nil, nil, err
	}
	if err := controlplane.VerifyKeyMaterial(raw, m.KeyID); err != nil {
		return nil, nil, err
	}
	blob, err := base64.StdEncoding.DecodeString(m.EscrowBlobB64)
	if err != nil {
		return nil, nil, fmt.Errorf("delivered escrow blob is not valid base64: %w", err)
	}
	if len(blob) == 0 {
		// Same refusal as the durable path, and it matters more here: an
		// ephemeral machine holds nothing afterwards, so a snapshot written
		// without its escrow blob is recoverable only from the control plane
		// that this whole design assumes may one day be gone.
		return nil, nil, errors.New("delivered backup key carries no escrow blob")
	}
	return raw, blob, nil
}
