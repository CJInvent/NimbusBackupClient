//go:build service
// +build service

package main

// The service's half of the console routes (console_api_service.go), against
// real inputs: a config carrying a real-shaped secret, a TLS server standing
// in for PBS, a real directory for the space check.
//
// Named TestServiceAPI* so the Windows smoke step's `-tags service` run picks
// them up; CI's Linux unit job runs them too.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tizbac/proxmoxbackupclient_go/gui/api"
)

// testSecret is generated per run: a fixed literal would be a
// credential-shaped string in the repository, and a test that a fixed value
// is absent can pass by luck.
func testSecret(t *testing.T) string {
	t.Helper()
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func withProxyMode(t *testing.T, on bool) {
	t.Helper()
	prev := dpProxy.Load()
	dpProxy.Store(on)
	t.Cleanup(func() { dpProxy.Store(prev) })
}

func TestServiceAPIPBSServersNeverCarryTheSecret(t *testing.T) {
	withProxyMode(t, false)
	s1, s2 := testSecret(t), testSecret(t)
	a := &App{config: &Config{
		DefaultPBSID: "primary",
		PBSServers: map[string]*PBSServer{
			"primary": {ID: "primary", Name: "Primary", BaseURL: "https://pbs1.example:8007",
				AuthID: "backup@pbs!ws01", Secret: s1, Datastore: "store1"},
			"dr": {ID: "dr", Name: "Offsite", BaseURL: "https://dr.example:8007",
				AuthID: "backup@pbs!ws01", Secret: s2, Datastore: "dr"},
			"blank": {ID: "blank", Name: "No token yet", BaseURL: "https://new.example:8007"},
		},
	}}

	out := a.PBSServersForAPI()
	wire, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{s1, s2} {
		if strings.Contains(string(wire), s) {
			t.Fatalf("GET /pbs/servers would carry a stored token: %s", wire)
		}
	}
	if out.DefaultID != "primary" || out.DatapathProxy {
		t.Errorf("default/proxy = %q/%v", out.DefaultID, out.DatapathProxy)
	}
	ids := []string{}
	set := map[string]bool{}
	for _, s := range out.Servers {
		ids = append(ids, s.ID)
		set[s.ID] = s.SecretSet
	}
	if strings.Join(ids, ",") != "blank,dr,primary" {
		t.Errorf("servers in order %v; want sorted by id, so the list does not reshuffle between reads", ids)
	}
	if !set["primary"] || !set["dr"] || set["blank"] {
		t.Errorf("secret_set = %v; want true exactly where a token is stored", set)
	}
	// And the stored records still hold their tokens: sanitizing the answer
	// must not have blanked the configuration.
	if a.config.PBSServers["primary"].Secret != s1 {
		t.Error("building the answer changed the stored server")
	}
}

func TestServiceAPIPBSServersAreEmptyInProxyMode(t *testing.T) {
	withProxyMode(t, true)
	a := &App{config: &Config{PBSServers: map[string]*PBSServer{
		"old": {ID: "old", BaseURL: "https://old.example:8007", Secret: testSecret(t)},
	}}}
	out := a.PBSServersForAPI()
	if !out.DatapathProxy || len(out.Servers) != 0 || out.Servers == nil {
		t.Errorf("proxy mode answered %+v; want an empty list and datapath_proxy", out)
	}
}

// fakePBS answers the one request the connection test makes: the datastore's
// status, which needs the right token.
func fakePBS(t *testing.T, authID, secret, datastore string) *httptest.Server {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/admin/datastore/"+datastore+"/status" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != fmt.Sprintf("PBSAPIToken=%s:%s", authID, secret) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"total":1,"used":0,"avail":1}}`))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func fingerprintOf(ts *httptest.Server) string {
	sum := sha256.Sum256(ts.Certificate().Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

func TestServiceAPITestDiscoverPinAndTestAgain(t *testing.T) {
	// The console's trust-on-first-use flow, entirely in the service: the
	// test fails on the self-signed certificate and says so as a field, the
	// fingerprint is discovered, pinned, and the test passes.
	withProxyMode(t, false)
	const authID, store = "backup@pbs!ws01", "store1"
	secret := testSecret(t)
	ts := fakePBS(t, authID, secret, store)
	srv := &PBSServer{ID: "lab", Name: "Lab", BaseURL: ts.URL, AuthID: authID, Secret: secret, Datastore: store}
	a := &App{config: &Config{PBSServers: map[string]*PBSServer{"lab": srv}}}

	out, err := a.TestPBSServerForAPI("lab")
	if err != nil {
		t.Fatalf("a test that ran returned a request error: %v", err)
	}
	if out.OK || !out.CertUntrusted {
		t.Fatalf("self-signed, unpinned = %+v; want a failure marked cert_untrusted", out)
	}
	if strings.Contains(out.Error, secret) {
		t.Fatalf("the failure text carries the token: %s", out.Error)
	}

	fp, err := a.DiscoverPBSFingerprintForAPI(ts.URL)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if fp != fingerprintOf(ts) {
		t.Fatalf("discovered %s, the server presents %s", fp, fingerprintOf(ts))
	}

	srv.CertFingerprint = fp
	if out, err = a.TestPBSServerForAPI("lab"); err != nil || !out.OK {
		t.Fatalf("pinned test = %+v, %v; want OK", out, err)
	}

	// A wrong token past a pinned certificate: a failure, and not one that
	// pinning would fix.
	srv.Secret = testSecret(t)
	out, err = a.TestPBSServerForAPI("lab")
	if err != nil || out.OK || out.CertUntrusted || !strings.Contains(out.Error, "authentication failed") {
		t.Errorf("wrong token = %+v, %v; want an authentication failure, not cert_untrusted", out, err)
	}
}

func TestServiceAPITestRefusals(t *testing.T) {
	withProxyMode(t, false)
	a := &App{config: &Config{PBSServers: map[string]*PBSServer{}}}

	_, err := a.TestPBSServerForAPI("nope")
	var se *api.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusNotFound {
		t.Errorf("unknown server = %v, want 404", err)
	}

	// A configured server missing its token is a test that failed, with the
	// coded reason, not a request error.
	a.config.PBSServers["half"] = &PBSServer{ID: "half", BaseURL: "https://pbs.example:8007",
		AuthID: "backup@pbs!ws01", Datastore: "store1"}
	out, err := a.TestPBSServerForAPI("half")
	if err != nil || out.OK || !strings.Contains(out.Error, "[NB-1003]") {
		t.Errorf("no token = %+v, %v; want a failed test carrying NB-1003", out, err)
	}

	withProxyMode(t, true)
	for name, call := range map[string]func() error{
		"test":     func() error { _, err := a.TestPBSServerForAPI("half"); return err },
		"discover": func() error { _, err := a.DiscoverPBSFingerprintForAPI("https://pbs.example:8007"); return err },
	} {
		err := call()
		if !errors.As(err, &se) || se.Code != http.StatusConflict || !errors.Is(err, errPBSSettingsServerManaged) {
			t.Errorf("%s in proxy mode = %v, want 409 with the data-path sentence", name, err)
		}
	}
}

func TestServiceAPIDiscoverRefusesWhatIsNotAPBSURL(t *testing.T) {
	withProxyMode(t, false)
	a := &App{}
	for _, u := range []string{"ftp://pbs.example", "http://pbs.example:8007", "https://", "not a url"} {
		_, err := a.DiscoverPBSFingerprintForAPI(u)
		var se *api.StatusError
		if !errors.As(err, &se) || se.Code != http.StatusBadRequest {
			t.Errorf("discover(%q) = %v, want 400 before any dial", u, err)
		}
	}
}

func TestServiceAPISpaceOp(t *testing.T) {
	a := &App{}
	dir := t.TempDir()
	raw, _ := json.Marshal(SpaceParams{DestPath: dir, NeededBytes: 1})
	res, err := a.RestoreQuery(opSpace, raw)
	if err != nil {
		t.Fatalf("space: %v", err)
	}
	sc, ok := res.(SpaceCheck)
	if !ok || !sc.Fits || sc.TotalBytes == 0 || sc.NeededBytes != 1 || sc.Path != dir {
		t.Errorf("space = %#v", res)
	}

	// Built by hand: filepath.Join would clean the ".." away.
	sep := string(filepath.Separator)
	for _, bad := range []SpaceParams{{DestPath: ""}, {DestPath: dir + sep + ".." + sep + "x"}} {
		raw, _ := json.Marshal(bad)
		if _, err := a.RestoreQuery(opSpace, raw); err == nil {
			t.Errorf("space(%+v) accepted", bad)
		}
	}
	if _, err := a.RestoreQuery(opSpace, nil); err == nil {
		t.Error("space with no parameters accepted")
	}
}

func TestServiceAPIStatusConfigurationIsTheConsoleView(t *testing.T) {
	// The console's settings form reads every one of these from /status after
	// C2. Before C1 the service sent five.
	secret := testSecret(t)
	a := &App{config: &Config{BaseURL: "https://pbs.example:8007", AuthID: "backup@pbs!ws01", Secret: secret,
		Datastore: "store1", Namespace: "ns", UseVSS: true, UploadLimitMbps: 50,
		LastBackupDirs: []string{`C:\Users\a`, `D:\Data`}, ExchangeAware: true}}
	view := a.GetConfigWithHostname()
	wire, _ := json.Marshal(view)
	if strings.Contains(string(wire), secret) {
		t.Fatalf("/status configuration carries the token: %s", wire)
	}
	for _, key := range []string{"baseurl", "certfingerprint", "authid", "secret_set", "datastore", "namespace",
		"backupdir", "backup-id", "usevss", "upload_limit_mbps", "control_server_url", "control_enrolled",
		"exchange_aware", "exchange_log_truncation", "last_backup_dirs", "hostname"} {
		if _, ok := view[key]; !ok {
			t.Errorf("configuration lacks %q", key)
		}
	}
	if dirs, _ := view["last_backup_dirs"].([]string); len(dirs) != 2 || dirs[1] != `D:\Data` {
		t.Errorf("last_backup_dirs = %#v", view["last_backup_dirs"])
	}
	if view["secret_set"] != true || view["secret"] != "" {
		t.Errorf("secret/secret_set = %v/%v", view["secret"], view["secret_set"])
	}
}
