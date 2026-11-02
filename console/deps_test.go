package main

// THE CONSOLE DEPENDS ON THIS LIST AND NOTHING ELSE (docs/V4-CONSOLE.md §5
// item 1).
//
// The question the old CI checks never asked was "what does the GUI depend
// on at all?". They were denylists: ten engine files, eight engine symbols. A
// file nobody had named (backup_inline.go, the data-path identity code) went
// straight through. This is an allowlist, written out, and it fails on what
// nobody thought to forbid: a module not below is a red build, named.
//
// It asks the toolchain for the SHIPPED build (GOOS=windows, the Wails tags
// CI builds with), because the desktop tags pull in code the test build does
// not. A list that came back empty would pass anything, so it must contain
// Wails before an absence means something.

import (
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// shippedTags are `wails build`'s tags for the console CI ships: Build GUI's
// `wails build -platform windows/amd64 -webview2 error` (desktop, production,
// and the WebView2 strategy). The strategy comes from the -webview2 FLAG,
// whose default is "download": wails.json's "webview2" key, which used to
// say "download" here, is not read by the Wails CLI at all.
// TestTheShippedTagsAreTheBuildsTags pins the workflow and the local gates
// to this constant.
const shippedTags = "desktop,wv2runtime.error,production"

// allowedModules is every module the shipped console may link, and why.
var allowedModules = map[string]string{
	"github.com/tizbac/proxmoxbackupclient_go/console": "this module",
	"github.com/tizbac/proxmoxbackupclient_go/gui/api": "the local API's client and wire types: the ONLY project module",

	"github.com/wailsapp/wails/v2":         "the window",
	"github.com/wailsapp/go-webview2":      "Wails: the WebView2 host",
	"github.com/wailsapp/mimetype":         "Wails: the asset server",
	"github.com/go-ole/go-ole":             "Wails: COM on Windows",
	"github.com/bep/debounce":              "Wails",
	"github.com/google/uuid":               "Wails",
	"github.com/leaanthony/go-ansi-parser": "Wails: its logger",
	"github.com/leaanthony/slicer":         "Wails",
	"github.com/leaanthony/u":              "Wails: options",
	"github.com/pkg/browser":               "Wails: runtime.BrowserOpenURL",
	"github.com/pkg/errors":                "Wails",
	"github.com/rivo/uniseg":               "Wails: its logger",
	"github.com/samber/lo":                 "Wails",
	"github.com/tkrajina/go-reflector":     "Wails: binding reflection",
	"golang.org/x/net":                     "Wails: the HTML parser",
	"golang.org/x/exp":                     "Wails",

	"github.com/getlantern/systray": "the tray",
	"github.com/getlantern/context": "systray: its logger",
	"github.com/getlantern/errors":  "systray: its logger",
	"github.com/getlantern/golog":   "systray: its logger",
	"github.com/getlantern/hex":     "systray: its logger",
	"github.com/getlantern/hidden":  "systray: its logger",
	"github.com/getlantern/ops":     "systray: its logger",
	"github.com/go-stack/stack":     "systray: its logger",
	"github.com/oxtoacart/bpool":    "systray: its logger",

	"golang.org/x/sys": "Windows APIs (Wails, systray, the local API's named pipe in gui/api)",
}

// goTool is the go command on the PATH: the toolchain CI pins and asserts
// (the toolchain job), and the one scripts/local-gates.sh checks.
func goTool(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("no go command to list the console's dependencies with: %v", err)
	}
	return p
}

func TestTheConsoleDependsOnlyOnItsAllowlist(t *testing.T) {
	cmd := exec.Command(goTool(t), "list", "-deps", "-tags", shippedTags,
		"-f", "{{with .Module}}{{.Path}}{{end}}", ".")
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0", "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("go list failed: %v\n%s", err, stderr)
	}

	seen := map[string]bool{}
	for _, m := range strings.Fields(string(out)) {
		seen[m] = true
	}
	// The precondition: an empty or truncated listing must not read as clean.
	if !seen["github.com/wailsapp/wails/v2"] || !seen["github.com/tizbac/proxmoxbackupclient_go/gui/api"] {
		t.Fatalf("the listing has no Wails or no gui/api (%d modules); an absence below would prove nothing:\n%s",
			len(seen), out)
	}

	var bad []string
	for m := range seen {
		if _, ok := allowedModules[m]; !ok {
			bad = append(bad, m)
		}
	}
	sort.Strings(bad)
	for _, m := range bad {
		t.Errorf("the console depends on %s, which is not on its allowlist. The console is a client of "+
			"the service and nothing else (docs/V4-CONSOLE.md §2): if this is functional code, it "+
			"belongs in the service; if it is a new dependency of Wails or systray, add it to "+
			"allowedModules with the reason", m)
	}
}

// TestTheShippedTagsAreTheBuildsTags: the console's WebView2 strategy is
// decided by `wails build -webview2`, whose default ("download") links Wails'
// downloader and a TLS client the console does not need (V4-CONSOLE D2). The
// allowlist above is only true of the build CI ships if CI builds with these
// tags, so the workflow and the local gates are read here, not trusted.
func TestTheShippedTagsAreTheBuildsTags(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(b)
	}
	workflow := read("../.github/workflows/build-and-release.yml")
	gates := read("../scripts/local-gates.sh")

	build := ""
	for _, line := range strings.Split(workflow, "\n") {
		if strings.Contains(line, "wails build ") {
			if build != "" {
				t.Fatalf("more than one `wails build` in the workflow; this test reads one:\n%s\n%s", build, line)
			}
			build = line
		}
	}
	if build == "" {
		t.Fatal("no `wails build` in the workflow; this test is reading the wrong file")
	}
	if !strings.Contains(build, "-webview2 error") {
		t.Errorf("Build GUI's `wails build` does not pass -webview2 error, so it ships the downloader:\n%s", build)
	}
	// The compile view, govulncheck and the local vet line name the tags.
	if n := strings.Count(workflow, shippedTags); n < 2 {
		t.Errorf("the workflow names %q %d times; Compile Views and govulncheck both should", shippedTags, n)
	}
	if !strings.Contains(gates, shippedTags) {
		t.Errorf("scripts/local-gates.sh does not vet the console with %q", shippedTags)
	}
	for name, text := range map[string]string{"the workflow": workflow, "scripts/local-gates.sh": gates} {
		if strings.Contains(text, "wv2runtime.download") {
			t.Errorf("%s still names wv2runtime.download", name)
		}
	}
}
