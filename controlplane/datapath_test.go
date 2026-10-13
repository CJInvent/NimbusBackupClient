package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The data-path endpoints (NimbusControl docs/V4-DATAPATH-PROXY.md §13):
// what goes on the wire, what comes back, and that every refusal arrives as
// the server's code rather than a sentence to be matched.

type dpServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	got  map[string]map[string]any
	auth string
}

func newDPServer(t *testing.T, answers map[string]func(w http.ResponseWriter)) *dpServer {
	t.Helper()
	d := &dpServer{got: map[string]map[string]any{}}
	d.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		d.mu.Lock()
		d.got[r.URL.Path] = body
		d.auth = r.Header.Get("Authorization")
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if f, ok := answers[r.URL.Path]; ok {
			f(w)
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(d.srv.Close)
	return d
}

func (d *dpServer) client() *Client {
	sum := sha256.Sum256(d.srv.Certificate().Raw)
	return &Client{BaseURL: d.srv.URL, AgentID: 17, Secret: "s3cret-agent-secret-that-is-long-enough-to-pass",
		CertFingerprint: hex.EncodeToString(sum[:])}
}

const testGrant = `{"auth_id":"nimbus@grant!gabcdefghijklmnopqrstuvwx","secret_sealed":"c2VhbGVk",
 "sealed_to_key_id":"2026-10-13-aa","base_url":"https://dp1.example.test:8007","fingerprint":"AA:BB",
 "fallbacks":[{"base_url":"https://dp2.example.test:8007","fingerprint":"CC:DD"},{"base_url":"https://dp1.example.test:8007","fingerprint":"AA:BB"}],
 "datastore":"nimbus","namespace":"","kind":"backup","redeem_by":"2026-10-13T12:00:00Z"}`

func TestRequestBackupCarriesTheJobAndReturnsTheGrant(t *testing.T) {
	d := newDPServer(t, map[string]func(http.ResponseWriter){
		"/api/agent/v1/backup-request": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"request_id":"7f0c6f9e-1111-4222-8333-444455556666","grant":` + testGrant + `}`))
		},
	})
	resp, err := d.client().RequestBackup(BackupRequest{JobName: "Manual backup", Managed: false})
	if err != nil {
		t.Fatal(err)
	}
	if resp.RequestID == "" || resp.Grant == nil || resp.Grant.Datastore != "nimbus" {
		t.Fatalf("response %+v", resp)
	}
	body := d.got["/api/agent/v1/backup-request"]
	if body["job_name"] != "Manual backup" || body["managed"] != false {
		t.Fatalf("sent %v", body)
	}
	if !strings.HasPrefix(d.auth, "Bearer 17.") {
		t.Fatalf("not authenticated as the agent: %q", d.auth)
	}
	eps := resp.Grant.Endpoints()
	if len(eps) != 2 || eps[0].BaseURL != "https://dp1.example.test:8007" || eps[1].Fingerprint != "CC:DD" {
		t.Fatalf("endpoints %+v: the instance first, its fallbacks after, no duplicate", eps)
	}
}

func TestRefusalsArriveAsTheServersCode(t *testing.T) {
	for _, code := range []string{"org_suspended", "unmanaged_restricted", "datapath_unavailable"} {
		d := newDPServer(t, map[string]func(http.ResponseWriter){
			"/api/agent/v1/backup-request": func(w http.ResponseWriter) {
				w.WriteHeader(409)
				_, _ = w.Write([]byte(`{"error":"Backups are suspended by your provider.","code":"` + code + `"}`))
			},
		})
		_, err := d.client().RequestBackup(BackupRequest{JobName: "J"})
		var ref *RefusedError
		if !errors.As(err, &ref) || ref.Code != code || ref.Status != 409 || ref.Message == "" {
			t.Fatalf("%s: got %v", code, err)
		}
	}
}

func TestAGrantMissingItsPartsIsRefusedBeforeUse(t *testing.T) {
	d := newDPServer(t, map[string]func(http.ResponseWriter){
		"/api/agent/v1/restore-request": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"grant":{"auth_id":"root@pam","secret_sealed":"c2VhbGVk","base_url":"http://x","datastore":"nimbus"}}`))
		},
	})
	if _, err := d.client().RequestRestore(RestoreRequest{RestoreUUID: "7f0c6f9e-1111-4222-8333-444455556666"}); err == nil {
		t.Fatal("a grant that is not a grant id over https was accepted")
	}
}

func TestBrowseRequestSendsNoSnapshot(t *testing.T) {
	d := newDPServer(t, map[string]func(http.ResponseWriter){
		"/api/agent/v1/restore-request": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"grant":` + strings.Replace(testGrant, `"backup"`, `"restore"`, 1) + `}`))
		},
	})
	g, err := d.client().RequestRestore(RestoreRequest{RestoreUUID: "7f0c6f9e-1111-4222-8333-444455556666"})
	if err != nil || g.Kind != "restore" {
		t.Fatalf("%v %+v", err, g)
	}
	body := d.got["/api/agent/v1/restore-request"]
	if _, named := body["backup_id"]; named || len(body) != 1 {
		t.Fatalf("a browse request must carry only restore_uuid, sent %v", body)
	}
}

func TestDatapathCertSendsTheCSR(t *testing.T) {
	d := newDPServer(t, map[string]func(http.ResponseWriter){
		"/api/agent/v1/datapath-cert": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"certificate":"-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n","ca":"ca","expires_at":"2026-11-12T00:00:00Z"}`))
		},
	})
	out, err := d.client().RequestDatapathCert("-----BEGIN CERTIFICATE REQUEST-----")
	if err != nil || out.ExpiresAt == "" {
		t.Fatalf("%v %+v", err, out)
	}
	if d.got["/api/agent/v1/datapath-cert"]["csr"] != "-----BEGIN CERTIFICATE REQUEST-----" {
		t.Fatalf("sent %v", d.got)
	}
}

func TestProxyModeIsItsOwnPBSCredentialAnswer(t *testing.T) {
	got := asPBSCredentialError(&httpError{status: 409, msg: "This server reaches PBS through its data path", code: "proxy_mode"})
	if !errors.Is(got, ErrProxyMode) || errors.Is(got, ErrNotProvisioned) || errors.Is(got, ErrKeyRequired) {
		t.Fatalf("got %v", got)
	}
}

func TestCheckinDeliversTheDatapathBlockBeforeCommands(t *testing.T) {
	var order []string
	d := newDPServer(t, map[string]func(http.ResponseWriter){
		"/api/agent/v1/checkin": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"checkin_seconds":120,"commands":[{"id":5,"command":"noop","payload":{}}],
				"policy":{},"datapath":{"mode":"proxy","cert_renew":true}}`))
		},
		"/api/agent/v1/commands/5/result": func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{}`)) },
	})
	var seen *DatapathCheckin
	a := &Agent{Client: d.client(),
		BuildInventory: func() Inventory { return Inventory{} },
		OnDatapath:     func(dc *DatapathCheckin) { seen = dc; order = append(order, "datapath") },
		HandleCommand: func(Command) CommandResult {
			order = append(order, "command")
			return CommandResult{OK: true}
		},
	}
	a.CheckinNow()
	if st := a.Status(); st.LastError != "" {
		t.Fatalf("check-in failed: %s", st.LastError)
	}
	if !seen.Proxy() || !seen.CertRenew {
		t.Fatalf("block %+v", seen)
	}
	if len(order) < 2 || order[0] != "datapath" {
		t.Fatalf("order %v: the mode must land before the commands it governs", order)
	}
	if (*DatapathCheckin)(nil).Proxy() {
		t.Fatal("an older server (no block) must read as direct")
	}
}
