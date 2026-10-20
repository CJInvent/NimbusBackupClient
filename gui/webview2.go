package main

import (
	"strconv"
	"strings"
)

// The WebView2 runtime is the service's to install (docs/V4-CONSOLE.md §6 D2,
// built in V4-LOCAL-API P1).
//
// The console draws its window in WebView2. It used to be built with Wails'
// `wv2runtime.download`, which, finding no runtime, downloaded Microsoft's
// installer itself; that downloader is most of why the console linked a TLS
// client. The console is now built with `wv2runtime.error`: it says the runtime
// is missing and exits. The service installs it instead. It already links TLS,
// it runs as SYSTEM (so the install is per-machine and serves every user), and
// it is the one program on the machine that is always running.
//
// Windows 11 and current Windows 10 ship the runtime; this matters on Windows
// Server images and old Windows 10 builds.

// webView2MinVersion is the oldest runtime the console accepts: Wails v2.8.0's
// wv2installer.MinimumRuntimeVersion (WebView2 SDK 1.0.992.28). Older is
// treated as missing, because the console would refuse it.
const webView2MinVersion = "94.0.992.31"

// webView2Usable reports whether a registry `pv` value names a runtime the
// console can use. Microsoft: a missing, empty or 0.0.0.0 value is no runtime.
func webView2Usable(pv string) bool {
	pv = strings.TrimSpace(pv)
	if pv == "" || pv == "0.0.0.0" {
		return false
	}
	return !versionOlder(pv, webView2MinVersion)
}

// versionOlder compares dotted numeric versions. A version with any part that
// is not a number is older than anything, wherever that part is: an
// unreadable version is not one to trust.
func versionOlder(a, b string) bool {
	pa, ok := versionParts(a)
	if !ok {
		return true
	}
	pb, ok := versionParts(b)
	if !ok {
		return false
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func versionParts(v string) ([]int, bool) {
	fields := strings.Split(v, ".")
	out := make([]int, len(fields))
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

// microsoftHTTPS is an HTTPS URL on microsoft.com or a subdomain of it: where
// the bootstrapper's download may be redirected.
func microsoftHTTPS(scheme, host string) bool {
	host = strings.ToLower(host)
	return scheme == "https" && (host == "microsoft.com" || strings.HasSuffix(host, ".microsoft.com"))
}
