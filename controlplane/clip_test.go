package controlplane

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Error summaries keep their HEAD (ledger F-43): a VSS summary's tail was the
// library's hex-encoded description, which is what the portal showed. Logs
// keep their tail. The server stores 500 BYTES of a summary, so the clipped
// head must fit in 500 bytes and never split a character.
func TestSummariesKeepTheHeadAndLogsKeepTheTail(t *testing.T) {
	long := "VSS_E_UNEXPECTED (0x80042302) during InitializeForBackup: " + strings.Repeat("x", 900)
	got := clipHead(long, 500)
	if len(got) > 500 || !strings.HasPrefix(got, long[:497]) || !strings.HasSuffix(got, "…") {
		t.Fatalf("clipHead: %d bytes, head kept: %v", len(got), strings.HasPrefix(got, long[:497]))
	}
	wide := strings.Repeat("é", 400) // 800 bytes of two-byte runes
	if g := clipHead(wide, 500); len(g) > 500 || !utf8.ValidString(g) {
		t.Fatalf("clipHead split a character or overran: %d bytes, valid=%v", len(g), utf8.ValidString(g))
	}
	if clipHead("short", 500) != "short" {
		t.Fatal("a short summary was changed")
	}
	if tail := clip(long, 16); tail != long[len(long)-16:] {
		t.Fatalf("clip must keep the tail: %q", tail)
	}
}
