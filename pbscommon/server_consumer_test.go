package pbscommon

// NimbusControl docs/V4-CLIENT-CHANGES-FOR-SERVER-BROWSE.md section 3: this
// package is also the server's volume reader, where stdout is the reader's
// data channel and a PBS error page passed off as an index or a chunk becomes
// "bad magic" on a portal page. So: nothing here writes to stdout, and the two
// read calls say what PBS answered.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Section 3.1: no fmt.Print* in non-test code. Parsed, not grepped, so a
// commented-out call does not count and an aliased import cannot hide one.
func TestNothingInPbscommonWritesToStdout(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) < 10 {
		t.Fatalf("found %d source files (%v); an empty list would prove nothing", len(files), err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		fmtName := ""
		for _, imp := range f.Imports {
			if imp.Path.Value == `"fmt"` {
				fmtName = "fmt"
				if imp.Name != nil {
					fmtName = imp.Name.Name
				}
			}
		}
		if fmtName == "" {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == fmtName && strings.HasPrefix(sel.Sel.Name, "Print") {
				t.Errorf("%s: fmt.%s writes to stdout; use debugf (DebugLogFn)", fset.Position(call.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
}

// The lines that used to be printed reach the host's hook instead, and are
// silent when there is none.
func TestDebugfGoesToTheHookOnly(t *testing.T) {
	saved := DebugLogFn
	defer func() { DebugLogFn = saved }()
	var got []string
	DebugLogFn = func(s string) { got = append(got, s) }
	debugf("Writer id: %d", 7)
	if len(got) != 1 || got[0] != "Writer id: 7" {
		t.Fatalf("hook got %q", got)
	}
	DebugLogFn = nil
	debugf("nobody listening") // must not panic, must not print
}

// Section 3.2: a PBS error is reported as one, with its status and what PBS
// said, not handed back as the index or chunk it was asked for.
func TestReadsReportThePBSStatusAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/chunk") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"data":null,"message":"no such chunk\n"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"data":null,"message":"permission check failed"}`))
	}))
	defer srv.Close()
	pbs := &PBSClient{BaseURL: srv.URL, AuthID: "test@pbs!t", Secret: "x", Client: *srv.Client()}

	_, err := pbs.DownloadToBytes("drive-scsi0.img.fidx")
	if err == nil || !strings.Contains(err.Error(), "PBS HTTP 403") || !strings.Contains(err.Error(), "permission check failed") {
		t.Fatalf("DownloadToBytes: %v", err)
	}
	if strings.Contains(err.Error(), "bad magic") {
		t.Fatalf("the error page was parsed as an index: %v", err)
	}
	_, err = pbs.GetChunkData(strings.Repeat("ab", 32))
	if err == nil || !strings.Contains(err.Error(), "PBS HTTP 404") || !strings.Contains(err.Error(), "no such chunk") {
		t.Fatalf("GetChunkData: %v", err)
	}
	if strings.ContainsAny(err.Error(), "\n\r") {
		t.Fatalf("the quoted body kept a line break: %q", err)
	}
}

func TestTheBodyQuotedInAnErrorIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(strings.Repeat("x", 10000)))
	}))
	defer srv.Close()
	pbs := &PBSClient{BaseURL: srv.URL, AuthID: "test@pbs!t", Secret: "x", Client: *srv.Client()}
	_, err := pbs.DownloadToBytes("index.json.blob")
	if err == nil || len(err.Error()) > 512 {
		t.Fatalf("error not bounded: %d bytes", len(err.Error()))
	}
}

// Section 2: the package says, at the top, that a server builds it.
func TestThePackageNamesItsServerConsumer(t *testing.T) {
	b, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "nimbus-volume-reader") {
		t.Fatal("doc.go does not name the server's volume reader as a consumer")
	}
}
