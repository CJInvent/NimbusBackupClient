package controlplane

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// Client talks to NimbusControl. All calls are outbound HTTPS; the server
// never dials in (CGNAT/Starlink safe). Authentication after enrollment is
//
//	Authorization: Bearer <agent_id>.<secret>
//
// The secret is handed out exactly once at enrollment — persist it via the
// TPM-backed secret store, never plaintext on disk.
type Client struct {
	BaseURL string // e.g. https://nimbus.dpsol.com
	AgentID int64
	Secret  string

	// Optional SHA-256 pin of the control server's certificate (hex).
	// Empty = system trust store (the normal case behind HAProxy with a
	// real certificate).
	CertFingerprint string

	// UserAgent is sent on every request; defaults to "NimbusBackupClient".
	UserAgent string

	httpc *http.Client
}

// MaxBodyBytes mirrors the server's request cap; responses are read with
// the same bound so a MITM'd or broken server can't balloon agent memory.
const MaxBodyBytes = 256 << 10

func (c *Client) http() *http.Client {
	if c.httpc != nil {
		return c.httpc
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CertFingerprint != "" {
		want := strings.ToLower(strings.ReplaceAll(c.CertFingerprint, ":", ""))
		// Pin: accept any chain whose LEAF matches the fingerprint. System
		// verification is disabled because the pin IS the trust anchor.
		tlsCfg.InsecureSkipVerify = true
		tlsCfg.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return fmt.Errorf("controlplane: no peer certificate")
			}
			got := sha256.Sum256(raw[0])
			if hex.EncodeToString(got[:]) != want {
				return fmt.Errorf("controlplane: certificate fingerprint mismatch")
			}
			return nil
		}
	}
	c.httpc = &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg, ForceAttemptHTTP2: true},
	}
	return c.httpc
}

// Enroll redeems a one-time token for an identity. On success the caller
// MUST persist resp.AgentID + resp.Secret via the secure store before
// making any other call.
func (c *Client) Enroll(req EnrollRequest) (*EnrollResponse, error) {
	var resp EnrollResponse
	if err := c.post("/api/agent/v1/enroll", req, &resp, false); err != nil {
		return nil, err
	}
	if resp.AgentID <= 0 || resp.Secret == "" {
		return nil, fmt.Errorf("controlplane: malformed enroll response")
	}
	return &resp, nil
}

// Checkin is the heartbeat: reports inventory, drains the command queue,
// and returns the current interval + policy. Call every CheckinSeconds.
func (c *Client) Checkin(req CheckinRequest) (*CheckinResponse, error) {
	var resp CheckinResponse
	if err := c.post("/api/agent/v1/checkin", req, &resp, true); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ReportRun posts a phase change. Safe to retry: the server upserts by
// RunUUID and its state machine is forward-only.
func (c *Client) ReportRun(r RunReport) error {
	return c.post("/api/agent/v1/runs", r, nil, true)
}

// PostCommandResult completes a command (idempotence contract: only 'sent'
// commands accept a result; anything else is a 404 we treat as done).
func (c *Client) PostCommandResult(id int64, res CommandResult) error {
	err := c.post(fmt.Sprintf("/api/agent/v1/commands/%d/result", id), res, nil, true)
	if he := (*httpError)(nil); asHTTPError(err, &he) && he.status == 404 {
		return nil // already resulted / expired — not an error for the caller
	}
	return err
}

// ---------------------------------------------------------------- plumbing

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return fmt.Sprintf("controlplane: HTTP %d: %s", e.status, e.msg) }

func asHTTPError(err error, out **httpError) bool {
	he, ok := err.(*httpError)
	if ok {
		*out = he
	}
	return ok
}

// post sends JSON with retry. Backoff ladder: 2s, 8s, 30s (+ jitter) on
// 429/5xx/transport errors — per the contract, never tight-loop. 4xx other
// than 429 is returned immediately (retrying a rejected payload is noise).
func (c *Client) post(path string, in, out interface{}, authed bool) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("controlplane: encode: %w", err)
	}

	delays := []time.Duration{0, 2 * time.Second, 8 * time.Second, 30 * time.Second}
	var last error
	for attempt, base := range delays {
		if base > 0 {
			time.Sleep(base + jitter(base/2))
		}
		last = c.once(path, body, out, authed)
		if last == nil {
			return nil
		}
		var he *httpError
		if asHTTPError(last, &he) && he.status != 429 && he.status < 500 {
			return last // non-retryable
		}
		_ = attempt
	}
	return last
}

func (c *Client) once(path string, body []byte, out interface{}, authed bool) error {
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	ua := c.UserAgent
	if ua == "" {
		ua = "NimbusBackupClient"
	}
	req.Header.Set("User-Agent", ua)
	if authed {
		if c.AgentID <= 0 || c.Secret == "" {
			return fmt.Errorf("controlplane: not enrolled")
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %d.%s", c.AgentID, c.Secret))
	}

	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("controlplane: transport: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes))
	if err != nil {
		return fmt.Errorf("controlplane: read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Server error bodies are {"error": "..."} — surface the message.
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = http.StatusText(resp.StatusCode)
		}
		return &httpError{status: resp.StatusCode, msg: e.Error}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("controlplane: decode: %w", err)
		}
	}
	return nil
}

// jitter returns a uniform random duration in [0, max) from crypto/rand —
// no math/rand seeding concerns, and the amounts are tiny.
func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return max / 2
	}
	return time.Duration(n.Int64())
}

// NewRunUUID returns a RFC 4122 v4 UUID for RunReport.RunUUID.
func NewRunUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is a broken platform; timestamp fallback keeps
		// uniqueness good enough to not lose a run report.
		now := time.Now().UnixNano()
		for i := 0; i < 8; i++ {
			b[i] = byte(now >> (8 * i))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
