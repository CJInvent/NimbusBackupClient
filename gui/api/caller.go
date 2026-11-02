package api

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// Who is calling (docs/V4-LOCAL-API.md §3, phase P1).
//
// The service decides from the caller's Windows identity and from nothing the
// caller says about itself. The identity is read from the connection: the
// named pipe's server impersonates its client for long enough to open the
// client's token, reads it, and reverts (pipe_windows.go). It is attached to
// the connection's context by the listener and nowhere else, so a header, a
// query parameter or a body that claims to be someone is just a header, a
// query parameter or a body.
//
// P1 establishes the identity and refuses a request that has none. P2 makes
// decisions from it (the route table); P3 acts as the caller (scoped browse
// and restore).

// Caller is the identity of the process on the other end of the connection,
// as Windows reports it.
type Caller struct {
	// UserSID is the token's user (TokenUser).
	UserSID string `json:"user_sid"`
	// Account is UserSID's name, DOMAIN\user, for the log and for people.
	// Best effort: a SID that does not resolve leaves it empty.
	Account string `json:"account,omitempty"`
	// SessionID is the Windows session the token belongs to
	// (TokenSessionId): 0 is services, an interactive user is 1 or more.
	SessionID uint32 `json:"session_id"`
	// Elevated is TokenElevation: a UAC-elevated token, or an administrator's
	// token on a machine without UAC.
	Elevated bool `json:"elevated"`
	// Admin is the Administrators group present AND enabled in the token
	// (CheckTokenMembership). A UAC-filtered administrator carries the group
	// as deny-only, so this is false until they elevate, which is what §6.1's
	// button depends on.
	Admin bool `json:"admin"`

	// PID and Image are the client process, for the log only. Never a
	// decision: a process can be named anything, and Windows identity is the
	// token, not the executable.
	PID   uint32 `json:"-"`
	Image string `json:"-"`
}

// errNoCaller is a request whose connection carries no identity: something
// served Handler() without the pipe listener in front of it.
var errNoCaller = errors.New("the connection carries no caller identity")

// callerSource is what a connection that knows its caller implements. The
// pipe's server connection resolves the identity on its first read (Windows
// will not impersonate a pipe client that has not written yet); a test's
// fixed source returns what it was given.
type callerSource interface {
	caller() (*Caller, error)
}

type callerSourceKey struct{}
type callerKey struct{}

// connContext is http.Server.ConnContext for the pipe: it makes the
// connection's identity reachable from every request read on it. A
// connection that is not a callerSource gets nothing, and authMiddleware
// refuses its requests.
func connContext(ctx context.Context, c net.Conn) context.Context {
	if src, ok := c.(callerSource); ok {
		return context.WithValue(ctx, callerSourceKey{}, src)
	}
	return ctx
}

type fixedCaller struct{ c Caller }

func (f fixedCaller) caller() (*Caller, error) {
	c := f.c
	return &c, nil
}

// FixedCallerContext is an http.Server ConnContext that gives every
// connection the identity c. It exists for TESTS that serve Handler() on a
// loopback listener (httptest), which has no Windows identity to read.
//
// Production never calls it: the pipe listener attaches the identity it read
// from Windows (Serve). TestOnlyTestsUseFixedCallerContext fails if a
// non-test file anywhere in the repository does, because a server built with
// it believes whatever it was told.
func FixedCallerContext(c Caller) func(context.Context, net.Conn) context.Context {
	return func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, callerSourceKey{}, callerSource(fixedCaller{c}))
	}
}

// callerFrom is the identity authMiddleware established for this request.
func callerFrom(ctx context.Context) (*Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(*Caller)
	return c, ok && c != nil
}

// handleCaller answers "who does the service think I am": the identity it
// read from Windows for this connection. The console uses it to decide
// whether to show Elevate permissions (§6.1, phase P4); the tests use it to
// see what the service saw. It reads nothing from the request, which is the
// point: a body or header claiming an identity changes nothing here.
func (s *Server) handleCaller(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c, ok := callerFrom(r.Context())
	if !ok {
		// authMiddleware refuses before this; reaching here without one is
		// a wiring fault, and the answer is still no.
		s.writeError(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.writeJSON(w, c, http.StatusOK)
}

// Caller asks the service who it thinks this client is.
func (c *Client) Caller() (*Caller, error) {
	var out Caller
	if err := c.getJSON("/caller", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
