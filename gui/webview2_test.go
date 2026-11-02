package main

import "testing"

func TestWebView2VersionsTheConsoleAccepts(t *testing.T) {
	for _, c := range []struct {
		pv   string
		want bool
	}{
		{"", false},
		{"0.0.0.0", false}, // Microsoft: registered but not installed
		{"  ", false},
		{"94.0.992.30", false},
		{"94.0.992.31", true}, // Wails v2.8.0's minimum, exactly
		{"94.0.992.31.0", true},
		{"94.1", true},
		{"141.0.3537.71", true},
		{"93.9.9999.9999", false},
		{"141.x", false}, // unreadable is not trusted, even after a decisive part
		{"x.0.0.0", false},
		{"141..0", false},
		{"141.-1.0.0", false},
	} {
		if got := webView2Usable(c.pv); got != c.want {
			t.Errorf("webView2Usable(%q) = %t, want %t", c.pv, got, c.want)
		}
	}
}

func TestWebView2DownloadsStayOnMicrosoft(t *testing.T) {
	for _, c := range []struct {
		scheme, host string
		want         bool
	}{
		{"https", "go.microsoft.com", true},
		{"https", "msedge.sf.dl.delivery.mp.microsoft.com", true},
		{"https", "MICROSOFT.COM", true},
		{"http", "go.microsoft.com", false},
		{"https", "microsoft.com.example.invalid", false},
		{"https", "evilmicrosoft.com", false},
		{"https", "example.invalid", false},
	} {
		if got := microsoftHTTPS(c.scheme, c.host); got != c.want {
			t.Errorf("microsoftHTTPS(%q, %q) = %t, want %t", c.scheme, c.host, got, c.want)
		}
	}
}
