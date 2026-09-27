package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"controlplane"
	"pbscommon"
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
// public key, and only when (agent id, auth-id, generation) says what we hold
// is not the credential the server names.
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

// PBSCredentialTuple is WHICH credential a stored PBS secret is: the agent
// identity it was issued to, the token's auth-id, and the generation the
// server minted it under. See Config.PBSCredentialFor.
type PBSCredentialTuple struct {
	AgentID int64  `json:"agent_id"`
	AuthID  string `json:"auth_id"`
	Gen     int64  `json:"gen"`
}

var (
	pbsTargetMu sync.Mutex
	// pbsTargetLastSaid is the last thing written to the log about the
	// target, so that a state which persists for weeks is reported once
	// rather than ~720 times a day.
	pbsTargetLastSaid string
	// pbsFetchFor / pbsFetchAt bound how often the sealed secret is asked
	// for. THE SERVER ALLOWS 3/HOUR and answers 429 above it, so an agent
	// that fetched on every mismatched check-in -- once every ~120s -- would
	// spend two of its three attempts inside the first four minutes and then
	// be locked out for the rest of the hour, at exactly the moment it has no
	// working credential. A new (agent, auth-id, generation) is always worth
	// one immediate attempt; a repeat of one that just failed waits.
	pbsFetchFor PBSCredentialTuple
	pbsFetchAt  time.Time
	// pbsLastTarget is the newest pbs_target a check-in delivered, so a PBS
	// refusal in the middle of a run can ask for the credential the server
	// currently names without waiting for the next check-in.
	pbsLastTarget *controlplane.PBSTarget
	// pbsRefusalSaid is the tuple the last refusal WARN was about: one WARN
	// per refused credential, not one per run.
	pbsRefusalSaid PBSCredentialTuple
)

// pbsFetchCooldown keeps repeat attempts for the SAME tuple inside the
// server's 3/hour budget with room to spare. Twenty-five minutes gives at most
// two retries an hour and leaves one attempt for a genuinely new generation
// arriving mid-hour.
const pbsFetchCooldown = 25 * time.Minute

// pbsCredentialRefusedMsg is how a run ends when PBS refuses the credential
// and the control server has nothing newer: the operator-facing sentence, and
// the one WARN line the refusal logs.
const pbsCredentialRefusedMsg = "PBS refused this machine's credential; requesting a new one from the control server"

// pbsWantedTuple is the credential a target names for this machine.
func pbsWantedTuple(cfg *Config, t *controlplane.PBSTarget) PBSCredentialTuple {
	return PBSCredentialTuple{AgentID: cfg.ControlAgentID, AuthID: t.AuthID, Gen: t.CredentialGen}
}

// pbsCredentialHeld reports whether cfg holds the secret for exactly want.
//
// INDEPENDENT OF THE FIELDS applyPBSTargetFields WRITES. The held test used
// to be `Secret != "" && AuthID == target.AuthID`, evaluated AFTER the target
// had been copied into AuthID -- so the ids always matched and any saved
// secret, however old, counted as held (ledger F-38b: re-enrollment kept a
// dead secret and said it "matches"). PBSCredentialFor is written only
// together with the secret, from the fetch response.
func pbsCredentialHeld(cfg *Config, want PBSCredentialTuple) bool {
	return cfg.Secret != "" && cfg.PBSCredentialFor != nil && *cfg.PBSCredentialFor == want
}

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
			writeInfoLog("[PBS] the control server provisions no PBS target for this machine; " +
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
	pbsTargetMu.Lock()
	tc := *t
	pbsLastTarget = &tc
	pbsTargetMu.Unlock()

	// THE HELD TEST COMES FIRST, and it compares the tuple the secret was
	// stored WITH -- never AuthID, which the next statement overwrites.
	want := pbsWantedTuple(cfg, t)
	held := pbsCredentialHeld(cfg, want)

	// The non-secret half is applied on its own. It is safe to write even
	// when the secret fetch below fails or is on cooldown: a machine holding
	// the right destination and a stale secret reports a PBS authentication
	// failure, which is diagnosable, while one holding a stale destination
	// reports success against the wrong namespace, which is not.
	if applyPBSTargetFields(cfg, t) {
		if err := cfg.Save(); err != nil {
			writeErrorLog(fmt.Sprintf("[PBS] ERROR: could not persist the server-provisioned PBS target: %v", err))
			return
		}
		writeInfoLog(fmt.Sprintf(
			"[PBS] control server provisioned this machine: %s datastore=%q namespace=%q as %s",
			t.BaseURL, t.Datastore, t.Namespace, t.AuthID))
	}

	if held {
		sayOnce(fmt.Sprintf("held:%s:%d", t.AuthID, t.CredentialGen), func() {
			writeInfoLog(fmt.Sprintf(
				"[PBS] the credential this machine holds is the one the server provisioned (%s, generation %d)",
				t.AuthID, t.CredentialGen))
		})
		return
	}

	cpMu.Lock()
	client := cpClient
	cpMu.Unlock()
	if client == nil {
		return
	}
	a.fetchPBSCredential(withAgentKeyFetcher(client), want)
}

// pbsCredentialFetcher is the one call fetchPBSCredential makes, so a test
// can stand in for the control server without a network.
type pbsCredentialFetcher func() (*controlplane.PBSCredential, error)

// withAgentKeyFetcher is the production fetcher: registers this machine's key
// first when the server asks for one (withAgentKey), then fetches.
func withAgentKeyFetcher(client *controlplane.Client) pbsCredentialFetcher {
	return func() (*controlplane.PBSCredential, error) {
		return withAgentKey(client, client.FetchPBSCredential)
	}
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
// cooldown that keeps this inside the server's rate limit. Reports whether a
// credential was stored, and whether it is a DIFFERENT one from what this
// machine held before.
func (a *App) fetchPBSCredential(fetch pbsCredentialFetcher, want PBSCredentialTuple) (stored, changed bool) {
	pbsTargetMu.Lock()
	if pbsFetchFor == want && time.Since(pbsFetchAt) < pbsFetchCooldown {
		pbsTargetMu.Unlock()
		return false, false
	}
	pbsFetchFor, pbsFetchAt = want, time.Now()
	pbsTargetMu.Unlock()

	cred, err := fetch()
	if err != nil {
		switch {
		case errors.Is(err, controlplane.ErrNotProvisioned):
			// NOT A FAULT. The organization has not been attached to a
			// datastore, or the vault is locked. It resolves when an operator
			// acts, with nothing for this machine to do but ask again later --
			// which the cooldown above already schedules.
			sayOnce("unprovisioned:"+want.AuthID, func() {
				writeInfoLog("[PBS] the control server has no credential provisioned for this machine yet")
			})
		case errors.Is(err, controlplane.ErrKeyRequired):
			// withAgentKey already registered and retried once. Reaching here
			// means the server still disagrees, which is not a race this
			// client wins by trying harder.
			writeWarnLog(fmt.Sprintf("[PBS] WARNING: cannot receive a PBS credential: %v", err))
		default:
			writeWarnLog(fmt.Sprintf("[PBS] WARNING: could not fetch the PBS credential: %v", err))
		}
		return false, false
	}

	// The server may have re-provisioned between the check-in that named a
	// credential and this call. Its answer is the newer one, so it wins -- and
	// it is filed under the auth-id and generation IT carries, which the
	// server read in the same statement as the secret.
	if cred.AuthID != want.AuthID || cred.CredentialGen != want.Gen {
		writeWarnLog(fmt.Sprintf(
			"[PBS] the server issued a credential for %s (generation %d) rather than the %s (generation %d) advertised at check-in; using the newer one",
			cred.AuthID, cred.CredentialGen, want.AuthID, want.Gen))
	}

	secret, err := openSealedPBSSecret(cred)
	if err != nil {
		writeErrorLog(fmt.Sprintf("[PBS] ERROR: the delivered PBS credential could not be opened: %v", err))
		return false, false
	}

	cfg := a.config
	if cfg == nil {
		return false, false
	}
	got := PBSCredentialTuple{AgentID: want.AgentID, AuthID: cred.AuthID, Gen: cred.CredentialGen}
	changed = cfg.PBSCredentialFor == nil || *cfg.PBSCredentialFor != got || cfg.Secret != secret
	applyPBSTargetFields(cfg, &cred.PBSTarget)
	cfg.Secret = secret
	cfg.PBSCredentialFor = &got
	if changed {
		// A refusal was about the credential it replaced.
		cfg.PBSRefusedAt = 0
	}
	if err := cfg.Save(); err != nil {
		// Loud, and it matters: an agent that cannot persist this refetches
		// against a 3/hour limit forever, which is precisely the failure that
		// limit exists to make visible rather than to hide.
		writeErrorLog(fmt.Sprintf("[PBS] ERROR: could not store the PBS credential: %v", err))
		return false, false
	}
	writeInfoLog(fmt.Sprintf("[PBS] stored the PBS credential the control server issued for %s (generation %d)",
		cred.AuthID, cred.CredentialGen))

	pbsTargetMu.Lock()
	pbsTargetLastSaid = fmt.Sprintf("held:%s:%d", cred.AuthID, cred.CredentialGen)
	pbsTargetMu.Unlock()
	return true, changed
}

// heldPBSCredentialReport is the check-in's `pbs_credential`: the pair stored
// with the secret and the last refusal, never the secret. nil when nothing is
// held, which omits the field.
func (a *App) heldPBSCredentialReport() *controlplane.HeldPBSCredential {
	cfg := a.config
	if cfg == nil || cfg.Secret == "" || cfg.PBSCredentialFor == nil {
		return nil
	}
	out := &controlplane.HeldPBSCredential{AuthID: cfg.PBSCredentialFor.AuthID, Gen: cfg.PBSCredentialFor.Gen}
	if cfg.PBSRefusedAt > 0 {
		r := cfg.PBSRefusedAt
		out.RefusedAt = &r
	}
	return out
}

// isPBSCredentialRefused reports whether err is PBS refusing this machine's
// CREDENTIAL (HTTP 401 on the session upgrade) -- not a network failure, not
// a 403 (a valid token lacking permission, which no new secret fixes), and not
// the "session lost" that used to buy a 25-minute wait for a credential that
// was never going to work (T5.4).
//
// Typed first: the engines wrap with %w, and pbscommon.PBSResponseError is
// the upgrade's rejection. The text fallback is OUR OWN format
// (PBSResponseError.Error), pinned by a test against the captured lines, for
// any path that flattened the chain with %v.
func isPBSCredentialRefused(err error) bool {
	if err == nil {
		return false
	}
	var rej *pbscommon.PBSResponseError
	if errors.As(err, &rej) {
		return strings.HasPrefix(rej.StatusCode, "401")
	}
	return strings.Contains(err.Error(), "PBS authentication or authorization failed: HTTP 401")
}

// notePBSCredentialRefused records that PBS refused the held credential, says
// so ONCE per refused credential at WARN, and asks the control server for the
// credential it currently names -- through the normal cooldown, so repeated
// refusals cost at most the ordinary budget. Reports whether that produced a
// different credential (the next run can then succeed).
//
// It never re-mints anything: the server shows the refusal (it arrives on the
// next check-in as pbs_credential.refused_at) for a human, or F-22's Resync,
// to act on.
func (a *App) notePBSCredentialRefused(fetch pbsCredentialFetcher) (renewed bool) {
	cfg := a.config
	if cfg == nil {
		return false
	}
	held := PBSCredentialTuple{}
	if cfg.PBSCredentialFor != nil {
		held = *cfg.PBSCredentialFor
	}
	cfg.PBSRefusedAt = time.Now().Unix()
	if err := cfg.Save(); err != nil {
		writeErrorLog(fmt.Sprintf("[PBS] ERROR: could not record the PBS refusal: %v", err))
	}

	pbsTargetMu.Lock()
	first := pbsRefusalSaid != held
	pbsRefusalSaid = held
	var t *controlplane.PBSTarget
	if pbsLastTarget != nil {
		tc := *pbsLastTarget
		t = &tc
	}
	pbsTargetMu.Unlock()
	if first {
		writeWarnLog(fmt.Sprintf("[PBS] %s (%s, generation %d)", pbsCredentialRefusedMsg, held.AuthID, held.Gen))
	}

	want := held
	if t != nil {
		want = pbsWantedTuple(cfg, t)
	}
	if fetch == nil {
		return false
	}
	_, changed := a.fetchPBSCredential(fetch, want)
	return changed
}

// pbsRefusalRunError is the run's error after a refusal: the operator-facing
// sentence first, the PBS detail kept underneath.
func pbsRefusalRunError(err error, renewed bool) error {
	if renewed {
		return fmt.Errorf("PBS refused this machine's credential; the control server issued a new one, which the next run uses: %w", err)
	}
	return fmt.Errorf("%s: %w", pbsCredentialRefusedMsg, err)
}
