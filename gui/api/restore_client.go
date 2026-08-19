package api

// The console's half of the restore seam.
//
// These four methods are the ONLY way the GUI reaches a restore, and they hand
// back raw JSON rather than decoded types on purpose: the shapes belong to the
// engine (package main, `service` build), and a copy of them here would be a
// second definition to keep in step with the first. The binding that called
// the engine directly now decodes the same struct it used to return.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RestoreRequestTimeout bounds a QUERY, not a job.
//
// The package default (30s) is too short for one of these: a snapshot listing
// downloads and walks a catalog, and an image partition scan pulls the $MFT.
// Both are minutes on a large snapshot over a slow link. Jobs are unaffected —
// they return an id immediately and are polled.
const RestoreRequestTimeout = 30 * time.Minute

// restoreHTTP is a second client with the long timeout, sharing the token
// transport. Kept separate so an ordinary status call cannot hang for half an
// hour because restore needed room.
func (c *Client) restoreHTTP() *http.Client {
	return &http.Client{Timeout: RestoreRequestTimeout, Transport: c.httpClient.Transport}
}

func (c *Client) postRestore(hc *http.Client, path string, call restoreCall) ([]byte, error) {
	body, err := json.Marshal(call)
	if err != nil {
		return nil, fmt.Errorf("could not encode the %s request: %w", call.Op, err)
	}
	resp, err := hc.Post(c.baseURL+path, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("the backup service could not be reached: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read the service's reply: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, restoreError(raw, resp.StatusCode)
	}
	return raw, nil
}

// restoreError turns the service's refusal into the error the user reads.
//
// The service's own words are used verbatim when it sent any: it is the side
// that knows whether this was a policy refusal, a missing key or an unreadable
// snapshot, and paraphrasing it here would flatten all three into "restore
// failed".
func restoreError(raw []byte, status int) error {
	var er ErrorResponse
	if err := json.Unmarshal(raw, &er); err == nil && er.Error != "" {
		return fmt.Errorf("%s", er.Error)
	}
	return fmt.Errorf("the backup service refused the request (HTTP %d)", status)
}

// RestoreQuery runs a bounded restore operation and returns its raw JSON.
func (c *Client) RestoreQuery(op string, params any) (json.RawMessage, error) {
	encoded, err := encodeParams(op, params)
	if err != nil {
		return nil, err
	}
	raw, err := c.postRestore(c.restoreHTTP(), "/restore/query", restoreCall{Op: op, Params: encoded})
	if err != nil {
		return nil, err
	}
	var out restoreQueryResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("could not read the result of %s: %w", op, err)
	}
	return out.Result, nil
}

// RestoreJobStart begins long-running work and returns its job id.
func (c *Client) RestoreJobStart(op string, params any) (string, error) {
	encoded, err := encodeParams(op, params)
	if err != nil {
		return "", err
	}
	// The DEFAULT timeout here, not the long one: starting a job is a
	// registration and returns at once. A start that hangs for 30 minutes
	// would be a broken service, not a slow restore.
	raw, err := c.postRestore(c.httpClient, "/restore/job", restoreCall{Op: op, Params: encoded})
	if err != nil {
		return "", err
	}
	var out restoreJobStartResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("could not read the job id for %s: %w", op, err)
	}
	if out.JobID == "" {
		return "", fmt.Errorf("the service started %s but named no job to watch", op)
	}
	return out.JobID, nil
}

// RestoreJobState polls a running job.
func (c *Client) RestoreJobState(id string) (*RestoreJobState, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/restore/job/" + id)
	if err != nil {
		return nil, fmt.Errorf("the backup service could not be reached: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read the service's reply: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, restoreError(raw, resp.StatusCode)
	}
	var st RestoreJobState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("could not read the job state: %w", err)
	}
	return &st, nil
}

// RestoreControl acts on work already running (the Cancel buttons).
func (c *Client) RestoreControl(op string, params any) error {
	encoded, err := encodeParams(op, params)
	if err != nil {
		return err
	}
	_, err = c.postRestore(c.httpClient, "/restore/control", restoreCall{Op: op, Params: encoded})
	return err
}

func encodeParams(op string, params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("could not encode the parameters for %s: %w", op, err)
	}
	return raw, nil
}
