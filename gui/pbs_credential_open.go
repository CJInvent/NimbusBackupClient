package main

import (
	"encoding/base64"
	"errors"
	"fmt"

	"controlplane"
)

// openSealedPBSSecret turns a sealed PBS credential into the token secret.
//
// The same shape as openDeliveredKey, and separate from it for one reason:
// what comes out is a STRING that goes into a config field, not key bytes, so
// the two cannot share a signature without one of them lying about its
// output. The opening step itself is identical and goes through the same
// openSealedToAgent, which is where "how a sealed delivery is opened" is
// answered once.
//
// Every failure is an error and never an empty secret. A caller handed "" would
// write a blank token into the config, and a blank token is indistinguishable
// from "never provisioned" on the next cycle -- so the machine would refetch
// forever against a 3/hour limit while appearing, in the config, to be fine.
func openSealedPBSSecret(c *controlplane.PBSCredential) (string, error) {
	if c == nil {
		return "", errors.New("no PBS credential was delivered")
	}
	if c.SecretSealedB64 == "" {
		// A contract violation rather than a damaged message, and worth its
		// own sentence: a server that returned an unsealed secret lands here.
		return "", errors.New("the delivered PBS credential carries no sealed secret")
	}
	sealed, err := base64.StdEncoding.DecodeString(c.SecretSealedB64)
	if err != nil {
		return "", fmt.Errorf("the delivered PBS credential is not valid base64: %w", err)
	}

	// The hint matters during a rotation: the server seals to the OLD key
	// until promotion and this machine holds both halves, so the named key is
	// tried first and every other half we hold after it.
	raw, err := openSealedToAgent(sealed, c.SealedToKeyID)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 {
		return "", errors.New("the delivered PBS credential opened to nothing")
	}
	return string(raw), nil
}
