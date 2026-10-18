package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// StatusError is a handler's refusal with the HTTP status it should travel
// under. The routes added for the console (docs/V4-CONSOLE.md §4.1) answer
// "no such server", "this machine's PBS settings belong to its control
// server" and "that is not a URL" as three different statuses, because the
// console acts differently on each; a flat 500 would make all three read like
// a crash in the service.
//
// The message is Err's, unchanged: the console shows it verbatim, and the
// [NB-xxxx] codes inside some of them are what it translates.
type StatusError struct {
	Code int
	Err  error
}

func (e *StatusError) Error() string { return e.Err.Error() }
func (e *StatusError) Unwrap() error { return e.Err }

// Refuse wraps err with the status it should be answered with.
func Refuse(code int, err error) error { return &StatusError{Code: code, Err: err} }

// statusOf picks the status for a handler error: the one it asked for, or
// fallback when it asked for none.
func statusOf(err error, fallback int) int {
	var se *StatusError
	if errors.As(err, &se) && se.Code >= 400 && se.Code <= 599 {
		return se.Code
	}
	return fallback
}

// getJSON and postJSON are the client half of every route added for the
// console: one request, the service's own error text on a refusal, the body
// decoded into out. The older client methods each carry their own copy of
// this; new ones share it.
func (c *Client) getJSON(path string, out any) error {
	resp, err := c.httpClient.Get(c.baseURL + path)
	if err != nil {
		return fmt.Errorf("[NB-2007] communication with the service failed :: %w", err)
	}
	return decodeServiceResponse(resp, out)
}

func (c *Client) postJSON(path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("failed to encode request: %w", err)
	}
	resp, err := c.httpClient.Post(c.baseURL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("[NB-2007] communication with the service failed :: %w", err)
	}
	return decodeServiceResponse(resp, out)
}

// decodeServiceResponse returns the service's error message for a non-200
// answer (as a StatusError, so a caller can still tell 404 from 409), and
// decodes a 200 into out.
func decodeServiceResponse(resp *http.Response, out any) error {
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read the service's response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e ErrorResponse
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return Refuse(resp.StatusCode, errors.New(e.Error))
		}
		return Refuse(resp.StatusCode, fmt.Errorf("the service answered HTTP %d", resp.StatusCode))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("failed to decode the service's response: %w", err)
	}
	return nil
}
