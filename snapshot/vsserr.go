package snapshot

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	vss "github.com/st-matskevich/go-vss"
)

// VSS failure text an operator can read (NimbusControl V4-BETA-FIXES §3.1,
// ledger F-43).
//
// WHY THIS EXISTS. go-vss v0.3.3 renders a VSS failure as
//
//	fmt.Sprintf("VSS error: %s: %s (%#x)", text, hresult.String(), hresult)
//
// and HRESULT has a String() method, so fmt formats the `%#x` operand by
// hex-encoding the STRING: the parenthesis is the whole description again,
// in hex, not the code. The run report then kept the TAIL of a 500-character
// summary, which for this message is the hex, and the portal showed
// "Snapshot creation failed: 5722066696c65..." (event 403). No numeric HRESULT
// appeared anywhere, so every classifier matching one by code could never
// match (the in-progress reset, the writer-error tolerance: dead wiring).
//
// DescribeVSSError decodes the hex back into the text, looks the text up
// among the library's OWN exported HRESULT constants (by their String(), so
// the table follows the library), and yields the name, the real code, the
// call that failed and one sentence of description.

// VSSErrorInfo is a VSS failure, classified.
type VSSErrorInfo struct {
	Name string // e.g. "VSS_E_OBJECT_NOT_FOUND"; "" when not recognized
	Code uint32 // the HRESULT; 0 when not recognized
	Step string // the VSS call that failed, e.g. "IsVolumeSupported"
	Text string // one sentence of the library's description
	raw  string // the original message, for the unrecognized case
}

// knownHRESULTs lists the library's exported HRESULT constants by name. The
// names are ours (Go cannot enumerate constants); the codes and the text they
// are matched by come from the library, and TestTheHRESULTTableFollowsTheLibrary
// fails if a library bump changes a String() so it no longer starts with its
// name.
var knownHRESULTs = []struct {
	name string
	code vss.HRESULT
}{
	{"E_ACCESSDENIED", vss.E_ACCESSDENIED},
	{"E_OUTOFMEMORY", vss.E_OUTOFMEMORY},
	{"E_INVALIDARG", vss.E_INVALIDARG},
	{"VSS_E_BAD_STATE", vss.VSS_E_BAD_STATE},
	{"VSS_E_UNEXPECTED", vss.VSS_E_UNEXPECTED},
	{"VSS_E_PROVIDER_ALREADY_REGISTERED", vss.VSS_E_PROVIDER_ALREADY_REGISTERED},
	{"VSS_E_PROVIDER_NOT_REGISTERED", vss.VSS_E_PROVIDER_NOT_REGISTERED},
	{"VSS_E_PROVIDER_VETO", vss.VSS_E_PROVIDER_VETO},
	{"VSS_E_PROVIDER_IN_USE", vss.VSS_E_PROVIDER_IN_USE},
	{"VSS_E_OBJECT_NOT_FOUND", vss.VSS_E_OBJECT_NOT_FOUND},
	{"VSS_E_VOLUME_NOT_SUPPORTED", vss.VSS_E_VOLUME_NOT_SUPPORTED},
	{"VSS_E_VOLUME_NOT_SUPPORTED_BY_PROVIDER", vss.VSS_E_VOLUME_NOT_SUPPORTED_BY_PROVIDER},
	{"VSS_E_OBJECT_ALREADY_EXISTS", vss.VSS_E_OBJECT_ALREADY_EXISTS},
	{"VSS_E_UNEXPECTED_PROVIDER_ERROR", vss.VSS_E_UNEXPECTED_PROVIDER_ERROR},
	{"VSS_E_CORRUPT_XML_DOCUMENT", vss.VSS_E_CORRUPT_XML_DOCUMENT},
	{"VSS_E_INVALID_XML_DOCUMENT", vss.VSS_E_INVALID_XML_DOCUMENT},
	{"VSS_E_MAXIMUM_NUMBER_OF_VOLUMES_REACHED", vss.VSS_E_MAXIMUM_NUMBER_OF_VOLUMES_REACHED},
	{"VSS_E_FLUSH_WRITES_TIMEOUT", vss.VSS_E_FLUSH_WRITES_TIMEOUT},
	{"VSS_E_HOLD_WRITES_TIMEOUT", vss.VSS_E_HOLD_WRITES_TIMEOUT},
	{"VSS_E_UNEXPECTED_WRITER_ERROR", vss.VSS_E_UNEXPECTED_WRITER_ERROR},
	{"VSS_E_SNAPSHOT_SET_IN_PROGRESS", vss.VSS_E_SNAPSHOT_SET_IN_PROGRESS},
	{"VSS_E_MAXIMUM_NUMBER_OF_SNAPSHOTS_REACHED", vss.VSS_E_MAXIMUM_NUMBER_OF_SNAPSHOTS_REACHED},
	{"VSS_E_WRITER_INFRASTRUCTURE", vss.VSS_E_WRITER_INFRASTRUCTURE},
	{"VSS_E_WRITER_NOT_RESPONDING", vss.VSS_E_WRITER_NOT_RESPONDING},
	{"VSS_E_WRITER_ALREADY_SUBSCRIBED", vss.VSS_E_WRITER_ALREADY_SUBSCRIBED},
	{"VSS_E_UNSUPPORTED_CONTEXT", vss.VSS_E_UNSUPPORTED_CONTEXT},
	{"VSS_E_VOLUME_IN_USE", vss.VSS_E_VOLUME_IN_USE},
	{"VSS_E_MAXIMUM_DIFFAREA_ASSOCIATIONS_REACHED", vss.VSS_E_MAXIMUM_DIFFAREA_ASSOCIATIONS_REACHED},
	{"VSS_E_INSUFFICIENT_STORAGE", vss.VSS_E_INSUFFICIENT_STORAGE},
	{"VSS_E_NO_SNAPSHOTS_IMPORTED", vss.VSS_E_NO_SNAPSHOTS_IMPORTED},
	{"VSS_E_SOME_SNAPSHOTS_NOT_IMPORTED", vss.VSS_E_SOME_SNAPSHOTS_NOT_IMPORTED},
	{"VSS_E_MAXIMUM_NUMBER_OF_REMOTE_MACHINES_REACHED", vss.VSS_E_MAXIMUM_NUMBER_OF_REMOTE_MACHINES_REACHED},
	{"VSS_E_REMOTE_SERVER_UNAVAILABLE", vss.VSS_E_REMOTE_SERVER_UNAVAILABLE},
	{"VSS_E_REMOTE_SERVER_UNSUPPORTED", vss.VSS_E_REMOTE_SERVER_UNSUPPORTED},
	{"VSS_E_REVERT_IN_PROGRESS", vss.VSS_E_REVERT_IN_PROGRESS},
	{"VSS_E_REVERT_VOLUME_LOST", vss.VSS_E_REVERT_VOLUME_LOST},
	{"VSS_E_REBOOT_REQUIRED", vss.VSS_E_REBOOT_REQUIRED},
	{"VSS_E_TRANSACTION_FREEZE_TIMEOUT", vss.VSS_E_TRANSACTION_FREEZE_TIMEOUT},
	{"VSS_E_TRANSACTION_THAW_TIMEOUT", vss.VSS_E_TRANSACTION_THAW_TIMEOUT},
	{"VSS_E_VOLUME_NOT_LOCAL", vss.VSS_E_VOLUME_NOT_LOCAL},
	{"VSS_E_CLUSTER_TIMEOUT", vss.VSS_E_CLUSTER_TIMEOUT},
	{"VSS_E_WRITERERROR_INCONSISTENTSNAPSHOT", vss.VSS_E_WRITERERROR_INCONSISTENTSNAPSHOT},
	{"VSS_E_WRITERERROR_OUTOFRESOURCES", vss.VSS_E_WRITERERROR_OUTOFRESOURCES},
	{"VSS_E_WRITERERROR_TIMEOUT", vss.VSS_E_WRITERERROR_TIMEOUT},
	{"VSS_E_WRITERERROR_RETRYABLE", vss.VSS_E_WRITERERROR_RETRYABLE},
	{"VSS_E_WRITERERROR_NONRETRYABLE", vss.VSS_E_WRITERERROR_NONRETRYABLE},
	{"VSS_E_WRITERERROR_RECOVERY_FAILED", vss.VSS_E_WRITERERROR_RECOVERY_FAILED},
	{"VSS_E_BREAK_REVERT_ID_FAILED", vss.VSS_E_BREAK_REVERT_ID_FAILED},
	{"VSS_E_LEGACY_PROVIDER", vss.VSS_E_LEGACY_PROVIDER},
	{"VSS_E_MISSING_DISK", vss.VSS_E_MISSING_DISK},
	{"VSS_E_MISSING_HIDDEN_VOLUME", vss.VSS_E_MISSING_HIDDEN_VOLUME},
	{"VSS_E_MISSING_VOLUME", vss.VSS_E_MISSING_VOLUME},
	{"VSS_E_AUTORECOVERY_FAILED", vss.VSS_E_AUTORECOVERY_FAILED},
	{"VSS_E_DYNAMIC_DISK_ERROR", vss.VSS_E_DYNAMIC_DISK_ERROR},
	{"VSS_E_NONTRANSPORTABLE_BCD", vss.VSS_E_NONTRANSPORTABLE_BCD},
	{"VSS_E_CANNOT_REVERT_DISKID", vss.VSS_E_CANNOT_REVERT_DISKID},
	{"VSS_E_RESYNC_IN_PROGRESS", vss.VSS_E_RESYNC_IN_PROGRESS},
	{"VSS_E_CLUSTER_ERROR", vss.VSS_E_CLUSTER_ERROR},
	{"VSS_E_UNSELECTED_VOLUME", vss.VSS_E_UNSELECTED_VOLUME},
	{"VSS_E_SNAPSHOT_NOT_IN_SET", vss.VSS_E_SNAPSHOT_NOT_IN_SET},
	{"VSS_E_NESTED_VOLUME_LIMIT", vss.VSS_E_NESTED_VOLUME_LIMIT},
	{"VSS_E_NOT_SUPPORTED", vss.VSS_E_NOT_SUPPORTED},
	{"VSS_E_WRITERERROR_PARTIAL_FAILURE", vss.VSS_E_WRITERERROR_PARTIAL_FAILURE},
	{"VSS_E_WRITER_STATUS_NOT_AVAILABLE", vss.VSS_E_WRITER_STATUS_NOT_AVAILABLE},
}

var (
	hresultByText = map[string]int{}
	hresultByCode = map[uint32]int{}
)

func init() {
	for i, h := range knownHRESULTs {
		hresultByText[h.code.String()] = i
		hresultByCode[uint32(h.code)] = i
	}
}

var (
	hexGroupRe = regexp.MustCompile(`\(0x([0-9a-fA-F]+)\)`)
	longHexRe  = regexp.MustCompile(`\s*\(0x[0-9a-fA-F]{9,}\)`)
	comStepRe  = regexp.MustCompile(`IVssBackupComponents\.([A-Za-z]+):`)
	opStepRe   = regexp.MustCompile(`failed: ([A-Za-z]+)[, ]`)
	tagStepRe  = regexp.MustCompile(`\b(VSS_[A-Z]+) - `)
	nameRe     = regexp.MustCompile(`\b((?:VSS_E|E)_[A-Z_]+)\b`)
)

// DescribeVSSError classifies a VSS failure from its message.
func DescribeVSSError(err error) VSSErrorInfo {
	if err == nil {
		return VSSErrorInfo{}
	}
	msg := err.Error()
	info := VSSErrorInfo{raw: msg}

	idx := -1
	for _, m := range hexGroupRe.FindAllStringSubmatch(msg, -1) {
		h := m[1]
		if len(h) <= 8 {
			// A real numeric code, should a later library print one.
			if v, perr := strconv.ParseUint(h, 16, 32); perr == nil {
				if i, ok := hresultByCode[uint32(v)]; ok {
					idx = i
				}
			}
			continue
		}
		if len(h)%2 != 0 {
			continue
		}
		b, derr := hex.DecodeString(h)
		if derr != nil || !printable(b) {
			continue
		}
		if i, ok := hresultByText[string(b)]; ok {
			idx = i
		}
	}
	if idx < 0 {
		// No usable parenthesis: fall back to a known name in the text.
		for _, m := range nameRe.FindAllStringSubmatch(msg, -1) {
			for i, h := range knownHRESULTs {
				if h.name == m[1] {
					idx = i
				}
			}
		}
	}
	if idx >= 0 {
		h := knownHRESULTs[idx]
		info.Name, info.Code = h.name, uint32(h.code)
		info.Text = firstSentence(strings.TrimPrefix(h.code.String(), h.name+" - "))
		if info.Text == h.name {
			info.Text = ""
		}
	}

	switch {
	case comStepRe.MatchString(msg):
		info.Step = comStepRe.FindStringSubmatch(msg)[1]
	case opStepRe.MatchString(msg):
		info.Step = opStepRe.FindStringSubmatch(msg)[1]
	case tagStepRe.MatchString(msg):
		info.Step = tagStepRe.FindStringSubmatch(msg)[1]
	}
	return info
}

// Summary is the one line an operator reads: name and code FIRST, so no
// truncation anywhere downstream can lose them, then the call and one
// sentence. At most summaryMax bytes. An unrecognized failure is its own
// text with any hex parenthesis removed (unknown hex is dropped, never shown).
func (i VSSErrorInfo) Summary() string {
	var s string
	if i.Name != "" {
		s = fmt.Sprintf("%s (0x%08x)", i.Name, i.Code)
		if i.Step != "" {
			s += " during " + i.Step
		}
		if i.Text != "" {
			s += ": " + i.Text
		}
	} else {
		s = strings.TrimSpace(longHexRe.ReplaceAllString(i.raw, ""))
	}
	return clipHeadBytes(s, summaryMax)
}

const summaryMax = 300

// Error is a VSS failure leaving this package. Error() is the summary; the
// library's own message stays reachable through Unwrap for anything that
// needs it, and in the ERROR line logged where it happened.
type Error struct {
	Volume  string
	Info    VSSErrorInfo
	message string // set when the outcome has its own sentence (in progress)
	cause   error
}

func (e *Error) Error() string {
	if e.message != "" {
		return e.message
	}
	return e.Info.Summary()
}
func (e *Error) Unwrap() error { return e.cause }

func newError(volume string, cause error) *Error {
	return &Error{Volume: volume, Info: DescribeVSSError(cause), cause: cause}
}

// StripHex removes go-vss's hex-encoded description from a message. For log
// lines that quote a library error verbatim.
func StripHex(msg string) string {
	return strings.TrimSpace(longHexRe.ReplaceAllString(msg, ""))
}

func printable(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if !unicode.IsPrint(r) && r != ' ' {
			return false
		}
	}
	return true
}

// firstSentence is the text up to and including the first ". ", or all of it.
func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}

// clipHeadBytes keeps the HEAD of s within n bytes, on a rune boundary, with
// a trailing ellipsis when anything was cut.
func clipHeadBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const ell = "…"
	cut := n - len(ell)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ell
}
