package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	DefaultServiceURL = "http://127.0.0.1:18765"
	ConnectionTimeout = 5 * time.Second  // Increased for service startup
	RequestTimeout    = 30 * time.Second // Backup returns immediately, safe
)

// Client handles communication with the local service
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new API client for the service on this machine.
func NewClient(tokenPath string) *Client {
	return NewClientAt(DefaultServiceURL, tokenPath)
}

// NewClientAt is NewClient for a service at another address: a test's fake
// service. The token is still read from tokenPath on every request.
func NewClientAt(baseURL, tokenPath string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:   RequestTimeout,
			Transport: &tokenTransport{tokenPath: tokenPath, base: http.DefaultTransport},
		},
	}
}

// IsServiceAvailable checks if the local service is running
func (c *Client) IsServiceAvailable() bool {
	client := &http.Client{
		Timeout:   ConnectionTimeout,
		Transport: c.httpClient.Transport, // reuse the token-injecting transport
	}

	resp, err := client.Get(c.baseURL + "/status")
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode == http.StatusOK
}

// GetStatus retrieves the service status
func (c *Client) GetStatus() (*StatusResponse, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/status")
	if err != nil {
		return nil, fmt.Errorf("failed to connect to service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("service returned error: %d", resp.StatusCode)
	}

	var status StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &status, nil
}

// StartBackup asks the service to run a backup. A refusal (locked, the
// control server said no, a bad request) is a StatusError carrying the
// service's own sentence.
func (c *Client) StartBackup(req *BackupRequest) (*BackupResponse, error) {
	var out BackupResponse
	if err := c.postJSON("/backup", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelBackup asks the service to stop the running backup. The service cancels
// the active backup's context; the engine unwinds before committing the index
// and releases the VSS snapshot, so nothing is left behind.
func (c *Client) CancelBackup() error {
	return c.postJSON("/backup/cancel", struct{}{}, nil)
}

// GetScheduledJobs returns this machine's local scheduled jobs, whole.
func (c *Client) GetScheduledJobs() ([]ScheduledJob, error) {
	var out JobsResponse
	if err := c.getJSON("/jobs", &out); err != nil {
		return nil, err
	}
	if out.Jobs == nil {
		out.Jobs = []ScheduledJob{}
	}
	return out.Jobs, nil
}

// CreateJob creates a new scheduled job (an api.ScheduledJob, or the same
// fields as a map).
func (c *Client) CreateJob(job any) error {
	return c.postJSON("/jobs/create", job, nil)
}

// PinFingerprint asks the service to persist a pinned certificate fingerprint
// for a PBS server. The service validates it and is the only writer of
// config.json.
func (c *Client) PinFingerprint(id, fingerprint string) error {
	return c.postJSON("/pbs/fingerprint", map[string]string{"id": id, "fingerprint": fingerprint}, nil)
}

// SavePBSServer upserts a PBS server in the service. An empty secret keeps the
// stored one.
func (c *Client) SavePBSServer(server any) error {
	return c.postJSON("/pbs/save", server, nil)
}

// DeletePBSServer removes a PBS server by id.
func (c *Client) DeletePBSServer(id string) error {
	return c.deleteJSON("/pbs/delete/" + url.PathEscape(id))
}

// SetDefaultPBS sets the default PBS server.
func (c *Client) SetDefaultPBS(id string) error {
	return c.postJSON("/pbs/default", map[string]string{"id": id}, nil)
}

// UpdateJob updates an existing scheduled job.
func (c *Client) UpdateJob(job any) error {
	return c.postJSON("/jobs/update", job, nil)
}

// DeleteJob deletes a scheduled job by id.
func (c *Client) DeleteJob(jobID string) error {
	return c.deleteJSON("/jobs/delete/" + url.PathEscape(jobID))
}

// GetControlPlaneStatus fetches the control-server connectivity snapshot
// (with the storage identity and the effective policy) from the service.
func (c *Client) GetControlPlaneStatus() (map[string]interface{}, error) {
	var out map[string]interface{}
	if err := c.getJSON("/controlplane/status", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SaveControlPlane sends control-server settings to the service, which
// persists them and restarts its check-in loop.
func (c *Client) SaveControlPlane(m map[string]interface{}) error {
	return c.postJSON("/controlplane/save", m, nil)
}

// GetActiveRuns returns every backup currently in flight on this machine,
// whatever started it.
//
// This is the call the GUI needs and did not have. The old GetBackupStatus
// answers "how is THIS job id doing", which a front end can only ask about a
// run it started itself — so a scheduled backup was invisible
// (docs/V4-PIPELINE.md §2.1).
func (c *Client) GetActiveRuns() (*RunsResponse, error) {
	return c.getRuns("/runs/active")
}

// GetRecentRuns returns runs started within the last `days`, live ones
// included, for the seven-day status panel.
func (c *Client) GetRecentRuns(days int) (*RunsResponse, error) {
	if days < 1 {
		days = runsWindowDefaultDays
	}
	return c.getRuns("/runs/recent?days=" + strconv.Itoa(days))
}

func (c *Client) getRuns(path string) (*RunsResponse, error) {
	resp, err := c.httpClient.Get(c.baseURL + path)
	if err != nil {
		return nil, fmt.Errorf("failed to get runs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("service returned error: %d", resp.StatusCode)
	}

	var out RunsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}

// GetConnections returns what this machine can currently reach: every PBS
// destination and the control plane. Served from the service's cache, so it
// is safe to poll.
func (c *Client) GetConnections() (*ConnectionsResponse, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/connections")
	if err != nil {
		return nil, fmt.Errorf("failed to get connections: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("service returned error: %d", resp.StatusCode)
	}

	var out ConnectionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &out, nil
}
