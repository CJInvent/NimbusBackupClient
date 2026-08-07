package api

import (
	"net/http"
	"strings"
	"sync"
)

// Read-only lockdown, enforced HERE rather than in the front end.
//
// V4-CLIENT-CONFIG.md §5.1: when an org sets `gui_read_only`, the client opens
// as a status page and nothing else. The GUI hiding its own buttons is
// presentation. THIS is the control — a modified front end that draws a Start
// button gets an HTTP error when it presses it, and so does anything else
// holding the local API token.
//
// THE LIST IS AN ALLOWLIST, AND THAT IS THE WHOLE DESIGN. Enumerating the
// mutating routes instead would mean every route added later is permitted
// until somebody remembers to add it, and "somebody remembers" is not a
// security control. A new endpoint is refused under lockdown until it is
// explicitly declared safe, so the failure mode of forgetting is a bug report
// rather than a silent hole.
//
// Read-only does NOT stop the machine backing up. Scheduled work and
// control-plane commands never pass through this API — they originate inside
// the service. What stops is the console driving them, which is what an org
// that sets this key is asking for.

// readOnlyExactPaths are served in full while the agent is locked.
var readOnlyExactPaths = map[string]struct{}{
	"/status":              {}, // version, active job count, connections
	"/runs/active":         {}, // what is running now
	"/runs/recent":         {}, // the seven-day panel
	"/controlplane/status": {}, // whether the server is reachable, and the policy
}

// readOnlyPrefixes are subtree routes served while locked. Kept separate from
// the exact set because a prefix match is a broader grant and should be
// obviously so when reading this file.
var readOnlyPrefixes = []string{
	"/runs/",          // one run by id
	"/backup/status/", // deprecated per-job progress, read-only by nature
}

// readOnlyAllowed reports whether a request is safe to serve under lockdown.
//
// Method is checked as well as path. Every allowed route is a reader, so a
// POST to one is either a mistake or an attempt — and a handler that today
// only reads may grow a POST branch tomorrow. Pinning the method here means
// that change cannot quietly widen what a locked agent will do.
func readOnlyAllowed(method, path string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if _, ok := readOnlyExactPaths[path]; ok {
		return true
	}
	for _, p := range readOnlyPrefixes {
		// A bare "/runs/" names no run and is handled as a 400 by the
		// handler; it is still a read, so it is allowed through to say so.
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// SetLockedFunc installs the predicate that answers "is this agent locked?".
//
// A function rather than a value because the answer changes underneath us: the
// policy arrives on a check-in, and an org that locks a machine expects that to
// take effect on the next call, not on the next service restart.
//
// Nil, or never set, means unlocked. That matches the key's own permissive
// default (NimbusControl Core\Policy): an agent that has never heard from a
// control server is not a locked agent, and defaulting the other way would
// brick the console of every unmanaged install.
func (s *Server) SetLockedFunc(f func() bool) {
	s.lockedMu.Lock()
	s.locked = f
	s.lockedMu.Unlock()
}

// IsLocked reports the current lockdown state.
func (s *Server) IsLocked() bool {
	s.lockedMu.RLock()
	f := s.locked
	s.lockedMu.RUnlock()
	if f == nil {
		return false
	}
	return f()
}

// lockedMu guards the predicate itself, not the answer it gives.
type lockedState struct {
	lockedMu sync.RWMutex
	locked   func() bool
}

// readOnlyMiddleware refuses anything that is not on the allowlist while the
// agent is locked.
//
// It runs INSIDE authMiddleware, never outside it: an unauthenticated caller
// must get 401 and learn nothing, rather than 403 and learn that this machine
// is under management and locked.
func (s *Server) readOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.IsLocked() && !readOnlyAllowed(r.Method, r.URL.Path) {
			s.writeError(w,
				"this agent is managed and its console is read-only; "+
					"scheduled backups continue to run",
				http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
