package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// The SHARED calendar fixtures, run against the Go evaluator.
//
// testdata/calendar-fixtures.json is a byte-identical copy of
// docs/calendar-fixtures.json in NimbusControl, and the PHP parser's suite
// (tests/calendar_fixtures.php) reads the same artifact. Every date in it was
// worked out from a calendar rather than produced by running either parser —
// dev rule 25, because a fixture generated from one implementation makes the
// other's test a check that it reproduces a particular bug.
//
// If these two implementations ever disagree, the fleet backs up on one
// schedule and is judged late against another, and the symptom is alerts
// nobody can explain. This file is what makes that a build failure instead.
const fixturesSHA256 = "6dcf0d616d70cc1b729481665cbf8661fa287faacfefac66442295e5bdaec83a"

type occCase struct {
	Why    string   `json:"why"`
	Expr   string   `json:"expr"`
	TZ     string   `json:"tz"`
	From   string   `json:"from"`
	Expect []string `json:"expect"`
	UTC    []string `json:"utc"`
}

type gapCase struct {
	Why    string `json:"why"`
	Expr   string `json:"expr"`
	TZ     string `json:"tz"`
	From   string `json:"from"`
	Expect *int64 `json:"expect"`
}

type fixtures struct {
	Next    []occCase `json:"next_occurrences"`
	Gaps    []gapCase `json:"largest_gap_seconds"`
	Valid   []string  `json:"valid"`
	Invalid []string  `json:"invalid"`
}

const layout = "2006-01-02 15:04:05"

func loadFixtures(t *testing.T) fixtures {
	t.Helper()
	raw, err := os.ReadFile("testdata/calendar-fixtures.json")
	if err != nil {
		t.Fatalf("reading fixtures: %v", err)
	}

	// The hash catches an accidental one-sided edit, which is the realistic
	// failure: someone fixes a date here and forgets the server's copy, and
	// the two implementations quietly start meaning different things. It
	// cannot catch a deliberate simultaneous edit to both, and does not
	// claim to.
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != fixturesSHA256 {
		t.Fatalf("fixtures have diverged from NimbusControl docs/calendar-fixtures.json\n"+
			"  have %s\n  want %s\n"+
			"If this change is intentional, update BOTH copies and both recorded hashes.", got, fixturesSHA256)
	}

	var fx fixtures
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parsing fixtures: %v", err)
	}
	if len(fx.Next) == 0 || len(fx.Gaps) == 0 {
		t.Fatal("fixtures loaded but empty — a passing run would prove nothing")
	}
	return fx
}

func TestCalendarFixturesNextOccurrences(t *testing.T) {
	for _, c := range loadFixtures(t).Next {
		t.Run(c.Expr+" ["+c.TZ+"]", func(t *testing.T) {
			loc, err := time.LoadLocation(c.TZ)
			if err != nil {
				t.Fatalf("timezone %s: %v", c.TZ, err)
			}
			from, err := time.ParseInLocation(layout, c.From, loc)
			if err != nil {
				t.Fatalf("from %s: %v", c.From, err)
			}
			s, err := ParseCalendar(c.Expr)
			if err != nil {
				t.Fatalf("parsing %q: %v", c.Expr, err)
			}

			got := s.NextOccurrences(loc, from, len(c.Expect))
			if len(got) != len(c.Expect) {
				t.Fatalf("got %d occurrences, want %d (%s)", len(got), len(c.Expect), c.Why)
			}
			for i, want := range c.Expect {
				if g := got[i].Format(layout); g != want {
					t.Errorf("occurrence %d = %s, want %s\n  %s", i, g, want, c.Why)
				}
			}

			// Where the fixture pins UTC too, the point is that a local
			// wall clock and an instant are different facts. Asserting
			// only the local form would pass on an implementation that
			// ignored the timezone entirely.
			for i, want := range c.UTC {
				if g := got[i].UTC().Format(layout); g != want {
					t.Errorf("occurrence %d as UTC = %s, want %s", i, g, want)
				}
			}
		})
	}
}

func TestCalendarFixturesLargestGap(t *testing.T) {
	for _, c := range loadFixtures(t).Gaps {
		t.Run(c.Expr+" ["+c.TZ+"]", func(t *testing.T) {
			loc, err := time.LoadLocation(c.TZ)
			if err != nil {
				t.Fatalf("timezone %s: %v", c.TZ, err)
			}
			from, err := time.ParseInLocation(layout, c.From, loc)
			if err != nil {
				t.Fatalf("from %s: %v", c.From, err)
			}
			s, err := ParseCalendar(c.Expr)
			if err != nil {
				t.Fatalf("parsing %q: %v", c.Expr, err)
			}

			got, ok := s.LargestGapSeconds(loc, from)
			if c.Expect == nil {
				if ok {
					t.Errorf("derived %d seconds, want NO interval\n  %s", got, c.Why)
				}
				return
			}
			if !ok {
				t.Fatalf("derived no interval, want %d\n  %s", *c.Expect, c.Why)
			}
			if got != *c.Expect {
				t.Errorf("derived %d, want %d\n  %s", got, *c.Expect, c.Why)
			}
		})
	}
}

func TestCalendarFixturesGrammar(t *testing.T) {
	fx := loadFixtures(t)
	for _, e := range fx.Valid {
		if !CalendarIsValid(e) {
			_, err := ParseCalendar(e)
			t.Errorf("the fixtures call %q valid, but it was refused: %v", e, err)
		}
	}
	for _, e := range fx.Invalid {
		if CalendarIsValid(e) {
			t.Errorf("the fixtures call %q invalid, but it parsed", e)
		}
	}
}

func TestCalendarScanTerminates(t *testing.T) {
	// An expression matching nothing reachable must stop, not spin.
	loc := time.UTC
	from := time.Date(2026, 8, 5, 0, 0, 0, 0, loc)
	start := time.Now()
	if got := (mustParse(t, "*-02-30 00:00")).NextOccurrence(loc, from); !got.IsZero() {
		t.Errorf("February 30th matched %s", got)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the scan took %v; the ceiling is not bounding it", d)
	}

	// The densest expression the grammar allows must also be fast.
	start = time.Now()
	mustParse(t, "*:*:*").NextOccurrences(loc, from, 100)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("*:*:* took %v", d)
	}
}

func TestCalendarStrictlyAfterAcrossDST(t *testing.T) {
	// Not in the fixtures because it is about THIS implementation's cursor:
	// advancing with AddDate rather than Add(24h). Across a transition a day
	// is not 24 hours, and a fixed-duration cursor drifts off midnight and
	// eventually skips or repeats a calendar day.
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	s := mustParse(t, "*-*-* 12:00")
	from := time.Date(2026, 3, 6, 0, 0, 0, 0, loc)
	got := s.NextOccurrences(loc, from, 5)
	want := []string{
		"2026-03-06 12:00:00",
		"2026-03-07 12:00:00",
		"2026-03-08 12:00:00", // spring forward — still noon, still one per day
		"2026-03-09 12:00:00",
		"2026-03-10 12:00:00",
	}
	for i, w := range want {
		if g := got[i].Format(layout); g != w {
			t.Errorf("day %d = %s, want %s", i, g, w)
		}
	}
}

func mustParse(t *testing.T, expr string) *CalendarSchedule {
	t.Helper()
	s, err := ParseCalendar(expr)
	if err != nil {
		t.Fatalf("parsing %q: %v", expr, err)
	}
	return s
}
