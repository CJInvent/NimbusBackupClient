package main

// THE CONSOLE'S CODES ARE THE SERVICE'S CODES.
//
// The front end translates an error by its [NB-xxxx] code, so a code means one
// thing whichever process returned it. The console keeps its own copy of the
// few it returns itself (errcodes.go) because it cannot import the service's.
// This reads the service's source: a code both declare must carry the same
// text. Two codes are the console's alone (NB-1011, NB-2011: only a client
// fails to reach the service or reads its cert_untrusted flag); the service's
// lint would report them unused there.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

var nbCode = regexp.MustCompile(`^\[NB-\d{4}\]`)

// stringConsts maps each string constant in file to its value.
func stringConsts(t *testing.T, file string) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	out := map[string]string{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err == nil {
					out[name.Name] = v
				}
			}
		}
	}
	return out
}

func TestConsoleCodesMatchTheService(t *testing.T) {
	console := stringConsts(t, "errcodes.go")
	service := stringConsts(t, filepath.Join("..", "gui", "errcodes.go"))
	if len(console) == 0 || len(service) == 0 {
		t.Fatalf("no codes read (console %d, service %d); a match would mean nothing", len(console), len(service))
	}
	byCode := map[string]string{}
	for _, v := range service {
		if c := nbCode.FindString(v); c != "" {
			byCode[c] = v
		}
	}
	shared := 0
	for name, v := range console {
		c := nbCode.FindString(v)
		if c == "" {
			t.Errorf("%s = %q carries no [NB-xxxx] code", name, v)
			continue
		}
		if want, ok := byCode[c]; ok && want != v {
			t.Errorf("%s: the console says %q, the service says %q for the same code", name, v, want)
		}
		if _, ok := byCode[c]; ok {
			shared++
		}
	}
	// The restore argument codes are shared; none matching would mean the
	// service's file was not read as expected.
	if shared == 0 {
		t.Fatal("no console code is declared by the service; the comparison checked nothing")
	}
}
