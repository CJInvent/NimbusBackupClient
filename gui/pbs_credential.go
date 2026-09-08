package main

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"controlplane"
)

// P3, client half — the machine's own PBS credential, applied from the server.
//
// CJ's rule is that nothing is filled in by hand that the system can derive,
// and this is where that rule meets PBS: an operator configures ONE token, on
// the control plane, and every machine's own token is minted from it and
// delivered here. Nobody types a datastore name into an agent.
//
// The two halves arrive differently and that split is the design. `pbs_target`
// rides on every check-in -- base URL, datastore, namespace, and the auth-id
// naming this machine's token -- because none of it is secret and the agent
// needs it to know whether what it holds is current. The secret comes from
// POST /api/agent/v1/pbs-credential, sealed to this machine's registered
// public key, and only when the auth-id says what we hold is not ours.
//
// ==================================================== the three states of nil
//
// A nil target is by far the most common non-trivial case, and confusing it
// for an instruction is how a fleet loses its configuration:
//
//   - the organization is not attached to a datastore yet;
//   - the control plane has no client token owner configured;
//   - THE VAULT IS LOCKED, which is routine, temporary, and required by
//     roadmap §2 to leave check-in working.
//
// All three mean "this server has nothing to say about your PBS settings".
// None of them means stop, and none means back up somewhere else. So nil
// changes nothing here, ever.

var (
	pbsTargetMu sync.Mutex
	// pbsTargetLastSaid is the last thing written to the log about the
	// target, so that a state which persists for weeks is reported once
	// rather than ~720 times a day.
	pbsTargetLastSaid string
	// pbsFetchAuthID / pbsFetchAt bound how often the sealed secret is
	// asked for. THE SERVER ALLOWS 3/HOUR and answers 429 above it, so an
	// agent that fetched on every mismatched check-in -- once every ~120s --
	// would spend two of its three attempts inside the first four minutes and
	// then be locked out for the rest of the hour, at exactly the moment it
	// has no working credential. A new auth-id is always worth one immediate
	// attempt; a repeat of one that just failed waits.
	pbsFetchAuthID string
	pbsFetchAt     time.Time
)

// pbsFetchCooldown keeps repeat attempts for the SAME auth-id inside the
// server's 3/hour budget with room to spare. Twenty-five minutes gives at most
// two retries an hour and leaves one attempt for a genuinely new auth-id
// arriving mid-hour.
const pbsFetchCooldown = 25 * time.Minute

// sayOnce logs a line only when it differs from the last thing said about the
// PBS target. Returns whether it said anything, which is useful in tests and
// nowhere else.
func sayOnce(state string, emit func()) bool {
	pbsTargetMu.Lock()
	same := pbsTargetLastSaid == state
	pbsTargetLastSaid = state
	pbsTargetMu.Unlock()
	if same {
		return false
	}
	emit()
	return true
}

// applyPBSTargetFromCheckin is the OnPBSTarget hook.
//
// A METHOD, not a package function, because unlike managed jobs this writes
// into the live Config the backup engines read through EffectivePBS -- there
// is no separate file it could own instead.
func (a *App) applyPBSTargetFromCheckin(t *controlplane.PBSTarget) {
	if t == nil {
		sayOnce("none", func() {
			writeDebugLog("[PBS] the control server provisions no PBS target for this machine; " +
				"keeping the settings already configured here")
		})
		return
	}
	if !t.Complete() {
		// A half-filled target is a server-side provisioning state, not
		// something to configure from. Applying half of it would point this
		// machine at a real server with no datastore, and that fails at the
		// least useful moment -- inside a backup, at night.
		sayOnce("incomplete:"+t.AuthID, func() {
			writeWarnLog(fmt.Sprintf(
				"[PBS] the control server sent an incomplete PBS target (auth_id=%q base_url=%q datastore=%q); "+
					"ignoring it and keeping the settings already configured here",
				t.AuthID, t.BaseURL, t.Datastore))
		})
		return
	}

	cfg := a.config
	if cfg == nil {
		return
	}

	// The non-secret half is applied first and on its own. It is safe to
	// write even when the secret fetch below fails or is on cooldown: a
	// machine holding the right destination and a stale secret reports a
	// PBS authentication failure, which is diagnosable, while one holding a
	// stale destination reports success against the wrong namespace, which
	// is not.
	if applyPBSTargetFields(cfg, t) {
		if err := cfg.Save(); err != nil {
			writeErrorLog(fmt.Sprintf("[PBS] ERROR: could not persist the server-provisioned PBS target: %v", err))
			return
		}
		writeDebugLog(fmt.Sprintf(
			"[PBS] control server provisioned this machine: %s datastore=%q namespace=%q as %s",
			t.BaseURL, t.Datastore, t.Namespace, t.AuthID))
	}

	// THE AUTH-ID IS THE CHANGE DETECTOR. A secret is FOR an auth-id; holding
	// one against a different one is indistinguishable from a wrong password
	// until the first backup fails.
	if cfg.Secret != "" && cfg.AuthID == t.AuthID {
		sayOnce("held:"+t.AuthID, func() {
			writeDebugLog("[PBS] the credential this machine holds matches the one the server provisioned")
		})
		return
	}

	cpMu.Lock()
	client := cpClient
	cpMu.Unlock()
	if client == nil {
		return
	}
	a.fetchPBSCredential(client, t.AuthID)
}

// applyPBSTargetFields copies the non-secret half onto the config, reporting
// whether anything actually changed.
//
// WRITTEN ONLY ON CHANGE, because the alternative is rewriting the config file
// roughly 720 times a day to store the same bytes -- avoidable disk wear, and
// a log nobody can read for the noise.
func applyPBSTargetFields(cfg *Config, t *controlplane.PBSTarget) bool {
	if cfg.BaseURL == t.BaseURL && cfg.AuthID == t.AuthID &&
		cfg.Datastore == t.Datastore && cfg.Namespace == t.Namespace &&
		cfg.CertFingerprint == t.Fingerprint {
		return false
	}
	// The secret is deliberately NOT cleared when the auth-id changes. It is
	// about to be replaced by a fetch, and blanking it first would leave a
	// window -- however short -- in which this machine can reach PBS and
	// cannot authenticate to it. A wrong-but-present secret fails the same
	// way an absent one does, and it fails without a gap.
	cfg.BaseURL = t.BaseURL
	cfg.AuthID = t.AuthID
	cfg.Datastore = t.Datastore
	cfg.Namespace = t.Namespace
	cfg.CertFingerprint = t.Fingerprint
	return true
}

// fetchPBSCredential asks for the sealed secret and stores it, subject to the
// cooldown that keeps this inside the server's rate limit.
func (a *App) fetchPBSCredential(client *controlplane.Client, authID string) {
	pbsTargetMu.Lock()
	if pbsFetchAuthID == authID && time.Since(pbsFetchAt) < pbsFetchCooldown {
		pbsTargetMu.Unlock()
		return
	}
	pbsFetchAuthID, pbsFetchAt = authID, time.Now()
	pbsTargetMu.Unlock()

	cred, err := withAgentKey(client, client.FetchPBSCredential)
	if err != nil {
		switch {
		case errors.Is(err, controlplane.ErrNotProvisioned):
			// NOT A FAULT. The organization has not been attached to a
			// datastore, or the vault is locked. It resolves when an operator
			// acts, with nothing for this machine to do but ask again later --
			// which the cooldown above already schedules.
			sayOnce("unprovisioned:"+authID, func() {
				writeDebugLog("[PBS] the control server has no credential provisioned for this machine yet")
			})
		case errors.Is(err, controlplane.ErrKeyRequired):
			// withAgentKey already registered and retried once. Reaching here
			// means the server still disagrees, which is not a race this
			// client wins by trying harder.
			writeWarnLog(fmt.Sprintf("[PBS] WARNING: cannot receive a PBS credential: %v", err))
		default:
			writeWarnLog(fmt.Sprintf("[PBS] WARNING: could not fetch the PBS credential: %v", err))
		}
		return
	}

	// The server may have re-provisioned between the check-in that named an
	// auth-id and this call. Its answer is the newer one, so it wins -- and
	// storing the secret under the auth-id we ASKED for would file a valid
	// credential against the wrong identity.
	if cred.AuthID != authID {
		writeWarnLog(fmt.Sprintf(
			"[PBS] the server issued a credential for %s rather than the %s advertised at check-in; using the newer one",
			cred.AuthID, authID))
	}

	secret, err := openSealedPBSSecret(cred)
	if err != nil {
		writeErrorLog(fmt.Sprintf("[PBS] ERROR: the delivered PBS credential could not be opened: %v", err))
		return
	}

	cfg := a.config
	if cfg == nil {
		return
	}
	applyPBSTargetFields(cfg, &cred.PBSTarget)
	cfg.Secret = secret
	if err := cfg.Save(); err != nil {
		// Loud, and it matters: an agent that cannot persist this refetches
		// against a 3/hour limit forever, which is precisely the failure that
		// limit exists to make visible rather than to hide.
		writeErrorLog(fmt.Sprintf("[PBS] ERROR: could not store the PBS credential: %v", err))
		return
	}
	writeDebugLog(fmt.Sprintf("[PBS] stored the PBS credential the control server issued for %s", cred.AuthID))

	pbsTargetMu.Lock()
	pbsTargetLastSaid = "held:" + cred.AuthID
	pbsTargetMu.Unlock()
}
