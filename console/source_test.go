package main

// ONE CALL PER BINDING: THE CONSOLE DOES NO LOCAL IO (docs/V4-CONSOLE.md §5
// item 3).
//
// Crude by design. It exists to make the next "just read the file here, it's
// quicker" a red build: the console's sources may not write, rename or delete
// files, touch the registry, run programs or dial the network. Everything they
// show, they ask the service for, through gui/api's client.
//
// A few files are exempt, each for a reason it states. The list is checked
// both ways: an exempt file that no longer exists is an error too, so the
// exemptions cannot outlive their reasons unnoticed.

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// exemptFromNoLocalIO are the console files that touch the machine, and why.
var exemptFromNoLocalIO = map[string]string{
	"main.go":              "the WebView2 data folder and the process exit",
	"logging.go":           "the console's own log under %LOCALAPPDATA% (V4-CONSOLE §9)",
	"tray.go":              "the tray icon, its process exit",
	"tray_stub.go":         "the tray off Windows (test builds)",
	"storage_tray_icon.go": "draws the tray's error icon",
	"pathpicker.go":        "the folder picker, listing as the user until V4-LOCAL-API P3 moves it into the service",
	"diskspace_windows.go": "the picker's drive list and free space, until V4-LOCAL-API P3",
	"diskspace_other.go":   "the picker's helpers off Windows (test builds)",
}

// forbiddenCalls are the selectors a console file may not use.
var forbiddenCalls = []string{
	"os.WriteFile", "os.Create", "os.CreateTemp", "os.OpenFile", "os.ReadFile", "os.Open",
	"os.Rename", "os.Remove", "os.RemoveAll", "os.Mkdir", "os.MkdirAll", "os.Chmod",
	"registry.", "exec.Command", "net.Dial", "syscall.NewLazyDLL", "windows.NewLazySystemDLL",
}

// forbiddenImports are packages a console file may not import at all.
var forbiddenImports = []string{
	"os/exec", "net", "golang.org/x/sys/windows/registry", "syscall", "golang.org/x/sys/windows",
}

func TestTheConsoleDoesNoLocalIO(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	present := map[string]bool{}
	scanned := map[string]bool{}
	for _, f := range files {
		present[f] = true
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if _, ok := exemptFromNoLocalIO[f]; ok {
			continue
		}
		scanned[f] = true
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range forbiddenCalls {
			if strings.Contains(stripComments(string(src)), call) {
				t.Errorf("%s uses %s. The console does nothing locally; this belongs in the service, "+
					"behind a local-API route (docs/V4-CONSOLE.md §2)", f, call)
			}
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, bad := range forbiddenImports {
				if path == bad {
					t.Errorf("%s imports %q, which the console's bindings have no use for", f, path)
				}
			}
		}
	}

	// Preconditions: the bindings were among what was scanned, so a glob
	// that matched nothing, or a renamed file, cannot pass as clean.
	for _, must := range []string{"console.go", "console_bindings.go", "restore_bindings.go"} {
		if !scanned[must] {
			t.Errorf("%s was not scanned; the check would be passing over the bindings it exists for", must)
		}
	}
	var stale []string
	for f := range exemptFromNoLocalIO {
		if !present[f] {
			stale = append(stale, f)
		}
	}
	sort.Strings(stale)
	for _, f := range stale {
		t.Errorf("%s is exempt but no longer exists; remove it from exemptFromNoLocalIO", f)
	}
}

// stripComments drops // line comments, so a comment naming a forbidden call
// (as this package's documentation does) is not a finding.
func stripComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 && !strings.Contains(line[:i], `"`) {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
