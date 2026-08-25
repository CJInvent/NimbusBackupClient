package controlplane

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Restore reporting — POST /api/agent/v1/restores (V4-SPEC §9.7 step 2).
//
// WHY AN AGENT REPORTS ITS OWN RESTORES. A restore fetches a backup key and no
// backup run follows it, so it is unmatched by construction in the server's
// key-release audit. Until this existed, `mode=restore` was the only thing
// distinguishing "a person restored a file" from "something took the key and
// vanished" — one field, chosen by this process, which an attacker holding a
// stolen agent token would simply set.
//
// THIS DOES NOT MAKE THE CLAIM HONEST, and the report must not be written as
// though it does. The same token authorises the key fetch and this report, so
// an attacker can write both. What it buys is that the two records have to
// AGREE: a lie now spans two calls made at different moments with different
// shapes, instead of one boolean-ish field. The server grades it accordingly —
// a release backed only by this report reads `reported`, below one backed by a
// portal action a named user took.
//
// A FAILED REPORT MUST NEVER FAIL A RESTORE. Recovery is the moment the
// product exists for. If the control server is unreachable, or slow, or
// returns 500, the restore continues and the report is lost — an audit gap is
// a far smaller harm than a restore that would not run because telemetry was
// down.

// RestoreStatus is the lifecycle of a reported restore. There is no
// intermediate phase: unlike a backup, a restore has nothing between "started"
// and "finished" that the server acts on.
type RestoreStatus string

const (
	RestoreRunning   RestoreStatus = "running"
	RestoreSuccess   RestoreStatus = "success"
	RestoreFailed    RestoreStatus = "failed"
	RestoreCancelled RestoreStatus = "cancelled"
)

// RestoreKind is what was READ, not what it was written to.
//
// There is deliberately no value meaning "a whole partition". The client
// restores files and folders; it cannot write an image over a live disk and
// must not be able to. Naming a kind for it here would be the first step
// towards someone implementing one.
type RestoreKind string

const (
	// RestoreArchive — files out of a directory backup's pxar archive.
	RestoreArchive RestoreKind = "archive"
	// RestoreVolume — files out of a stored disk image. The SOURCE is an
	// image; the operation is still a file restore.
	RestoreVolume RestoreKind = "volume"
)

// RestoreSource is where this agent believes the restore was driven from.
//
// RestoreConsole is not suspicious. Somebody using the machine's own GUI
// leaves no command on the server by design, and that is a supported way to
// work — the server's audit reads an uncorroborated console restore as
// `reported`, not as a finding.
const (
	RestoreConsole = "console"
	RestorePortal  = "portal"
	RestoreCLI     = "cli"
)

// RestoreReport is the body of POST /api/agent/v1/restores. The same
// RestoreUUID is posted twice: once at the start, once at the end.
type RestoreReport struct {
	RestoreUUID string        `json:"restore_uuid"`
	Kind        RestoreKind   `json:"kind"`
	Source      string        `json:"source,omitempty"`
	Status      RestoreStatus `json:"status"`
	StartedAt   string        `json:"started_at"`            // ISO 8601 (RFC 3339)
	FinishedAt  string        `json:"finished_at,omitempty"` // terminal statuses only
	// CommandID is set only when this agent was acting on a command the
	// server issued. A command id belonging to another agent is DROPPED by
	// the server rather than rejected, so a wrong value costs attribution
	// and not the record.
	CommandID    int64  `json:"command_id,omitempty"`
	Snapshot     string `json:"snapshot,omitempty"`
	KeyID        string `json:"key_id,omitempty"`
	ItemCount    int    `json:"item_count,omitempty"`
	BytesWritten int64  `json:"bytes_written,omitempty"`
	ErrorSummary string `json:"error_summary,omitempty"`
}

// RestoreOutcome turns a restore's ending into what gets reported.
//
// Separated from the service-side call site so the POLICY is testable: the
// call site only has to answer "was this a cancellation", which is a sentinel
// comparison, while the two judgements that could be wrong live here.
//
// A CANCELLATION IS NOT A FAILURE. Recording it as one puts a red row in a
// portal history for something the operator meant to do, and a history whose
// red rows include deliberate acts is one people learn to skim past — which
// costs exactly the attention the audit was built to attract.
//
// The summary is capped here rather than trusted to the server's own
// truncation. A restore that failed on a path list can produce a very long
// error, and there is no reason to push it across the wire to be cut at the
// far end.
func RestoreOutcome(err error, cancelled bool) (RestoreStatus, string) {
	switch {
	case err == nil:
		return RestoreSuccess, ""
	case cancelled:
		// Deliberately BEFORE the failure case and independent of it: a
		// cancelled restore also returns a non-nil error, so testing the
		// error first would classify every cancellation as a failure.
		return RestoreCancelled, ""
	}
	summary := err.Error()
	if len(summary) > restoreSummaryMax {
		summary = summary[:restoreSummaryMax]
	}
	return RestoreFailed, summary
}

// Matches the server's own column truncation, so a summary that survives this
// is a summary the portal will show whole.
const restoreSummaryMax = 2000

// ReportRestore posts one restore report.
//
// The error is returned rather than swallowed HERE so that callers can log it;
// the rule that a restore must not fail over it belongs at the call site,
// where the restore actually is.
func (c *Client) ReportRestore(r RestoreReport) error {
	return c.post("/api/agent/v1/restores", r, nil, true)
}

// NewRestoreUUID generates the identifier a restore is reported under.
//
// Version 4, from crypto/rand. An error is returned rather than falling back
// to something weaker: the server refuses a malformed uuid, and a
// deterministic fallback would make two machines collide on the same restore
// id — which the server's per-agent uniqueness would hide until it did not.
func NewRestoreUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating a restore id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

// RestoreStartedAt renders an instant the way the server's validator expects.
func RestoreStartedAt(t time.Time) string { return t.UTC().Format(time.RFC3339) }
