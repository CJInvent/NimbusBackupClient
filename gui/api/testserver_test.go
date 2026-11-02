package api

import (
	"net/http/httptest"
)

// testCaller is the identity every test server's connections carry. Its
// values are invented and recognizable; the pipe that reads a REAL identity
// from Windows is pipe_windows_test.go's subject.
var testCaller = Caller{
	UserSID:   "S-1-5-21-1000000001-1000000002-1000000003-1001",
	Account:   `TESTHOST\console-user`,
	SessionID: 1,
}

// serveTest serves s.Handler() on loopback with testCaller on every
// connection: the production stack, behind the production middleware, with
// the identity the pipe would otherwise supply.
func serveTest(s *Server) *httptest.Server {
	ts := httptest.NewUnstartedServer(s.Handler())
	ts.Config.ConnContext = FixedCallerContext(testCaller)
	ts.Start()
	return ts
}

// serveTestWithoutCaller serves s.Handler() with no identity at all: what a
// server reached by anything but the pipe looks like.
func serveTestWithoutCaller(s *Server) *httptest.Server {
	return httptest.NewServer(s.Handler())
}
