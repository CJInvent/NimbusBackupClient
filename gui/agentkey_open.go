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

	raw, err := openSealedToAgent(sealed)
	if err != nil {
		// Name the key it was sealed to. During a rotation this machine can
		// hold two, and "sealed to a key you no longer have" is a different
		// problem from "this payload is damaged" -- an operator reading the
		// log should not have to guess which.
		if m.SealedToKeyID != "" {
			return nil, fmt.Errorf("%w (sealed to key %s)", err, m.SealedToKeyID)
		}
		return nil, err
	}
	return raw, nil
}
