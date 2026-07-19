package controlplane

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validProfileJSON(t *testing.T, mutate func(m map[string]any)) []byte {
	t.Helper()
	m := map[string]any{
		"profile_version":     ProfileVersion,
		"org_name":            "Acme Corp",
		"control_server_url":  "https://control.example.com",
		"control_cert_fp":     strings.Repeat("ab", 32),
		"enroll_token":        "one-time-org-token",
		"default_backup_mode": "directory",
		"issued_at":           "2026-07-19T12:00:00Z",
		"issued_by":           "nimbuscontrol/0.1.2",
	}
	if mutate != nil {
		mutate(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestParseProfileAcceptsAValidProfile(t *testing.T) {
	p, err := ParseProfile(validProfileJSON(t, nil))
	if err != nil {
		t.Fatalf("valid profile refused: %v", err)
	}
	if p.ControlURL != "https://control.example.com" || p.EnrollToken != "one-time-org-token" {
		t.Errorf("fields not carried through: %+v", p)
	}
	if p.DefaultMode != "directory" || p.OrgName != "Acme Corp" {
		t.Errorf("optional fields not carried through: %+v", p)
	}

	// Colon-grouped fingerprints are what people paste out of OpenSSL.
	colon := strings.ToUpper(strings.Join(splitPairs(strings.Repeat("ab", 32)), ":"))
	p2, err := ParseProfile(validProfileJSON(t, func(m map[string]any) {
		m["control_cert_fp"] = colon
	}))
	if err != nil {
		t.Fatalf("colon-grouped fingerprint refused: %v", err)
	}
	if NormalizeFingerprint(p2.CertFingerprint) != strings.Repeat("ab", 32) {
		t.Errorf("fingerprint did not normalize: %q", p2.CertFingerprint)
	}
}

func TestParseProfileRefusesBadInput(t *testing.T) {
	cases := []struct {
		name   string
		body   []byte
		expect string
	}{
		{"not json", []byte("{nope"), "valid JSON"},
		{"empty", []byte(""), "valid JSON"},
		{"future version", validProfileJSON(t, func(m map[string]any) {
			m["profile_version"] = 99
		}), "unsupported provisioning profile version"},
		{"missing version", validProfileJSON(t, func(m map[string]any) {
			delete(m, "profile_version")
		}), "unsupported provisioning profile version"},
		{"no url", validProfileJSON(t, func(m map[string]any) {
			m["control_server_url"] = ""
		}), "no control_server_url"},
		{"plaintext http", validProfileJSON(t, func(m map[string]any) {
			m["control_server_url"] = "http://control.example.com"
		}), "must be https"},
		{"no host", validProfileJSON(t, func(m map[string]any) {
			m["control_server_url"] = "https://"
		}), "no host"},
		{"no token", validProfileJSON(t, func(m map[string]any) {
			m["enroll_token"] = ""
		}), "no enroll_token"},
		{"short fingerprint", validProfileJSON(t, func(m map[string]any) {
			m["control_cert_fp"] = "aabbcc"
		}), "SHA-256 hex digest"},
		{"non-hex fingerprint", validProfileJSON(t, func(m map[string]any) {
			m["control_cert_fp"] = strings.Repeat("zz", 32)
		}), "not hexadecimal"},
		{"bad backup mode", validProfileJSON(t, func(m map[string]any) {
			m["default_backup_mode"] = "everything"
		}), "directory or machine"},
		// A field this build does not understand may be load-bearing on the
		// server side; guessing is how an agent half-applies a policy.
		{"unknown field", []byte(`{"profile_version":1,"control_server_url":"https://x.example",` +
			`"enroll_token":"t","require_totp_before_backup":true}`), "not valid JSON"},
	}
	for _, c := range cases {
		p, err := ParseProfile(c.body)
		if err == nil {
			t.Errorf("%s: accepted, want refusal (parsed %+v)", c.name, p)
			continue
		}
		if !strings.Contains(err.Error(), c.expect) {
			t.Errorf("%s: error %q does not mention %q", c.name, err, c.expect)
		}
	}
}

// A profile with no fingerprint is legal — an org using a publicly trusted
// certificate does not need to pin.
func TestParseProfileFingerprintOptional(t *testing.T) {
	p, err := ParseProfile(validProfileJSON(t, func(m map[string]any) {
		delete(m, "control_cert_fp")
	}))
	if err != nil {
		t.Fatalf("profile without a pin refused: %v", err)
	}
	if p.CertFingerprint != "" {
		t.Errorf("expected no fingerprint, got %q", p.CertFingerprint)
	}
}

// The token is a bearer credential. It must not reach a log in any form —
// not truncated, not prefixed.
func TestRedactedNeverLeaksTheToken(t *testing.T) {
	p, err := ParseProfile(validProfileJSON(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	out := p.Redacted()
	if strings.Contains(out, p.EnrollToken) {
		t.Fatalf("Redacted() leaked the enrollment token: %s", out)
	}
	for n := 4; n <= len(p.EnrollToken); n++ {
		if strings.Contains(out, p.EnrollToken[:n]) {
			t.Fatalf("Redacted() leaked a %d-char token prefix: %s", n, out)
		}
	}
	// It still has to be useful for diagnosing a bad rollout.
	for _, want := range []string{"Acme Corp", "https://control.example.com", "pinned", "directory"} {
		if !strings.Contains(out, want) {
			t.Errorf("Redacted() omitted %q, leaving nothing to diagnose: %s", want, out)
		}
	}
}

func TestProfileAge(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	p := &Profile{IssuedAt: "2026-07-12T12:00:00Z"}
	d, ok := p.Age(now)
	if !ok || d != 7*24*time.Hour {
		t.Errorf("Age = %v, %v; want 168h, true", d, ok)
	}
	if _, ok := (&Profile{}).Age(now); ok {
		t.Error("a profile with no timestamp reported a usable age")
	}
	if _, ok := (&Profile{IssuedAt: "last tuesday"}).Age(now); ok {
		t.Error("an unparseable timestamp reported a usable age")
	}
}

func splitPairs(s string) []string {
	var out []string
	for i := 0; i+1 < len(s); i += 2 {
		out = append(out, s[i:i+2])
	}
	return out
}
