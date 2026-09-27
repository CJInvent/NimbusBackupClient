package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func captured(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "vss-errors", name))
	if err != nil {
		t.Fatalf("capture %s: %v", name, err)
	}
	return strings.TrimSpace(string(b))
}

var (
	longHexRun = regexp.MustCompile(`[0-9a-fA-F]{9,}`)
	// The volume GUID in a message is hex too; StripHex removes only the
	// library's parenthesised encoding.
	hexParen = regexp.MustCompile(`\(0x[0-9a-fA-F]{9,}\)`)
)

// The real messages from the test VM (dev rule 25) give the right name, code
// and step, and the summary carries no hex run longer than a code. Against
// the output before this helper existed, every one of these fails: the
// message had no numeric code at all, only hex-encoded text.
func TestCapturedVSSFailuresAreDescribed(t *testing.T) {
	for _, c := range []struct {
		file, name string
		code       uint32
		step, text string
	}{
		{"run113-initializeforbackup-unexpected.txt", "VSS_E_UNEXPECTED", 0x80042302, "InitializeForBackup",
			"A volume shadow copy service (VSS) component encountered an unexpected error."},
		{"run1790421299-4-isvolumesupported-object-not-found.txt", "VSS_E_OBJECT_NOT_FOUND", 0x80042308, "IsVolumeSupported",
			"The specified object was not found."},
	} {
		msg := captured(t, c.file)
		info := DescribeVSSError(errors.New(msg))
		if info.Name != c.name || info.Code != c.code || info.Step != c.step || info.Text != c.text {
			t.Errorf("%s: got %+v", c.file, info)
		}
		sum := info.Summary()
		if longHexRun.MatchString(sum) {
			t.Errorf("%s: summary still carries hex: %q", c.file, sum)
		}
		if !strings.HasPrefix(sum, c.name+" (0x") {
			t.Errorf("%s: name and code must come first: %q", c.file, sum)
		}
		if len(sum) > 300 {
			t.Errorf("%s: summary is %d bytes", c.file, len(sum))
		}
		if StripHex(msg) == msg || hexParen.MatchString(StripHex(msg)) {
			t.Errorf("%s: StripHex left the hex in", c.file)
		}
	}
	if want := "VSS_E_OBJECT_NOT_FOUND (0x80042308) during IsVolumeSupported: The specified object was not found."; DescribeVSSError(errors.New(captured(t, "run1790421299-4-isvolumesupported-object-not-found.txt"))).Summary() != want {
		t.Errorf("summary shape changed; want %q", want)
	}
}

// What the portal showed (event 403): a prefix and the tail of the hex. The
// head of a summary is what survives now; this pins that the old tail was
// indeed unreadable, so a regression to tail-clipping is recognizable.
func TestServerEvent403WasHexOnly(t *testing.T) {
	ev := captured(t, "server-event-403.txt")
	if !strings.HasPrefix(ev, "Snapshot creation failed: ") || !longHexRun.MatchString(ev) {
		t.Fatalf("capture is not the hex-tail event it documents: %q", ev)
	}
}

// Every table entry's text starts with its name, so a go-vss bump that changes
// the text fails here, loudly, instead of silently unclassifying failures.
func TestTheHRESULTTableFollowsTheLibrary(t *testing.T) {
	seen := map[string]bool{}
	for _, h := range knownHRESULTs {
		if seen[h.name] {
			t.Errorf("duplicate entry %s", h.name)
		}
		seen[h.name] = true
		if !strings.HasPrefix(h.code.String(), h.name) {
			t.Errorf("%s: library text %q does not start with the name", h.name, h.code.String())
		}
	}
	for _, name := range []string{"VSS_E_SNAPSHOT_SET_IN_PROGRESS", "VSS_E_UNEXPECTED_PROVIDER_ERROR", "E_ACCESSDENIED",
		"VSS_E_WRITERERROR_NONRETRYABLE"} {
		if !seen[name] {
			t.Errorf("the classifier needs %s", name)
		}
	}
}

// A real numeric code (should a later library print one) and an unknown
// failure: the first is classified, the second keeps its text without hex.
func TestNumericCodesAndUnknownFailures(t *testing.T) {
	if got := DescribeVSSError(errors.New("VSS_SNAPSHOT - Shadow copy creation failed: DoSnapshotSet, err: (0x80042316)")); got.Name != "VSS_E_SNAPSHOT_SET_IN_PROGRESS" || got.Step != "DoSnapshotSet" {
		t.Errorf("numeric code: %+v", got)
	}
	unk := DescribeVSSError(errors.New("VSS_CREATE - Failed to create the VSS backup component: 5 (0x" + strings.Repeat("ab", 20) + ")"))
	if unk.Name != "" || longHexRun.MatchString(unk.Summary()) || !strings.Contains(unk.Summary(), "Failed to create the VSS backup component") {
		t.Errorf("unknown: %+v -> %q", unk, unk.Summary())
	}
}

func TestClipHeadKeepsTheHeadOnARuneBoundary(t *testing.T) {
	s := strings.Repeat("a", 298) + "éééé"
	got := clipHeadBytes(s, 300)
	if len(got) > 300 || !utf8.ValidString(got) || !strings.HasPrefix(got, strings.Repeat("a", 297)) || !strings.HasSuffix(got, "…") {
		t.Fatalf("got %d bytes %q", len(got), got[len(got)-10:])
	}
}
