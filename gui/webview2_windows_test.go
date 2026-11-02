//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The signature check is what stands between a download and SYSTEM running
// it, so it is tested on real Windows against a real Microsoft signature and
// against a file with none.

// microsoftSignedFile is a file on every Windows image carrying an EMBEDDED
// Microsoft signature (most of System32 is catalog-signed, which is not what
// the bootstrapper carries).
func microsoftSignedFile(t *testing.T) string {
	t.Helper()
	win := os.Getenv("WINDIR")
	for _, p := range []string{
		filepath.Join(win, `Microsoft.NET\Framework64\v4.0.30319\csc.exe`),
		filepath.Join(win, `Microsoft.NET\Framework64\v4.0.30319\MSBuild.exe`),
		filepath.Join(win, `Microsoft.NET\Framework\v4.0.30319\csc.exe`),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Fatal("no Microsoft-signed .NET Framework executable on this machine; the signature test has nothing to check against")
	return ""
}

func TestTheBootstrapperMustBeSignedByMicrosoft(t *testing.T) {
	signed := microsoftSignedFile(t)
	if err := verifySignedBy(signed, webView2Signer); err != nil {
		t.Fatalf("a Microsoft-signed file was refused: %v", err)
	}
	// Signed, valid, by Microsoft, and the name still has to match.
	err := verifySignedBy(signed, "Someone Else")
	if err == nil || !strings.Contains(err.Error(), `is signed by "Microsoft Corporation"`) {
		t.Errorf("a signer that is not the one required was accepted, or refused for another reason: %v", err)
	}

	// An unsigned executable: this test binary.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	err = verifySignedBy(self, webView2Signer)
	if err == nil || !strings.Contains(err.Error(), "has no valid signature") {
		t.Errorf("an unsigned executable was accepted, or refused for another reason: %v", err)
	}

	// A signed file with one byte changed after signing.
	b, err := os.ReadFile(signed)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 0xff
	tampered := filepath.Join(t.TempDir(), "tampered.exe")
	if err := os.WriteFile(tampered, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifySignedBy(tampered, webView2Signer); err == nil {
		t.Error("a signed file modified after signing was accepted")
	}
}

func TestWebView2DetectionReadsTheRegistry(t *testing.T) {
	// The runner may or may not have the runtime; either way detection must
	// agree with the value it read.
	v := installedWebView2Version()
	if v != "" && !webView2Usable(v) {
		t.Errorf("installedWebView2Version returned %q, which it would not accept", v)
	}
	t.Logf("the runner's per-machine WebView2 runtime: %q", v)
}
