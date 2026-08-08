package main

import (
	"strings"
	"testing"
)

// Client logging — level resolution and redaction.
//
// Both are pure, so both are testable without a registry or a disk. The cases
// that matter are the degradations: a typo in a registry value must not
// silence a machine, and a secret must not reach a support bundle whichever
// way a caller happened to phrase the line.

func TestLogLevelFallsBackToInfo(t *testing.T) {
	// A typo in a registry value must not be able to turn logging off on a
	// machine, and must not stop it backing up either.
	for _, bad := range []string{"", "   ", "LOUD", "verbose", "0", "info!"} {
		if got := resolveLogLevel(bad); got != levelInfo {
			t.Errorf("resolveLogLevel(%q) = %v, want INFO", bad, got)
		}
	}
}

func TestLogLevelIsCaseInsensitive(t *testing.T) {
	// The value is documented uppercase but typed by a human into regedit.
	for _, s := range []string{"debug", "DEBUG", "Debug", "  debug  "} {
		if got := resolveLogLevel(s); got != levelDebug {
			t.Errorf("resolveLogLevel(%q) = %v, want DEBUG", s, got)
		}
	}
	if resolveLogLevel("TRACE") != levelTrace {
		t.Error("TRACE did not resolve")
	}
	// WARN and ERROR are deliberately NOT offered: every operational call
	// site here is one severity, so accepting ERROR would produce a silent
	// agent that looks identical to a broken one.
	for _, unsupported := range []string{"WARN", "ERROR", "FATAL"} {
		if got := resolveLogLevel(unsupported); got != levelInfo {
			t.Errorf("resolveLogLevel(%q) = %v; unsupported levels must fall back to INFO", unsupported, got)
		}
	}
}

func TestCategoriesFollowTheLevel(t *testing.T) {
	// Levels and categories are ONE verbosity system. At DEBUG every
	// category is on without a launch flag; at INFO none is, unless one was
	// explicitly enabled.
	orig := activeLevel
	defer func() { activeLevel = orig; SetLogCategories("") }()

	SetLogCategories("")
	activeLevel = levelInfo
	if categoryEnabled(catChunks) {
		t.Error("a category was on at INFO with no launch flag")
	}

	activeLevel = levelDebug
	if !categoryEnabled(catChunks) || !categoryEnabled(catPBS) {
		t.Error("DEBUG did not enable every category")
	}

	// A launch flag still works without raising the level for everything.
	activeLevel = levelInfo
	SetLogCategories("pbs")
	if !categoryEnabled(catPBS) {
		t.Error("an explicitly enabled category was off at INFO")
	}
	if categoryEnabled(catChunks) {
		t.Error("enabling one category enabled another")
	}
}

func TestRedactionRemovesSecretsWhateverTheirShape(t *testing.T) {
	cases := []struct {
		in     string
		secret string
	}{
		{"GET /api Authorization: Bearer abc123def", "abc123def"},
		{"X-Nimbus-Token: 0123456789abcdef", "0123456789abcdef"},
		{"connecting with root@pam!agent:0f1e2d3c-4b5a-6978", "0f1e2d3c-4b5a-6978"},
		{"url=https://pbs/api?api_key=supersecretvalue", "supersecretvalue"},
		{"password=hunter2 for user bob", "hunter2"},
		{"key: 0123456789abcdef0123456789abcdef0123", "0123456789abcdef0123456789abcdef0123"},
	}
	for _, c := range cases {
		got := redactLogLine(c.in)
		if strings.Contains(got, c.secret) {
			t.Errorf("redactLogLine(%q) still contains %q — got %q", c.in, c.secret, got)
		}
		if !strings.Contains(got, redactedMarker) {
			t.Errorf("redactLogLine(%q) removed the secret without marking it — got %q", c.in, got)
		}
	}
}

func TestRedactionKeepsWhatTheLogIsFor(t *testing.T) {
	// A redactor that eats paths, snapshot ids and token NAMES makes the
	// support bundle useless, which is a different way of having no logs.
	keep := []struct{ line, want string }{
		{"connecting with root@pam!agent:0f1e2d3c-4b5a-6978", "root@pam!agent"},
		{"url=https://pbs.example.com/api2/json?api_key=zzzzzzzzzzzz", "https://pbs.example.com/api2/json"},
		{`backing up C:\Users\Bob\Documents`, `C:\Users\Bob\Documents`},
		{"chunk digest 3b1f0a9c2d4e6f8a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a", "3b1f0a9c"},
		{"snapshot host/WS-01/2026-08-05T02:00:00Z", "host/WS-01/2026-08-05T02:00:00Z"},
	}
	for _, k := range keep {
		if got := redactLogLine(k.line); !strings.Contains(got, k.want) {
			t.Errorf("redactLogLine(%q) lost %q — got %q", k.line, k.want, got)
		}
	}
}
