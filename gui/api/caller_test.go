package api

import (
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The identity half of the local API that does not need Windows: what the
// middleware does with a connection's identity, and how the client talks.
// The pipe that supplies a real identity is pipe_windows_test.go's.

func TestCallerIsTheConnectionsNotTheRequests(t *testing.T) {
	ts := serveTest(NewServer(newStub()))
	t.Cleanup(ts.Close)

	// A request that says it is SYSTEM, elevated, in session 0: in its body,
	// its query and its headers.
	claim := `{"user_sid":"S-1-5-18","account":"NT AUTHORITY\\SYSTEM","session_id":0,"elevated":true,"admin":true}`
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/caller?user_sid=S-1-5-18&elevated=true", strings.NewReader(claim))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Nimbus-Caller", "S-1-5-18")
	req.Header.Set("X-Nimbus-Elevated", "true")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /caller = %d %s", resp.StatusCode, raw)
	}
	var got Caller
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := testCaller
	if got.UserSID != want.UserSID || got.Account != want.Account || got.SessionID != want.SessionID || got.Elevated || got.Admin {
		t.Errorf("GET /caller with a claim = %+v, want the connection's %+v", got, want)
	}
	// pid and image are for the log, never the wire.
	if strings.Contains(string(raw), "pid") || strings.Contains(string(raw), "image") {
		t.Errorf("/caller carries the client's process details: %s", raw)
	}
}

func TestCallerRefusesWrites(t *testing.T) {
	ts := serveTest(NewServer(newStub()))
	t.Cleanup(ts.Close)
	if code, _ := do(t, ts, http.MethodPost, "/caller", map[string]any{"user_sid": "S-1-5-18"}); code != http.StatusMethodNotAllowed {
		t.Errorf("POST /caller = %d, want 405", code)
	}
}

func TestTheClientReadsCaller(t *testing.T) {
	ts := serveTest(NewServer(newStub()))
	t.Cleanup(ts.Close)
	got, err := NewClientAt(ts.URL).Caller()
	if err != nil {
		t.Fatal(err)
	}
	if got.UserSID != testCaller.UserSID || got.SessionID != testCaller.SessionID {
		t.Errorf("Client.Caller() = %+v, want %+v", got, testCaller)
	}
}

// TestTheClientSendsOneRequestPerConnection: the service reads identity per
// connection, so a connection must never carry a second request.
func TestTheClientSendsOneRequestPerConnection(t *testing.T) {
	var mu sync.Mutex
	conns := map[net.Conn]int{}
	var closeHeaders int
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.Close {
			closeHeaders++
		}
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	ts.Config.ConnState = func(c net.Conn, st http.ConnState) {
		if st == http.StateActive {
			mu.Lock()
			conns[c]++
			mu.Unlock()
		}
	}
	ts.Start()
	t.Cleanup(ts.Close)

	c := NewClientAt(ts.URL)
	const n = 4
	for i := 0; i < n; i++ {
		if err := c.getJSON("/status", nil); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(conns) != n {
		t.Errorf("%d requests used %d connections, want one each", n, len(conns))
	}
	for _, k := range conns {
		if k != 1 {
			t.Errorf("a connection carried %d requests", k)
		}
	}
	if closeHeaders != n {
		t.Errorf("%d of %d requests asked for the connection to close", closeHeaders, n)
	}
}

// TestTheClientBoundsTheWholeExchange: a service that answers the headers and
// then stops must not hold the console. The bound covers the body, which an
// http.Client Timeout did and a bare ReadResponse does not unless the
// connection carries a deadline.
func TestTheClientBoundsTheWholeExchange(t *testing.T) {
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{"))
		w.(http.Flusher).Flush()
		<-release
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(release) })

	start := time.Now()
	resp, err := NewClientAt(ts.URL).send(http.MethodGet, "/status", nil, 300*time.Millisecond)
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("a stalled body was read to the end")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("a 300ms bound took %s to end the exchange", took)
	}
}

// TestOnlyTestsUseFixedCallerContext: FixedCallerContext and NewClientAt
// exist for tests. A server built with the first believes whatever it was
// told; a client built with the second talks TCP to anything. Neither may
// appear in a file that ships, anywhere in the repository.
func TestOnlyTestsUseFixedCallerContext(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	defs := map[string]string{
		"FixedCallerContext(": filepath.Join(root, "gui", "api", "caller.go"),
		"NewClientAt(":        filepath.Join(root, "gui", "api", "client.go"),
	}
	scanned := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist", ".tools":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for sym, def := range defs {
			if path != def && strings.Contains(string(b), sym) {
				t.Errorf("%s uses %s, which exists for tests only", path, strings.TrimSuffix(sym, "("))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The scan must have seen the shipped code, or its silence means nothing.
	if scanned < 100 {
		t.Fatalf("scanned only %d Go files from %s; the walk did not reach the repository", scanned, root)
	}
	for sym, def := range defs {
		b, err := os.ReadFile(def)
		if err != nil || !strings.Contains(string(b), "func "+sym) {
			t.Errorf("%s is no longer defined in %s; this test is looking in the wrong place", sym, def)
		}
	}
}
