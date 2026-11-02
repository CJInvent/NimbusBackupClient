package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	ConnectionTimeout = 5 * time.Second  // opening the pipe, and IsServiceAvailable
	RequestTimeout    = 30 * time.Second // Backup returns immediately, safe
)

// Client is the console's half of the local API (docs/V4-LOCAL-API.md §3).
//
// HTTP/1.1, ONE REQUEST PER CONNECTION, written and read by hand over a
// connection c.dial opens: req.Write, then http.ReadResponse, then the
// connection is closed with the body. There is no http.Client and no
// http.Transport here on purpose. A Transport is connection pooling, proxies,
// TLS and HTTP/2, none of which a local pipe needs, and linking it is most of
// what made the console 1.5 MB larger than it had to be (V4-CONSOLE §6 D2).
// One request per connection is also what keeps identity simple on the other
// side: the service reads the caller from the connection, and a connection
// never carries a second request whose identity could be confused with the
// first's.
type Client struct {
	// baseURL names the service in the requests: the Host header and the
	// scheme. The connection is whatever dial opens.
	baseURL string
	dial    func(timeout time.Duration) (net.Conn, error)
}

// NewClient is the client of this machine's service: the named pipe, opened
// so that the service can identify the console but not act as it, and
// refused unless the process serving it is the NimbusBackup service running
// as SYSTEM (pipe_windows.go, verifyServer).
func NewClient() *Client {
	return &Client{
		baseURL: "http://nimbus-service",
		dial: func(timeout time.Duration) (net.Conn, error) {
			return dialPipe(PipeName, serviceCheck(), timeout)
		},
	}
}

// NewClientAt is a client of a fake service on a loopback TCP address
// (httptest), for TESTS of what the client sends and how it reads the
// answers. Production never calls it: nothing on TCP has a Windows identity,
// and the service no longer listens on a port. Pinned to tests by
// TestOnlyTestsUseFixedCallerContext.
func NewClientAt(baseURL string) *Client {
	host := baseURL
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	return &Client{
		baseURL: baseURL,
		dial: func(timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("tcp", host, timeout)
		},
	}
}

// send makes one request on a connection of its own. The whole exchange,
// writing the request and reading every byte of the answer, is bounded by
// timeout: the deadline is set on the connection, so a service that stops
// answering mid-body ends the read instead of holding the console.
func (c *Client) send(method, path string, body []byte, timeout time.Duration) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// "Connection: close": the service closes after answering, and says so.
	req.Close = true

	conn, err := c.dial(ConnectionTimeout)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		// A connection that cannot be bounded could hang the console for
		// ever on a stuck service. Refuse it rather than find out.
		_ = conn.Close()
		return nil, fmt.Errorf("the connection to the service cannot be bounded: %w", err)
	}
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp.Body = &connBody{ReadCloser: resp.Body, conn: conn}
	return resp, nil
}

func (c *Client) get(path string) (*http.Response, error) {
	return c.send(http.MethodGet, path, nil, RequestTimeout)
}

func (c *Client) post(path string, body []byte) (*http.Response, error) {
	if body == nil {
		body = []byte{}
	}
	return c.send(http.MethodPost, path, body, RequestTimeout)
}

// connBody closes the connection with the body: the connection was the
// request's alone.
type connBody struct {
	io.ReadCloser
	conn net.Conn
}

func (b *connBody) Close() error {
	err := b.ReadCloser.Close()
	if cerr := b.conn.Close(); err == nil {
		err = cerr
	}
	return err
}

// IsServiceAvailable checks if the local service is running, and is the
// service (an untrusted server answers false: dialPipe refuses it).
func (c *Client) IsServiceAvailable() bool {
	resp, err := c.send(http.MethodGet, "/status", nil, ConnectionTimeout)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode == http.StatusOK
}

// GetStatus retrieves the service status
func (c *Client) GetStatus() (*StatusResponse, error) {
	resp, err := c.get("/status")
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
	resp, err := c.get(path)
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
	resp, err := c.get("/connections")
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
