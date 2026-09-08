package controlplane

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// P3 — the PBS credential, and the split that keeps it off the wire.
//
// A machine's PBS token has two halves and they travel differently. WHERE to
// back up (base URL, datastore, namespace, and the auth-id naming this
// machine's own token) rides on every check-in as `pbs_target`, because it is
// not a secret and an agent needs it to know whether what it holds is still
// current. The SECRET half moves only through POST /api/agent/v1/pbs-credential,
// sealed, and only when the agent does not already have it.
//
// That is the same split as the backup key, for the same reason: a credential
// re-sent on every cycle is a credential sitting in every log, proxy and cache
// between the server and the machine. The server rate-limits this endpoint to
// 3/hour and logs every release at WARN precisely so an agent that fetches
// repeatedly -- one failing to persist what it got -- is visible rather than
// invisible.

// PBSTarget is `pbs_target` from a check-in: where this machine backs up, and
// under which identity. Never carries the secret.
//
// A NULL pbs_target IS NOT AN INSTRUCTION. It means this server does not
// provision automatically -- no client token owner configured, the
// organization not attached to a datastore, or (legitimately, and
// temporarily) the vault locked. An agent that read null as "back up
// somewhere else" would throw away a working hand-configured PBS on the first
// cycle after a superadmin signed out. Keep what you have.
type PBSTarget struct {
	// AuthID is the PBS API token id belonging to THIS machine, e.g.
	// "nimbus-clients@pbs!acme--frontdesk-01-17". It is also the change
	// detector: when it differs from the auth-id whose secret we hold, our
	// secret is for a token that is not ours any more and must be refetched.
	AuthID string `json:"auth_id"`

	BaseURL   string `json:"base_url"`
	Datastore string `json:"datastore"`
	Namespace string `json:"namespace"`

	// Fingerprint is null when the PBS is CA-verified rather than pinned.
	Fingerprint string `json:"fingerprint"`
}

// Complete reports whether this target names somewhere a backup could
// actually be written. A partially-filled target is a server-side
// provisioning state, not something to configure a client from -- and
// applying half of one would leave a machine pointed at a real server with no
// datastore, which fails at the least useful moment.
func (t *PBSTarget) Complete() bool {
	return t != nil && t.AuthID != "" && t.BaseURL != "" && t.Datastore != ""
}

// PBSCredential is the sealed response from POST /api/agent/v1/pbs-credential:
// the target again, plus the secret half sealed to our registered public key.
//
// The server returns the target alongside the secret rather than making the
// caller correlate it with a check-in, and that is worth keeping: the two are
// only meaningful together. A secret is FOR an auth-id, and storing one
// against the wrong one is indistinguishable from a wrong password until the
// first backup fails.
type PBSCredential struct {
	PBSTarget
	SecretSealedB64 string `json:"secret_sealed"`
	SealedToKeyID   string `json:"key_id"`
}

// ErrNotProvisioned: this server provisions nothing for this machine.
//
// SEPARATE FROM ErrKeyRequired ON PURPOSE, because both arrive as 409 from the
// same endpoint and they demand opposite responses. ErrKeyRequired is fixed by
// an action only the agent can take (register a key), so it must act. This one
// is fixed by an operator attaching the organization to a datastore, so the
// agent must NOT act -- it keeps the PBS settings it has and asks again on a
// later cycle. Treating this as a fault would either spin forever registering
// keys or discard a working configuration; both have been shipped by somebody.
var ErrNotProvisioned = errors.New("controlplane: this server provisions no PBS credential for this machine")

// FetchPBSCredential retrieves the sealed secret half of this machine's PBS
// token. The caller opens it with the agent key named by SealedToKeyID.
//
// CALL THIS ON A MISMATCH, NOT ON A SCHEDULE. The auth-id on every check-in is
// what says whether the secret already held is still the right one.
func (c *Client) FetchPBSCredential() (*PBSCredential, error) {
	var out PBSCredential
	if err := c.post("/api/agent/v1/pbs-credential", struct{}{}, &out, true); err != nil {
		return nil, asPBSCredentialError(err)
	}
	if out.SecretSealedB64 == "" {
		return nil, fmt.Errorf("controlplane: pbs-credential returned no sealed secret")
	}
	if _, err := base64.StdEncoding.DecodeString(out.SecretSealedB64); err != nil {
		return nil, fmt.Errorf("controlplane: pbs-credential secret is not base64: %w", err)
	}
	if !out.Complete() {
		return nil, fmt.Errorf(
			"controlplane: pbs-credential returned an incomplete target (auth_id=%q base_url=%q datastore=%q)",
			out.AuthID, out.BaseURL, out.Datastore)
	}
	return &out, nil
}

// asPBSCredentialError sorts this endpoint's 409s into the two sentinels.
//
// THE SERVER'S `code` IS THE CONTRACT and the message is not, so the code is
// what is read. The message fallback below exists for exactly one situation --
// a server older than the code field -- and is deliberately narrow: it looks
// for the phrase naming a registered key, and anything it cannot place becomes
// ErrNotProvisioned, which is the SAFE default because its response is to
// change nothing.
func asPBSCredentialError(err error) error {
	he := (*httpError)(nil)
	if !asHTTPError(err, &he) || he.status != 409 {
		return err
	}
	switch he.code {
	case "key_required":
		return fmt.Errorf("%w: %s", ErrKeyRequired, he.msg)
	case "not_provisioned":
		return fmt.Errorf("%w: %s", ErrNotProvisioned, he.msg)
	}
	if strings.Contains(strings.ToLower(he.msg), "registered public key") {
		return fmt.Errorf("%w: %s", ErrKeyRequired, he.msg)
	}
	return fmt.Errorf("%w: %s", ErrNotProvisioned, he.msg)
}
