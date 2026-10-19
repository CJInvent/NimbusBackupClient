package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestTheInstallIsReportedFromTheExitCodeAndTheRunningVersion(t *testing.T) {
	m := &updateMarker{Version: "1.01.04"}
	cases := []struct {
		running  string
		code     int
		recorded bool
		outcome  string
	}{
		{"1.01.04", 0, true, "installed"},
		{"1.01.04", 3010, true, "installed"},
		{"1.01.03", 0, true, "failed"},    // msiexec said yes, the old service is back
		{"1.01.03", 1603, true, "failed"}, // rolled back
		{"1.01.04", 0, false, "failed"},   // the task never wrote a result
	}
	for _, c := range cases {
		r := updateOutcome(m, c.running, c.code, c.recorded, "tail")
		if r.Outcome != c.outcome || r.Version != "1.01.04" {
			t.Errorf("%+v: got %+v", c, r)
		}
		if c.recorded != (r.ExitCode != nil) || (r.ExitCode != nil && *r.ExitCode != c.code) {
			t.Errorf("%+v: exit code %v", c, r.ExitCode)
		}
	}
	long := strings.Repeat("x", 3*updateLogTailBytes)
	if r := updateOutcome(m, "1.01.03", 1603, true, long); len(r.LogTail) != updateLogTailBytes {
		t.Fatalf("the log tail is bounded: %d", len(r.LogTail))
	}
}

func TestTheMsiexecLogIsReadAsUTF16AndRedacted(t *testing.T) {
	dir := t.TempDir()
	// The secret is made at run time: a literal here is what the secret scan
	// flags (NimbusControl rule: no credential-shaped literals in the tree).
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	secret := hex.EncodeToString(raw)
	text := "MSI (s) line one\r\nProperty(S): PROVISIONING_PROFILE token=" + secret + "\r\nreturn value 3\r\n"
	u := utf16.Encode([]rune(text))
	b := []byte{0xFF, 0xFE}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	p := filepath.Join(dir, "install.log")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	got := readLogTail(p)
	if !strings.Contains(got, "return value 3") || strings.Contains(got, secret) || strings.Contains(got, "\r") {
		t.Fatalf("tail: %q", got)
	}
}

func TestTheInstallCommandRecordsTheExitCodeBesideTheLog(t *testing.T) {
	m := updateMarker{MSI: `C:\ProgramData\NimbusBackup\update\agent.msi`,
		Log: `C:\ProgramData\NimbusBackup\update\install.log`, Exit: `C:\ProgramData\NimbusBackup\update\exit.txt`}
	c := updateCommand(m)
	for _, want := range []string{`msiexec.exe /i "` + m.MSI + `" /qn /norestart /l*v "` + m.Log + `"`, `echo %ERRORLEVEL%>"` + m.Exit + `"`} {
		if !strings.Contains(c, want) {
			t.Fatalf("command lacks %q:\n%s", want, c)
		}
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "exit.txt")
	_ = os.WriteFile(p, []byte("1603 \r\n"), 0o600)
	if n, ok := readExitCode(p); !ok || n != 1603 {
		t.Fatalf("exit code %d %v", n, ok)
	}
	if _, ok := readExitCode(filepath.Join(dir, "missing")); ok {
		t.Fatal("no file, no code")
	}
}
