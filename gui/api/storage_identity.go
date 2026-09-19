package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

type storageIdentityHandler interface {
	GetStorageIdentityStatus() (map[string]interface{}, error)
	ApproveStorageIdentityFromMap(map[string]interface{}) error
}

func (s *Server) handleStorageIdentity(w http.ResponseWriter, r *http.Request) {
	h, ok := s.app.(storageIdentityHandler)
	if !ok {
		s.writeError(w, "storage identity unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodGet {
		status, err := h.GetStorageIdentityStatus()
		if err != nil {
			s.writeError(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		s.writeJSON(w, status, http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input map[string]interface{}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		s.writeError(w, "invalid storage approval", http.StatusBadRequest)
		return
	}
	if err := h.ApproveStorageIdentityFromMap(input); err != nil {
		s.writeError(w, err.Error(), http.StatusConflict)
		return
	}
	s.writeJSON(w, map[string]bool{"ok": true}, http.StatusOK)
}
func (c *Client) GetStorageIdentity() (map[string]interface{}, error) {
	resp, err := c.httpClient.Get(c.baseURL + "/storage/identity")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("storage status HTTP %d", resp.StatusCode)
	}
	var out map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}
func (c *Client) ApproveStorageIdentity(input map[string]interface{}) error {
	b, err := json.Marshal(input)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Post(c.baseURL+"/storage/identity", "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var body ErrorResponse
		if json.NewDecoder(resp.Body).Decode(&body) == nil && body.Error != "" {
			return fmt.Errorf("[NB-2013] storage identity requires manual intervention :: %s", body.Error)
		}
		return fmt.Errorf("[NB-2013] storage approval refused (HTTP %d)", resp.StatusCode)
	}
	return nil
}
