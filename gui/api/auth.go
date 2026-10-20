package api

import (
	"context"
	"log"
	"net/http"
)

// maxRequestBody bounds request bodies to avoid unbounded memory from a local
// caller (audit: "borner les tailles de body").
const maxRequestBody = 1 << 20 // 1 MiB

// authMiddleware admits a request only if its connection carries a Windows
// identity (docs/V4-LOCAL-API.md §3), and bounds its body.
//
// It replaced the shared token (H-01) in V4-LOCAL-API P1. The token proved
// that a caller could read a file every interactive user could read, so every
// logged-on user had the same power over a service running as SYSTEM. The
// identity is read from Windows per connection, and what a request says about
// itself is never consulted. The Origin check and the custom header went with
// the token: both existed because a browser can reach a loopback port, and a
// browser cannot open a named pipe.
//
// P1 refuses only a request with no identity. Deciding from the identity
// (which routes need an elevated administrator) is P2's route table.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

		src, _ := r.Context().Value(callerSourceKey{}).(callerSource)
		if src == nil {
			log.Printf("[API] refused %s %s: %v", r.Method, r.URL.Path, errNoCaller)
			s.writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := src.caller()
		if err != nil || c == nil || c.UserSID == "" {
			// The pipe's client could not be identified: Windows would not
			// impersonate it, or its token could not be read. Nothing is
			// served to a caller the service cannot name.
			log.Printf("[API] refused %s %s: the caller could not be identified: %v", r.Method, r.URL.Path, err)
			s.writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Every request that may change something is logged with who made
		// it (§8). Reads are not: the console polls, and a line per poll
		// would bury the ones that matter.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			log.Printf("[API] %s %s by %s (%s) session %d elevated=%t admin=%t pid %d %s",
				r.Method, r.URL.Path, c.Account, c.UserSID, c.SessionID, c.Elevated, c.Admin, c.PID, c.Image)
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)))
	})
}
