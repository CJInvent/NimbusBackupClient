package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The wire half of the backup-key contract: what the agent remembers from a
// check-in, and what it sends when it asks for material.
//
// THE ASSERTION THAT MATTERS IS THE THREE-STATE ONE. "the org does not
// encrypt" and "we have never heard from the server" are opposite instructions
// at backup time, and a *BackupKeyAd alone cannot tell them apart — both are
// nil. If CurrentBackupKey ever collapses to a single return, a machine that
// has never checked in starts backing up in the clear for an org that mandates
// encryption, and nothing anywhere reports a problem.

func keyServer(t *testing.T, ad *BackupKeyAd, capture *BackupKeyRequest) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agent/v1/checkin", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(CheckinResponse{CheckinSeconds: 120, BackupKey: ad})
	})
	mux.HandleFunc("/api/agent/v1/backup-key", func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			_ = json.NewDecoder(r.Body).Decode(capture)
		}
		_ = json.NewEncoder(w).Encode(BackupKeyMaterial{KeyID: "abc", Version: 1, Scope: "org"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestBackupKeyIsUnknownUntilTheFirstCheckin(t *testing.T) {
	a := &Agent{}
	if ad, known := a.CurrentBackupKey(); known || ad != nil {
		t.Fatalf("a fresh agent reported ad=%v known=%v; it has been told nothing", ad, known)
	}
}

func TestNullAdvertisementIsKnownAndMeansEncryptionOff(t *testing.T) {
	var seen int
	srv := keyServer(t, nil, nil)
	a := &Agent{
		Client:      &Client{BaseURL: srv.URL, AgentID: 1, Secret: "s"},
		OnBackupKey: func(ad *BackupKeyAd) { seen++ },
	}
	a.CheckinNow()

	ad, known := a.CurrentBackupKey()
	if !known {
		t.Fatal("after a check-in delivering backup_key:null the answer is KNOWN — the org does not encrypt")
	}
	if ad != nil {
		t.Errorf("ad = %+v, want nil", ad)
	}
	// THE NULL CASE MUST STILL FIRE THE HOOK. It is the only signal that the
	// org turned encryption off, and an agent that only reacted to non-null
	// advertisements would keep a key it is no longer entitled to hold and
	// could never record the "off" answer for use during an outage.
	if seen != 1 {
		t.Errorf("OnBackupKey fired %d times for a null advertisement, want 1", seen)
	}
}

func TestAdvertisementIsRemembered(t *testing.T) {
	want := &BackupKeyAd{ID: 4, KeyID: "deadbeef", Version: 2, Scope: "agent", MasterFingerprint: "fp"}
	srv := keyServer(t, want, nil)
	a := &Agent{Client: &Client{BaseURL: srv.URL, AgentID: 1, Secret: "s"}}
	a.CheckinNow()

	ad, known := a.CurrentBackupKey()
	if !known || ad == nil {
		t.Fatalf("ad=%v known=%v after an advertisement", ad, known)
	}
	if ad.KeyID != want.KeyID || ad.Scope != want.Scope || ad.Version != want.Version {
		t.Errorf("ad = %+v, want %+v", ad, want)
	}
}

// A key advertisement must NOT expire the way a policy does. Durable mode's
// whole promise is that an encrypted machine keeps backing up through a
// control-plane outage; ageing the advertisement out would quietly convert
// every such machine into one that stops.
func TestAdvertisementDoesNotExpireWithPolicyMaxAge(t *testing.T) {
	srv := keyServer(t, &BackupKeyAd{KeyID: "k1"}, nil)
	a := &Agent{
		Client:       &Client{BaseURL: srv.URL, AgentID: 1, Secret: "s"},
		PolicyMaxAge: 1, // 1ns: any policy is stale immediately
	}
	a.CheckinNow()

	if !a.PolicyIsStale() {
		t.Fatal("precondition: the policy should have expired, otherwise this test proves nothing")
	}
	ad, known := a.CurrentBackupKey()
	if !known || ad == nil || ad.KeyID != "k1" {
		t.Errorf("ad=%v known=%v; the key advertisement must outlive the policy window", ad, known)
	}
}

// The fetch names its mode and its run. Both are audit inputs: a release with
// no run behind it is what a stolen agent token looks like, and omitting
// run_uuid is the trivial evasion the server deliberately flags.
func TestFetchBackupKeySendsModeAndRunUUID(t *testing.T) {
	var got BackupKeyRequest
	srv := keyServer(t, nil, &got)
	c := &Client{BaseURL: srv.URL, AgentID: 1, Secret: "s"}

	if _, err := c.FetchBackupKey(BackupKeyRequest{Mode: KeyModeEphemeral, RunUUID: "run-1"}); err != nil {
		t.Fatalf("FetchBackupKey: %v", err)
	}
	if got.Mode != KeyModeEphemeral {
		t.Errorf("mode = %q, want %q", got.Mode, KeyModeEphemeral)
	}
	if got.RunUUID != "run-1" {
		t.Errorf("run_uuid = %q, want run-1 — a release the audit cannot match to a run reads as a stolen token", got.RunUUID)
	}

	// An unset mode must not reach the server empty: the server would fall back
	// to durable anyway, but then the rate limit an ephemeral machine gets
	// depends on a default two systems away from the code that chose the mode.
	got = BackupKeyRequest{}
	if _, err := c.FetchBackupKey(BackupKeyRequest{RunUUID: "run-2"}); err != nil {
		t.Fatalf("FetchBackupKey: %v", err)
	}
	if got.Mode != KeyModeDurable {
		t.Errorf("mode = %q for an unset mode, want %q", got.Mode, KeyModeDurable)
	}
}
