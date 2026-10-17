package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Self-update, the agent's half of the wire (NimbusControl
// docs/V4-CLIENT-RELEASES.md §7, AGENT-API "Versions, the line check and
// self-update"). FROZEN ACROSS COMPATIBILITY BUMPS on the server: an agent
// built today must be able to read these from any later server.

// UpdateOffer is the check-in's `update`: the version this machine should
// install, and what its MSI must measure.
type UpdateOffer struct {
	Version string `json:"version"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

// AgentLine is the check-in's `agent_line`: this agent's version against the
// server's L.CC. Anything but "ok" means the server will run nothing on this
// machine until it updates.
type AgentLine struct {
	State string `json:"state"` // ok | older | newer
	Needs string `json:"needs"`
}

// Compatible is true for "ok" and for a server too old to say (nil).
func (l *AgentLine) Compatible() bool { return l == nil || l.State == "ok" }

// UpdateResult is POST /api/agent/v1/update/result.
type UpdateResult struct {
	Version  string `json:"version"`
	Outcome  string `json:"outcome"` // started | installed | failed
	ExitCode *int   `json:"exit_code,omitempty"`
	LogTail  string `json:"log_tail,omitempty"`
}

// MaxUpdateBytes bounds what DownloadUpdate will write, whatever the offer
// says.
const MaxUpdateBytes = 512 << 20

// DownloadUpdate fetches the offer's MSI into path and checks it against the
// offer: size and SHA-256. On any mismatch the file is removed and an error
// returned; nothing that did not verify is left to run.
func (c *Client) DownloadUpdate(ctx context.Context, o UpdateOffer, path string) error {
	if _, ok := ParseVersion(o.Version); !ok || o.Size <= 0 || o.Size > MaxUpdateBytes || len(o.SHA256) != 64 {
		return fmt.Errorf("controlplane: malformed update offer %+v", o)
	}
	if c.AgentID <= 0 || c.Secret == "" {
		return errors.New("controlplane: not enrolled")
	}
	u := strings.TrimRight(c.BaseURL, "/") + "/api/agent/v1/update/msi?version=" + url.QueryEscape(o.Version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %d.%s", c.AgentID, c.Secret))
	ua := c.UserAgent
	if ua == "" {
		ua = "NimbusBackupClient"
	}
	req.Header.Set("User-Agent", ua)
	// The shared client's 60 s ceiling is for JSON; an MSI on a slow link
	// gets its own, with the same pinned TLS.
	hc := &http.Client{Timeout: 30 * time.Minute, Transport: c.http().Transport}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("controlplane: update download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("controlplane: update download: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) // #nosec G304 -- path chosen by the updater, not the server
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, o.Size+1))
	closeErr := f.Close()
	sum := hex.EncodeToString(h.Sum(nil))
	switch {
	case copyErr != nil:
		err = fmt.Errorf("controlplane: update download: %w", copyErr)
	case closeErr != nil:
		err = closeErr
	case n != o.Size:
		err = fmt.Errorf("controlplane: update is %d bytes, the offer says %d", n, o.Size)
	case !strings.EqualFold(sum, o.SHA256):
		err = errors.New("controlplane: update SHA-256 does not match the offer")
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

// ReportUpdate tells the server how an update went.
func (c *Client) ReportUpdate(r UpdateResult) error {
	return c.post("/api/agent/v1/update/result", r, nil, true)
}
