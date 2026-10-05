package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func echo(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestLookupAlternateWAN(t *testing.T) {
	good := echo(t, 200, " 203.0.113.9\n")
	defer good.Close()
	got, err := LookupAlternateWAN(context.Background(), good.Client(), good.URL)
	if err != nil || got != "203.0.113.9" {
		t.Fatalf("got %q, %v; want 203.0.113.9", got, err)
	}

	v6 := echo(t, 200, "2001:db8::7")
	defer v6.Close()
	if got, err := LookupAlternateWAN(context.Background(), v6.Client(), v6.URL); err != nil || got != "2001:db8::7" {
		t.Fatalf("IPv6: got %q, %v", got, err)
	}

	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"private":     {200, "192.168.1.5"},
		"cgnat":       {200, "100.64.0.7"},
		"loopback":    {200, "127.0.0.1"},
		"html page":   {200, "<html>captive portal</html>"},
		"empty":       {200, ""},
		"server fail": {500, "203.0.113.9"},
		"huge":        {200, strings.Repeat("1", 4096)},
	} {
		s := echo(t, c.status, c.body)
		if got, err := LookupAlternateWAN(context.Background(), s.Client(), s.URL); err == nil {
			t.Errorf("%s: accepted %q", name, got)
		}
		s.Close()
	}
}

func TestLookupAlternateWANIsBounded(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer slow.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := LookupAlternateWAN(ctx, slow.Client(), slow.URL); err == nil {
		t.Fatal("a stalled echo service produced an address")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("lookup was not bounded: %v", time.Since(start))
	}
}

// Wire shape transcribed from NimbusControl docs/AGENT-API.md, not derived
// from the structs.
func TestAlternateWANWireShape(t *testing.T) {
	raw, _ := json.Marshal(CheckinRequest{AlternateWANIP: "203.0.113.9"})
	if !strings.Contains(string(raw), `"alternate_wan_ip":"203.0.113.9"`) {
		t.Errorf("check-in lacks alternate_wan_ip: %s", raw)
	}
	quiet, _ := json.Marshal(CheckinRequest{})
	if strings.Contains(string(quiet), "alternate_wan_ip") {
		t.Errorf("field must be omitted when there is no reading: %s", quiet)
	}
	var p Policy
	if err := json.Unmarshal([]byte(`{"report_alternate_wan":true}`), &p); err != nil || !p.ReportAlternateWAN {
		t.Errorf("policy.report_alternate_wan not read: %+v %v", p, err)
	}
	if (Policy{}).ReportAlternateWAN {
		t.Error("zero policy must NOT contact a third party")
	}
}

// Through CheckinNow, against a fake control plane and a fake echo service:
// the policy gates the third-party request, and the reading rides the
// check-in.
func TestCheckinReportsAlternateWANOnlyWhenPolicyIsOn(t *testing.T) {
	echoHits := 0
	echoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		echoHits++
		_, _ = w.Write([]byte("203.0.113.9"))
	}))
	defer echoSrv.Close()

	var gotBody map[string]any
	policyOn := false
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"policy": map[string]any{"report_alternate_wan": policyOn},
		})
	}))
	defer cp.Close()

	a := &Agent{Client: &Client{BaseURL: cp.URL, AgentID: 1, Secret: "s"}, AlternateWANURL: echoSrv.URL}

	a.CheckinNow() // nothing delivered yet: policy is off by default
	if echoHits != 0 {
		t.Fatalf("contacted the echo service before any policy enabled it (%d hits)", echoHits)
	}
	if _, present := gotBody["alternate_wan_ip"]; present {
		t.Fatalf("check-in carried alternate_wan_ip with the policy off: %v", gotBody)
	}

	policyOn = true
	a.CheckinNow() // this response delivers policy=on
	a.CheckinNow() // the next cycle acts on it
	if echoHits != 1 {
		t.Fatalf("echo service hits = %d, want exactly 1 (one per check-in once enabled)", echoHits)
	}
	if gotBody["alternate_wan_ip"] != "203.0.113.9" {
		t.Fatalf("check-in alternate_wan_ip = %v, want 203.0.113.9", gotBody["alternate_wan_ip"])
	}
}
