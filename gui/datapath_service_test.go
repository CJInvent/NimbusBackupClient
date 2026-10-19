package main

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"controlplane"

	"golang.org/x/crypto/nacl/box"
)

// resetServiceDatapathState clears the service-only data path state (the
// run-bound grants and the browse grant) for resetDatapathState.
func resetServiceDatapathState() {
	runGrantsMu.Lock()
	runGrantsByBackup = map[string]*controlplane.Grant{}
	pendingRunGrant = nil
	runGrantsMu.Unlock()
	browseMu.Lock()
	browseTarget, browseLastUsed = nil, time.Time{}
	browseMu.Unlock()
}

// The pipeline in data path mode (docs/V4-DATAPATH-PROXY.md §13.3): the run's
// grant is its target, and without one nothing runs -- there is no PBS
// credential to fall back on, by design.
func TestThePipelineRunsOnlyOnItsGrant(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	resetDatapathState(t)
	dir := t.TempDir()
	a := &App{config: &Config{ControlAgentID: 17}}
	dpProxy.Store(true)

	req := backupRequest{BackupType: "directory", BackupDirs: []string{dir}, BackupID: "M17", Compression: "fastest"}
	if _, err := a.resolveBackupRequest(req); !errors.Is(err, errNoGrant) {
		t.Fatalf("no grant: got %v, want errNoGrant", err)
	}

	if err := renewDatapathIdentity(newFakeSigner(t), 17, "test"); err != nil {
		t.Fatal(err)
	}
	g := sealedGrant(t, "run-secret")
	g.BackupIDs = []controlplane.GrantBackupID{{Source: "machine", ID: "g4k2x8d9v0q7a1b3z"}, {Source: dir, ID: "g4k2x8d9v0q7a1b3z-1"}}
	stashRunGrant("req-1", g)
	bindRunGrant("req-1", "M17")
	r, err := a.resolveBackupRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if r.pbs.AuthID != g.AuthID || r.pbs.Secret != "run-secret" || r.pbs.Datastore != "nimbus" || r.pbs.DataPath == nil {
		t.Fatalf("the run's target is not its grant: %+v", r.pbs)
	}
	if _, err := a.resolveBackupRequest(req); !errors.Is(err, errNoGrant) {
		t.Fatal("one grant served two runs")
	}
}

// The local schedule never fires a managed job in data path mode: the server
// dispatches them, and both doing it would double every backup. Observed
// through the schedule state, which the tick advances (or seeds) for every
// job it looks at.
func TestTheLocalScheduleLeavesManagedJobsToTheServer(t *testing.T) {
	isolateConfigDir(t)
	resetDatapathState(t)
	applyManagedJobs([]controlplane.ManagedJob{{ID: 901, Name: "Nightly", BackupType: "directory",
		BackupDirs: []string{t.TempDir()}, Schedule: "*:0/1"}})
	t.Cleanup(func() { applyManagedJobs(nil) })
	stateLen := func() int {
		loadManagedState()
		managedStateMu.Lock()
		defer managedStateMu.Unlock()
		return len(managedState)
	}
	a := &App{config: &Config{}}

	dpProxy.Store(true)
	before := stateLen()
	a.checkManagedJobs()
	if stateLen() != before {
		t.Fatal("the managed schedule was evaluated in data path mode")
	}
	dpProxy.Store(false)
	a.checkManagedJobs()
	if stateLen() == before {
		t.Fatal("control: in direct mode the tick does look at the job (seeds it), so the assertion above means something")
	}
}

// sealedGrant makes a grant whose secret is sealed to this machine's agent key.
func sealedGrant(t *testing.T, secret string) *controlplane.Grant {
	t.Helper()
	keyID, pub, err := loadOrCreateAgentKey()
	if err != nil {
		t.Fatal(err)
	}
	var pk [32]byte
	copy(pk[:], pub)
	sealed, err := box.SealAnonymous(nil, []byte(secret), &pk, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &controlplane.Grant{AuthID: "nimbus@grant!gabcdefghijklmnopqrstuvwx",
		SecretSealedB64: base64.StdEncoding.EncodeToString(sealed), SealedToKeyID: keyID,
		BaseURL: "https://dp1.example.test:8007", Fingerprint: pinOf("dp1"),
		Fallbacks: []controlplane.GrantEndpoint{{BaseURL: "https://dp2.example.test:8007", Fingerprint: pinOf("dp2")}},
		Datastore: "nimbus", Kind: "backup"}
}

func TestAGrantOpensToATargetTheEnginesUse(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	resetDatapathState(t)
	if err := renewDatapathIdentity(newFakeSigner(t), 17, "test"); err != nil {
		t.Fatal(err)
	}
	g := sealedGrant(t, "grant-secret-value")
	tgt, err := grantTarget(g)
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Secret != "grant-secret-value" || tgt.AuthID != g.AuthID || tgt.BaseURL != g.BaseURL ||
		tgt.Datastore != "nimbus" || tgt.Namespace != "" || tgt.CertFingerprint != g.Fingerprint {
		t.Fatalf("target %+v", tgt)
	}
	if err := tgt.Validate(); err != nil {
		t.Fatalf("the engines' own check refuses a grant target: %v", err)
	}
	if tgt.DataPath == nil || tgt.DataPath.Cert == nil || len(tgt.DataPath.Endpoints) != 2 ||
		tgt.DataPath.Endpoints[1].BaseURL != "https://dp2.example.test:8007" {
		t.Fatalf("data path %+v", tgt.DataPath)
	}
	leaf, _ := x509.ParseCertificate(tgt.DataPath.Cert.Certificate[0])
	if leaf.Subject.CommonName != "nimbus-agent-17" {
		t.Fatalf("certificate names %q", leaf.Subject.CommonName)
	}
	g.SealedToKeyID = "someone-else"
	g.SecretSealedB64 = base64.StdEncoding.EncodeToString([]byte("not sealed to this machine at all, just bytes"))
	if _, err := grantTarget(g); err == nil {
		t.Fatal("a grant sealed to another key was opened")
	}
}

func TestTheGrantFollowsItsRequestToTheRun(t *testing.T) {
	resetDatapathState(t)
	g1, g2 := &controlplane.Grant{AuthID: "one"}, &controlplane.Grant{AuthID: "two"}
	stashRunGrant("req-1", g1)
	stashRunGrant("req-2", g2)
	if takeRunGrant("B1") != nil {
		t.Fatal("a grant was handed to a run before its request was announced")
	}
	bindRunGrant("req-1", "B1")
	bindRunGrant("req-2", "")
	if takeRunGrant("B1") != g1 {
		t.Fatal("the run did not get its own request's grant")
	}
	if got := takeRunGrant("B1"); got != g2 {
		t.Fatalf("second take: %v; a grant is used once, then the pending slot answers", got)
	}
	if takeRunGrant("") != nil {
		t.Fatal("a grant was handed out twice")
	}
}

type countingRequester struct {
	t     *testing.T
	calls int
}

func (c *countingRequester) RequestRestore(req controlplane.RestoreRequest) (*controlplane.Grant, error) {
	c.calls++
	if req.BackupID != "" || req.RestoreUUID == "" {
		c.t.Fatalf("a browse request must name no snapshot: %+v", req)
	}
	g := sealedGrant(c.t, "browse-secret")
	g.Kind = "restore"
	return g, nil
}

func TestOneBrowseGrantServesASessionAndIsRenewedBeforeTheServerClosesIt(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	resetDatapathState(t)
	if err := renewDatapathIdentity(newFakeSigner(t), 17, "test"); err != nil {
		t.Fatal(err)
	}
	r := &countingRequester{t: t}
	a, err := datapathBrowseTarget(r)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := datapathBrowseTarget(r)
	if r.calls != 1 || a != b || a.Secret != "browse-secret" {
		t.Fatalf("calls=%d: browsing asked the server once per listing", r.calls)
	}
	browseMu.Lock()
	browseLastUsed = time.Now().Add(-11 * time.Minute)
	browseMu.Unlock()
	if _, err := datapathBrowseTarget(r); err != nil || r.calls != 2 {
		t.Fatalf("calls=%d: a grant idle for 11 minutes was reused (the server closes it at 15)", r.calls)
	}
	if _, err := datapathBrowseTarget(nil); err != nil {
		t.Fatal("a live browse grant needs no server round trip")
	}
}

// Backup-ids in data path mode come from the grant and only from it
// (NimbusControl docs/V4-PBS-OPACITY.md §9.1): the job's own id still keys
// the run, but PBS sees the server's.
func TestThePipelineWritesOnlyUnderTheGrantsIDs(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	resetDatapathState(t)
	a := &App{config: &Config{ControlAgentID: 17}}
	dpProxy.Store(true)
	if err := renewDatapathIdentity(newFakeSigner(t), 17, "test"); err != nil {
		t.Fatal(err)
	}
	d1, d2 := t.TempDir(), t.TempDir()
	run := func(req backupRequest, ids []controlplane.GrantBackupID) (*resolvedBackup, error) {
		g := sealedGrant(t, "run-secret")
		g.BackupIDs = ids
		stashRunGrant("req-ids", g)
		bindRunGrant("req-ids", req.BackupID)
		return a.resolveBackupRequest(req)
	}
	both := []controlplane.GrantBackupID{{Source: "machine", ID: "g4k2x8d9v0q7a1b3z"},
		{Source: d1, ID: "g4k2x8d9v0q7a1b3z-1"}, {Source: d2, ID: "g4k2x8d9v0q7a1b3z-2"}}

	dirReq := backupRequest{BackupType: "directory", BackupDirs: []string{d1, d2}, BackupID: "DESKTOP-01_Docs", Compression: "fastest"}
	r, err := run(dirReq, both)
	if err != nil {
		t.Fatal(err)
	}
	if r.backupID != "DESKTOP-01_Docs" || r.pbsSourceIDs[d1] != "g4k2x8d9v0q7a1b3z-1" || r.pbsSourceIDs[d2] != "g4k2x8d9v0q7a1b3z-2" {
		t.Fatalf("the job id keys the run, the grant names PBS: key %q ids %v", r.backupID, r.pbsSourceIDs)
	}

	_, err = run(dirReq, both[:2])
	if err == nil || !strings.Contains(err.Error(), d2) {
		t.Fatalf("a directory the grant names no id for must refuse the run and say which: %v", err)
	}
	_, err = run(backupRequest{BackupType: "machine", DiskTargets: []string{"x"}, BackupID: "DESKTOP-01"}, both[1:])
	if err == nil || !strings.Contains(err.Error(), "no backup id") {
		t.Fatalf("an image backup without the machine id must not start: %v", err)
	}

	// The directory engine honors the ids as given and refuses a directory
	// without one before it connects anywhere.
	err = RunBackupInline(BackupOptions{BaseURL: "https://dp1.example.test:8007", AuthID: "nimbus@grant!x", Secret: "s",
		Datastore: "nimbus", BackupDirs: []string{d1, d2}, SourceIDs: map[string]string{d1: "g4k2x8d9v0q7a1b3z-1"}})
	if err == nil || !strings.Contains(err.Error(), "no server-assigned backup id for "+d2) {
		t.Fatalf("the engine ran a directory with no server id: %v", err)
	}
}
