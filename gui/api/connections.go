package api

import (
	"net/http"
	"sync"
	"time"
)

// Connections: what this machine can currently reach.
//
// The first panel a locked console shows (docs/V4-PIPELINE.md §4). It answers
// the question a technician standing at the machine actually has — "is this
// thing talking to anything?" — without giving them any control over it.
//
// WHY A SEPARATE ROUTE FROM /status: /status describes the SERVICE (running,
// version, active jobs) and is answered from memory. This describes the world
// outside it and needs network probes. Folding probes into /status would make
// a liveness check depend on a PBS server being up, which is exactly backwards.
//
// TRI-STATE, NOT BOOLEAN. `reachable` is a *bool: true, false, or unknown.
// A probe that could not be built or has not run yet is not the same fact as a
// server that answered "no", and flattening them would render a tile that says
// OFFLINE for a machine nobody has checked. cpCheckPBSReachability already
// makes this distinction internally and returns nil for "the check itself
// failed"; this preserves it all the way to the panel.

// PBSConnection is one backup destination's reachability.
type PBSConnection struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Host is the destination as configured, for a panel that needs to tell
	// two servers apart. It is a URL, never a credential.
	Host      string `json:"host,omitempty"`
	Datastore string `json:"datastore,omitempty"`
	IsDefault bool   `json:"is_default"`
	// Reachable is nil when unknown — never probed, or the probe could not
	// be constructed. See the note above.
	Reachable *bool `json:"reachable"`
	// CheckedAt is when Reachable was last established. Zero means never,
	// and a panel should say so rather than implying freshness.
	CheckedAt time.Time `json:"checked_at,omitempty"`
}

// ControlPlaneConnection is the link to NimbusControl.
type ControlPlaneConnection struct {
	// Configured is false on an unmanaged install, which is a supported
	// product rather than a fault. A panel should say "not managed", not
	// "disconnected".
	Configured  bool      `json:"configured"`
	Connected   bool      `json:"connected"`
	ServerHost  string    `json:"server_host,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

// ConnectionsResponse is the whole picture in one round trip, because the
// panel renders it as one thing and two requests could disagree with each
// other about what time it is.
type ConnectionsResponse struct {
	PBS          []PBSConnection        `json:"pbs"`
	ControlPlane ControlPlaneConnection `json:"control_plane"`
	ServerTime   time.Time              `json:"server_time"`
}

// connectionsState holds the provider installed by the service.
type connectionsState struct {
	connMu       sync.RWMutex
	connProvider func() ConnectionsResponse
}

// SetConnectionsFunc installs the provider.
//
// A function for the same reason SetLockedFunc is one: the answer changes, and
// the panel polls. The provider is expected to be CHEAP — it must serve from a
// cache rather than probing the network per request, because this route is on
// a poll loop and a locked console should not be able to generate PBS traffic
// by being left open.
func (s *Server) SetConnectionsFunc(f func() ConnectionsResponse) {
	s.connMu.Lock()
	s.connProvider = f
	s.connMu.Unlock()
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.connMu.RLock()
	f := s.connProvider
	s.connMu.RUnlock()

	if f == nil {
		// Not wired: report an empty, honest picture rather than 404. The
		// panel then shows "unknown" tiles, which is true, instead of an
		// error that suggests the agent is broken.
		s.writeJSON(w, ConnectionsResponse{
			PBS:        []PBSConnection{},
			ServerTime: time.Now(),
		}, http.StatusOK)
		return
	}

	out := f()
	if out.PBS == nil {
		// A nil slice marshals to null; the panel iterates it. An empty
		// list is the same fact and does not need a guard on every consumer.
		out.PBS = []PBSConnection{}
	}
	if out.ServerTime.IsZero() {
		out.ServerTime = time.Now()
	}
	s.writeJSON(w, out, http.StatusOK)
}
