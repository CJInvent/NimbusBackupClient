package main

import (
	"encoding/base64"
	"errors"
	"fmt"

	"controlplane"
)

// Opening what the control plane sealed to this machine (T4).
//
// One function, called by both delivery paths -- the durable store and the
// ephemeral holder -- so "how a delivered key is opened" has exactly one
// answer. The two paths already verify the opened material identically, and
// this keeps the step before that verification identical too.

// openDeliveredKey turns sealed key material into the raw bytes it carries.
//
// Every failure is an error and never empty bytes. A caller handed "" would
// encrypt under nothing, or store nothing and refetch forever against a
// 3/hour limit -- and the second case looks exactly like the storage failure
// that limit exists to make visible.
func openDeliveredKey(m *controlplane.BackupKeyMaterial) ([]byte, error) {
	if m == nil {
		return nil, errors.New("no backup key material was delivered")
	}
	if m.KeySealedB64 == "" {
		// Not a decode failure and worth its own sentence: a server that
		// returned an unsealed key would land here, and that is a contract
		// violation rather than a damaged message.
		return nil, errors.New("the delivered backup key carries no sealed material")
	}
	sealed, err := base64.StdEncoding.DecodeString(m.KeySealedB64)
	if err != nil {
		return nil, fmt.Errorf("the delivered backup key is not valid base64: %w", err)
	}

	// The hint matters during a rotation: the server seals to the OLD key
	// until promotion, and this machine holds both halves. openSealedToAgent
	// tries the named one first and every other one it has after that, so a
	// server that names a key we retired still gets its payload opened if we
	// can.
	raw, err := openSealedToAgent(sealed, m.SealedToKeyID)
	if err != nil {
		return nil, err
	}
	return raw, nil
}
