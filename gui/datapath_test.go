package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"controlplane"
)

// The data path, client half (NimbusControl docs/V4-DATAPATH-PROXY.md §13).
// Secrets are checked by OPENING them with this machine's keys, never by
// reading what was stored (rule 28).

// fakeSigner stands in for /datapath-cert: it signs whatever key the CSR
// carries with a test CA, optionally under another name or for another key.
type fakeSigner struct {
	ca       *x509.Certificate
	caKey    *ecdsa.PrivateKey
	cn       string // override the CSR's name
	otherKey bool   // sign a different key than the CSR's
	calls    int
}

func newFakeSigner(t *testing.T) *fakeSigner {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	ca, _ := x509.ParseCertificate(der)
	return &fakeSigner{ca: ca, caKey: key}
}

func (f *fakeSigner) RequestDatapathCert(csrPEM string) (*controlplane.DatapathCertResponse, error) {
	f.calls++
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		return nil, errors.New("not a CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub := csr.PublicKey
	if f.otherKey {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		pub = &k.PublicKey
	}
	cn := csr.Subject.CommonName
	if f.cn != "" {
		cn = f.cn
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(30 * 24 * time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.ca, pub, f.caKey)
	if err != nil {
		return nil, err
	}
	return &controlplane.DatapathCertResponse{
		Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		CA:          string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.ca.Raw})),
		ExpiresAt:   tmpl.NotAfter.UTC().Format(time.RFC3339)}, nil
}

func resetDatapathState(t *testing.T) {
	t.Helper()
	reset := func() {
		dpProxy.Store(false)
		dpIdentityMu.Lock()
		dpRenewAttempt = time.Time{}
		dpIdentityMu.Unlock()
		runGrantsMu.Lock()
		runGrantsByRequest = map[string]*controlplane.Grant{}
		runGrantsMu.Unlock()
		resetServiceDatapathState()
	}
	reset()
	t.Cleanup(reset)
}

func pinOf(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	pairs := make([]string, len(sum))
	for i, b := range sum {
		pairs[i] = strings.ToUpper(hex.EncodeToString([]byte{b}))
	}
	return strings.Join(pairs, ":")
}

func TestProxyModeForgetsEveryPBSSetting(t *testing.T) {
	isolateConfigDir(t)
	resetDatapathState(t)
	a := &App{config: &Config{BaseURL: "https://pbs.example:8007", AuthID: "nimbus@pbs!m17", Secret: "held-secret",
		Datastore: "DS1", Namespace: "NimbusManaged/acme/m17", CertFingerprint: pinOf("pbs"),
		PBSCredentialFor: &PBSCredentialTuple{AgentID: 17, AuthID: "nimbus@pbs!m17", Gen: 3}, PBSRefusedAt: 5,
		PBSServers:   map[string]*PBSServer{"hand": {ID: "hand", BaseURL: "https://other:8007", Secret: "typed"}},
		DefaultPBSID: "hand"}}

	a.applyDatapathFromCheckin(&controlplane.DatapathCheckin{Mode: controlplane.DatapathModeProxy})
	c := a.config
	if c.BaseURL != "" || c.AuthID != "" || c.Secret != "" || c.Datastore != "" || c.Namespace != "" ||
		c.CertFingerprint != "" || c.PBSCredentialFor != nil || c.PBSRefusedAt != 0 || c.PBSServers != nil || c.DefaultPBSID != "" {
		t.Fatalf("a PBS setting survived proxy mode: %+v", c)
	}
	if !datapathProxy() || c.DatapathMode != controlplane.DatapathModeProxy {
		t.Fatal("the mode was not recorded")
	}
	disk := LoadConfig()
	if disk.Secret != "" || len(disk.PBSServers) != 0 || disk.DatapathMode != controlplane.DatapathModeProxy {
		t.Fatalf("the file still holds what the machine forgot: secret=%q servers=%d mode=%q",
			disk.Secret, len(disk.PBSServers), disk.DatapathMode)
	}
	if a.heldPBSCredentialReport() != nil {
		t.Fatal("a proxy-mode machine reported a PBS credential")
	}

	// A restart: the mode is known before the first check-in.
	dpProxy.Store(false)
	initDatapathMode(disk)
	if !datapathProxy() {
		t.Fatal("after a restart the machine behaved as direct until its first check-in")
	}

	a.applyDatapathFromCheckin(nil) // an older server, or the switch turned off
	if datapathProxy() || a.config.DatapathMode != controlplane.DatapathModeDirect {
		t.Fatal("direct mode was not restored")
	}
}

func TestOnlyACertificateForThisKeyAndNameIsStored(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	resetDatapathState(t)
	s := newFakeSigner(t)
	s.otherKey = true
	if err := renewDatapathIdentity(s, 17, "test"); err == nil {
		t.Fatal("a certificate for another key was stored")
	}
	s = newFakeSigner(t)
	s.cn = "nimbus-agent-18"
	if err := renewDatapathIdentity(s, 17, "test"); err == nil {
		t.Fatal("a certificate naming another machine was stored")
	}
	if _, err := loadDatapathCert(); !errors.Is(err, errNoDPIdentity) {
		t.Fatalf("something was stored after two refusals: %v", err)
	}
}

func TestTheCertificateIsAskedForWhenNeededAndNotInALoop(t *testing.T) {
	isolateConfigDir(t)
	withProtector(t, "dpapi")
	resetDatapathState(t)
	cfg := &Config{ControlAgentID: 17}
	if need, _ := datapathCertNeeded(cfg, false); !need {
		t.Fatal("no certificate held, and none was asked for")
	}
	s := newFakeSigner(t)
	if err := renewDatapathIdentity(s, 17, "test"); err != nil {
		t.Fatal(err)
	}
	if need, _ := datapathCertNeeded(cfg, true); need {
		t.Fatal("asked again inside the cooldown: a server saying renew on every check-in would spend the 6/hour limit")
	}
	dpIdentityMu.Lock()
	dpRenewAttempt = time.Now().Add(-time.Hour)
	dpIdentityMu.Unlock()
	if need, _ := datapathCertNeeded(cfg, false); need {
		t.Fatal("a fresh certificate for this enrollment was replaced unasked")
	}
	if need, _ := datapathCertNeeded(cfg, true); !need {
		t.Fatal("the server's renew was ignored")
	}
	if need, _ := datapathCertNeeded(&Config{ControlAgentID: 99}, false); !need {
		t.Fatal("a certificate issued to another enrollment was kept")
	}
}

func TestTheServiceRefusesPBSSettingsInProxyMode(t *testing.T) {
	isolateConfigDir(t)
	resetDatapathState(t)
	a := &App{config: &Config{}}
	dpProxy.Store(true)
	for name, err := range map[string]error{
		"save server": a.SavePBSServerFromMap(map[string]interface{}{"id": "x", "baseurl": "https://pbs:8007"}),
		"set default": a.SetDefaultPBSByID("x"),
		"pin":         a.PinServerFingerprint("x", pinOf("x")),
		"save config": a.SaveConfigFromMap(map[string]interface{}{"baseurl": "https://pbs:8007"}),
	} {
		if !errors.Is(err, errPBSSettingsServerManaged) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
	if len(a.config.PBSServers) != 0 || a.config.BaseURL != "" {
		t.Fatal("a PBS setting was written in proxy mode")
	}
}

func TestAGrantArrivesWithItsCommand(t *testing.T) {
	resetDatapathState(t)
	p := map[string]interface{}{"grant": map[string]interface{}{"auth_id": "nimbus@grant!gabcdefghijklmnopqrstuvwx",
		"secret_sealed": "c2VhbGVk", "base_url": "https://dp1:8007", "fingerprint": "AA", "datastore": "nimbus",
		"fallbacks": []interface{}{map[string]interface{}{"base_url": "https://dp2:8007", "fingerprint": "BB"}}}}
	pg := grantFromPayload(p)
	if pg == nil || pg.Datastore != "nimbus" || len(pg.Fallbacks) != 1 || pg.Fallbacks[0].Fingerprint != "BB" {
		t.Fatalf("payload grant %+v", pg)
	}
	if grantFromPayload(map[string]interface{}{}) != nil {
		t.Fatal("a command without a grant produced one")
	}
	stashRunGrant("req-1", pg)
	stashRunGrant("", pg)
	stashRunGrant("req-2", nil)
	runGrantsMu.Lock()
	n := len(runGrantsByRequest)
	runGrantsMu.Unlock()
	if n != 1 {
		t.Fatalf("held %d grants; only a grant with its request is held", n)
	}
}
