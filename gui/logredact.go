package main

import "regexp"

// Central redaction for the client's logs (V4-SPEC §12: never log passwords,
// tokens, session secrets, raw keys, vault contents, or unredacted
// authorization headers).
//
// APPLIED TO THE WHOLE RENDERED LINE, in writeLogToLogger, so it cannot be
// bypassed by a caller. That is the point: this agent has ~280 writeDebugLog
// call sites, and the history of secrets in logs is entirely callers who did
// not think to name the field. A per-call-site opt-in would be a list somebody
// has to keep complete forever.
//
// It also has to NOT eat what an engineer reads these logs for. A PBS token's
// NAME identifies which credential a machine is using and is exactly what you
// want in a support bundle; only the secret after the last colon goes. Paths,
// snapshot ids, hostnames and URLs are untouched.
//
// Deliberately the same shapes as the server's Core\Log::redactLine, because
// they are the same secrets crossing the same wire — and an engineer reading
// both halves of one incident should not have to learn two redaction
// vocabularies.

const redactedMarker = "[redacted]"

var logRedactors = []struct {
	re   *regexp.Regexp
	with string
}{
	// Authorization headers in every form this codebase builds them.
	// `\S+(?:\s+\S+)?` and not `\S+`: a header is usually "Scheme
	// credential", and matching a single token leaves the credential itself
	// sitting on the line. Found by this package's tests; the server's
	// Core\Log carried the identical hole, where it had gone unnoticed
	// because its own test exercised the key-based path instead.
	{regexp.MustCompile(`(?i)\b(Authorization|X-Nimbus-Token|PBSAuthCookie)\s*[:=]\s*\S+(?:\s+\S+)?`),
		"$1: " + redactedMarker},

	// PBS API tokens: user@realm!tokenid:<secret>. The name survives.
	{regexp.MustCompile(`([A-Za-z0-9._-]+@[A-Za-z0-9._-]+![A-Za-z0-9._-]+):[A-Za-z0-9-]{8,}`),
		"$1:" + redactedMarker},

	// key=value where the key smells secret, including inside a URL query.
	{regexp.MustCompile(`(?i)\b([A-Za-z0-9_-]*(?:password|secret|token|passwd|apikey|api_key)[A-Za-z0-9_-]*)\s*=\s*[^\s&"']+`),
		"$1=" + redactedMarker},

	// An encryption key or fingerprint rendered as hex. Bounded at 32 to
	// avoid eating chunk digests and snapshot ids, which are the things
	// these logs exist to let you trace.
	{regexp.MustCompile(`(?i)\b(key|secret|fingerprint)\s*[:=]\s*[0-9a-f]{32,}\b`),
		"$1=" + redactedMarker},
}

// redactLogLine removes secrets from one rendered line.
func redactLogLine(line string) string {
	for _, r := range logRedactors {
		line = r.re.ReplaceAllString(line, r.with)
	}
	return line
}
