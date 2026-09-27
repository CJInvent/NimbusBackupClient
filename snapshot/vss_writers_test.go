package snapshot

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Every captured listing in testdata/vssadmin parses to the writers its
// sidecar names as failed (V4-BETA-FIXES §4, ledger F-36). There must be at
// least one: a directory that silently held nothing would pass this vacuously.
func TestCapturedWriterListingsParse(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "vssadmin", "*.txt"))
	if len(files) == 0 {
		t.Fatal("no vssadmin captures in testdata/vssadmin")
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		wantRaw, err := os.ReadFile(strings.TrimSuffix(f, ".txt") + ".want")
		if err != nil {
			t.Fatalf("%s has no .want sidecar: %v", f, err)
		}
		lines := strings.Split(strings.TrimSpace(string(wantRaw)), "\n")
		wantTotal := -1
		if len(lines) > 0 && strings.HasPrefix(lines[0], "writers=") {
			wantTotal, _ = strconv.Atoi(strings.TrimPrefix(lines[0], "writers="))
			lines = lines[1:]
		}
		var want []string
		for _, l := range lines {
			if l = strings.TrimSpace(l); l != "" {
				want = append(want, l)
			}
		}
		ws := parseWriters(string(raw))
		if wantTotal >= 0 && len(ws) != wantTotal {
			t.Errorf("%s: parsed %d writers, want %d", f, len(ws), wantTotal)
		}
		got := WriterNames(failedWriters(ws))
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: failed writers %q, want %q", f, got, want)
		}
		for _, w := range failedWriters(ws) {
			if w.LastError == "" {
				t.Errorf("%s: %s has no last error", f, w.Name)
			}
		}
	}
}

// The decision reads the bracketed state number, never the words: a listing
// whose labels and state words are in another language decides identically,
// and a writer whose "last error" text is not "No error" is NOT failed on
// that account (the old parser's rule, which blamed every writer on a
// non-English machine).
func TestTheStateNumberDecidesNotTheWords(t *testing.T) {
	en := "Writer name: 'System Writer'\n   State: [1] Stable\n   Last error: No error\n\n" +
		"Writer name: 'SqlServerWriter'\n   State: [7] Failed\n   Last error: Non-retryable error\n\n" +
		"Writer name: 'WMI Writer'\n   State: [5] Waiting for completion\n   Last error: No error\n"
	relabeled := strings.NewReplacer("Writer name", "Label A", "State", "Label B", "Last error", "Label C",
		"Stable", "words", "Failed", "words", "No error", "other words", "Waiting for completion", "words").Replace(en)
	for name, listing := range map[string]string{"labels as given": en, "labels and words changed": relabeled} {
		ws := parseWriters(listing)
		if len(ws) != 3 {
			t.Fatalf("%s: parsed %d writers", name, len(ws))
		}
		if got := WriterNames(failedWriters(ws)); len(got) != 1 || got[0] != "SqlServerWriter" {
			t.Errorf("%s: failed %q", name, got)
		}
		if busy := busyWriters(ws); len(busy) != 1 || !strings.Contains(busy[0], "WMI Writer") {
			t.Errorf("%s: busy %q", name, busy)
		}
	}
}
