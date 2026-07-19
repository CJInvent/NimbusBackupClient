package controlplane

// Provisioning profile — the payload a preconfigured MSI carries so a machine
// enrolls itself on first service start, with no technician typing a URL and a
// token into a GUI on every endpoint.
//
// The profile is DATA, not configuration: the service reads it once, applies
// it, and destroys it. It is deliberately parsed here rather than in the GUI
// package because it arrives from outside the process and carries a secret,
// which makes it exactly the sort of input that needs a tested parser.
//
// Threat model, stated plainly so nobody assumes more than is true:
//
//   - The profile contains a one-time ORG enrollment token. Anyone holding the
//     file can enroll a machine into that org until the token is revoked or
//     spent. It is a bearer credential and is treated like one: restrictive
//     ACL on disk, never logged, destroyed after use.
//   - Integrity comes from the MSI's Authenticode signature, NOT from anything
//     in this file. An attacker who can rewrite the profile inside an UNSIGNED
//     installer can point an agent at their own control server. Signing the
//     installer (Phase 4) is what closes that, which is why the Signature
//     field below is reserved rather than pretending to be a trust boundary.
//   - Pinning travels WITH the URL: a profile that names a control server also
//     names its certificate fingerprint, so a tampered URL alone does not buy
//     a working man-in-the-middle.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ProfileVersion is the contract version this build understands. An unknown
// version is REFUSED rather than best-effort parsed: a profile is a security
// boundary, and guessing at a format written by a newer server is how an
// agent ends up enrolled somewhere unintended.
const ProfileVersion = 1

// Profile is the on-disk provisioning payload. Field names are the frozen
// wire contract — see docs/MSI-PROVISIONING.md; NimbusControl generates this.
type Profile struct {
	ProfileVersion  int    `json:"profile_version"`
	OrgName         string `json:"org_name,omitempty"`
	ControlURL      string `json:"control_server_url"`
	CertFingerprint string `json:"control_cert_fp,omitempty"`
	EnrollToken     string `json:"enroll_token"`
	DefaultMode     string `json:"default_backup_mode,omitempty"`
	IssuedAt        string `json:"issued_at,omitempty"`
	IssuedBy        string `json:"issued_by,omitempty"`

	// Signature is reserved. It is NOT verified today and must not be relied
	// on: verifying it needs a trust anchor the agent would have to possess
	// before it has ever spoken to the server, which is the same distribution
	// problem the MSI signature already solves.
	Signature string `json:"signature,omitempty"`
}

// ParseProfile decodes and validates a profile. Every failure is a refusal —
// there is no partially-applied profile, because half a control-plane identity
// is worse than none.
func ParseProfile(raw []byte) (*Profile, error) {
	var p Profile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields() // a field we do not understand may be load-bearing
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("provisioning profile is not valid JSON: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate enforces the contract. Kept separate from parsing so a generator
// (or a test) can check a profile it built in memory.
func (p *Profile) Validate() error {
	if p.ProfileVersion != ProfileVersion {
		return fmt.Errorf("unsupported provisioning profile version %d (this agent understands %d)",
			p.ProfileVersion, ProfileVersion)
	}
	if strings.TrimSpace(p.ControlURL) == "" {
		return fmt.Errorf("provisioning profile has no control_server_url")
	}
	u, err := url.Parse(p.ControlURL)
	if err != nil {
		return fmt.Errorf("provisioning profile control_server_url is not a URL: %w", err)
	}
	// https only. The enrollment token crosses this connection, and an agent
	// that will speak plaintext once will do it on every endpoint the MSI
	// touches.
	if u.Scheme != "https" {
		return fmt.Errorf("provisioning profile control_server_url must be https (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("provisioning profile control_server_url has no host")
	}
	if strings.TrimSpace(p.EnrollToken) == "" {
		return fmt.Errorf("provisioning profile has no enroll_token")
	}
	if fp := NormalizeFingerprint(p.CertFingerprint); p.CertFingerprint != "" {
		if len(fp) != 64 {
			return fmt.Errorf("provisioning profile control_cert_fp must be a SHA-256 hex digest, got %d hex chars", len(fp))
		}
		for _, r := range fp {
			if !strings.ContainsRune("0123456789abcdef", r) {
				return fmt.Errorf("provisioning profile control_cert_fp is not hexadecimal")
			}
		}
	}
	switch p.DefaultMode {
	case "", "directory", "machine":
	default:
		return fmt.Errorf("provisioning profile default_backup_mode must be directory or machine, got %q", p.DefaultMode)
	}
	return nil
}

// NormalizeFingerprint strips the colon grouping people paste from OpenSSL and
// lowercases, so "AA:BB:.." and "aabb.." compare equal.
func NormalizeFingerprint(fp string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(fp), ":", ""))
}

// Redacted renders a profile for logs. The enrollment token is a bearer
// credential: it never appears, not even truncated, because a prefix is enough
// to narrow a brute force and log files travel further than anyone expects.
func (p *Profile) Redacted() string {
	org := p.OrgName
	if org == "" {
		org = "(unnamed)"
	}
	mode := p.DefaultMode
	if mode == "" {
		mode = "(unset)"
	}
	pin := "none"
	if p.CertFingerprint != "" {
		pin = "pinned"
	}
	issued := p.IssuedAt
	if issued == "" {
		issued = "(unknown)"
	}
	return fmt.Sprintf("org=%s url=%s cert=%s default_mode=%s issued_at=%s issued_by=%s token=[redacted]",
		org, p.ControlURL, pin, mode, issued, p.IssuedBy)
}

// Age reports how long ago the profile was issued. A profile is a bearer
// credential, so a caller may reasonably refuse a very old one; ok is false
// when the profile carries no usable timestamp.
func (p *Profile) Age(now time.Time) (d time.Duration, ok bool) {
	if p.IssuedAt == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, p.IssuedAt)
	if err != nil {
		return 0, false
	}
	return now.Sub(t), true
}
