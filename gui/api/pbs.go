package api

import (
	"encoding/json"
	"errors"
	"net/http"
)

// PBS servers, read and probed through the service (docs/V4-CONSOLE.md §4.1).
//
// Until these routes the console answered all three questions itself: it read
// its own decrypted copy of config.json for the list, built a pbscommon client
// with the stored token to test a server, and dialled an arbitrary URL from the
// user's session to discover a certificate. After V4-CONSOLE C2 it asks here,
// and the token never leaves the service.
//
//	GET  /pbs/servers   the configured servers, sanitized, and the default id
//	POST /pbs/test      {id}  one server's connection test
//	POST /pbs/discover  {url} a server's leaf-certificate fingerprint (TLS
//	                          handshake only; no token is sent)
//
// In data-path proxy mode the machine has no PBS settings (D2 of the data
// path): the list is empty and says why, and test and discover are refused.

// PBSServerInfo is one configured PBS server as the console may see it.
//
// THERE IS NO SECRET FIELD, and that is the sanitization. The service's own
// record carries the API token; this type cannot, so no handler, present or
// future, can put it on the wire by forgetting to blank it. SecretSet says
// whether one is stored, which is all a form needs to show "unchanged".
type PBSServerInfo struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	BaseURL         string `json:"baseurl"`
	CertFingerprint string `json:"certfingerprint"`
	AuthID          string `json:"authid"`
	Datastore       string `json:"datastore"`
	Namespace       string `json:"namespace"`
	Description     string `json:"description,omitempty"`
	SecretSet       bool   `json:"secret_set"`
}

// PBSServerSave is what the console sends to POST /pbs/save: a server as the
// setup form edits it. The secret is the only one that ever crosses the local
// API, typed by the user; empty means "keep the stored one".
type PBSServerSave struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	BaseURL         string `json:"baseurl"`
	CertFingerprint string `json:"certfingerprint"`
	AuthID          string `json:"authid"`
	Secret          string `json:"secret"`
	Datastore       string `json:"datastore"`
	Namespace       string `json:"namespace"`
	Description     string `json:"description,omitempty"`
}

// PBSServersResponse is GET /pbs/servers.
type PBSServersResponse struct {
	Servers   []PBSServerInfo `json:"servers"`
	DefaultID string          `json:"default_id"`
	// DatapathProxy is true when this machine reaches PBS only through its
	// control server. Servers is then empty, and that is the reason, not a
	// missing configuration.
	DatapathProxy bool `json:"datapath_proxy"`
}

// PBSTestResponse is POST /pbs/test. A test that ran and failed is an answer,
// not an error of this API: it comes back 200 with OK false.
type PBSTestResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// CertUntrusted is true when the failure was the server's certificate not
	// verifying and no fingerprint is pinned: the case the console offers to
	// resolve by discovering and pinning the fingerprint (trust on first use).
	// Carried as a field so the console stops recognising it by matching words
	// in an error message.
	CertUntrusted bool `json:"cert_untrusted,omitempty"`
}

// PBSHandler is the service side of these routes. Optional, like
// RestoreHandler: a server whose app lacks it answers 501.
type PBSHandler interface {
	PBSServersForAPI() PBSServersResponse
	// TestPBSServerForAPI refuses an unknown id with 404 and proxy mode with
	// 409 (Refuse); a test that ran returns its outcome and a nil error.
	TestPBSServerForAPI(id string) (PBSTestResponse, error)
	// DiscoverPBSFingerprintForAPI refuses a malformed URL with 400 and proxy
	// mode with 409; an unreachable server is a 502.
	DiscoverPBSFingerprintForAPI(url string) (string, error)
}

func (s *Server) pbsHandler(w http.ResponseWriter) (PBSHandler, bool) {
	h, ok := s.app.(PBSHandler)
	if !ok {
		s.writeError(w, "this agent does not serve PBS server queries", http.StatusNotImplemented)
		return nil, false
	}
	return h, true
}

func (s *Server) handleListPBSServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h, ok := s.pbsHandler(w)
	if !ok {
		return
	}
	out := h.PBSServersForAPI()
	if out.Servers == nil || out.DatapathProxy {
		// Proxy mode holds no PBS settings, whatever config.json still says.
		// And a nil slice marshals to null, which every consumer would have
		// to guard.
		out.Servers = []PBSServerInfo{}
	}
	s.writeJSON(w, out, http.StatusOK)
}

type pbsIDRequest struct {
	ID string `json:"id"`
}

type pbsDiscoverRequest struct {
	URL string `json:"url"`
}

// PBSFingerprintResponse is POST /pbs/discover.
type PBSFingerprintResponse struct {
	Fingerprint string `json:"fingerprint"`
}

func (s *Server) handleTestPBS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req pbsIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID == "" {
		s.writeError(w, "id is required", http.StatusBadRequest)
		return
	}
	h, ok := s.pbsHandler(w)
	if !ok {
		return
	}
	out, err := h.TestPBSServerForAPI(req.ID)
	if err != nil {
		s.writeError(w, err.Error(), statusOf(err, http.StatusInternalServerError))
		return
	}
	s.writeJSON(w, out, http.StatusOK)
}

func (s *Server) handleDiscoverPBS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req pbsDiscoverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.URL == "" {
		s.writeError(w, "url is required", http.StatusBadRequest)
		return
	}
	h, ok := s.pbsHandler(w)
	if !ok {
		return
	}
	fp, err := h.DiscoverPBSFingerprintForAPI(req.URL)
	if err != nil {
		s.writeError(w, err.Error(), statusOf(err, http.StatusBadGateway))
		return
	}
	if fp == "" {
		// A handler that "succeeded" with nothing would have the console
		// offer to pin an empty fingerprint.
		s.writeError(w, "the server presented no certificate", http.StatusBadGateway)
		return
	}
	s.writeJSON(w, PBSFingerprintResponse{Fingerprint: fp}, http.StatusOK)
}

// ListPBSServers returns the configured servers (sanitized) and the default.
func (c *Client) ListPBSServers() (*PBSServersResponse, error) {
	var out PBSServersResponse
	if err := c.getJSON("/pbs/servers", &out); err != nil {
		return nil, err
	}
	if out.Servers == nil {
		out.Servers = []PBSServerInfo{}
	}
	return &out, nil
}

// TestPBSServer runs one server's connection test in the service. A test that
// ran and failed is returned as an error carrying the PBS failure, so a caller
// that only wants "did it work" can treat the two alike; CertUntrusted is on
// the response for the caller that needs to tell.
func (c *Client) TestPBSServer(id string) (*PBSTestResponse, error) {
	var out PBSTestResponse
	if err := c.postJSON("/pbs/test", pbsIDRequest{ID: id}, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		msg := out.Error
		if msg == "" {
			msg = "the connection test failed"
		}
		return &out, errors.New(msg)
	}
	return &out, nil
}

// DiscoverPBSFingerprint returns the SHA-256 fingerprint of the certificate
// the server at url presents.
func (c *Client) DiscoverPBSFingerprint(url string) (string, error) {
	var out PBSFingerprintResponse
	if err := c.postJSON("/pbs/discover", pbsDiscoverRequest{URL: url}, &out); err != nil {
		return "", err
	}
	return out.Fingerprint, nil
}
