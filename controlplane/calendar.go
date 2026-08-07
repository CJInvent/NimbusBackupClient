package controlplane

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CalendarSchedule — PVE/systemd calendar events, evaluated on the agent.
//
// THIS IS THE SECOND IMPLEMENTATION OF ONE GRAMMAR, and that is a liability
// worth naming at the top of the file. The server has
// Nimbus\Backups\CalendarSchedule, which validates an expression when a job is
// authored and derives backup_expectations.expected_interval from it. This one
// decides when a managed job actually fires. If they disagree, the fleet backs
// up on one schedule and is judged late against another — and the symptom is
// alerts nobody can explain rather than an error anyone can find.
//
// So they are not tested separately. docs/calendar-fixtures.json in
// NimbusControl is vendored byte-identical at testdata/calendar-fixtures.json
// here, every date in it worked out from a calendar rather than produced by
// running either parser, and BOTH suites read it. Dev rule 25: two suites
// written from the same head prove the head was consistent, not that the
// implementations are.
//
// The port is deliberately literal — same field expansion, same day-at-a-time
// walk, same boundary rules — because a cleverer Go version would be a second
// design to keep in step rather than a second copy.
//
// Timezone is an INPUT to evaluation, never a property of a stored instant.
// Every time.Time returned carries its location and is comparable as an
// instant; nothing here stores a wall clock as if it were one.

const (
	// maxScanDays bounds every walk. An expression matching nothing
	// reachable (`*-02-30 00:00`, or a fixed past year) must terminate.
	maxScanDays = 400
	// deriveWindowDays is the minimum forward window for interval derivation.
	deriveWindowDays = 14
	// deriveMinOccurrences: two occurrences give one gap, which for
	// `mon..fri 02:00` might be the 24h Tuesday gap rather than the 72h
	// weekend one depending on where `from` lands.
	deriveMinOccurrences = 3
)

var weekdayNames = map[string]int{
	"mon": 1, "monday": 1,
	"tue": 2, "tues": 2, "tuesday": 2,
	"wed": 3, "weds": 3, "wednesday": 3,
	"thu": 4, "thur": 4, "thurs": 4, "thursday": 4,
	"fri": 5, "friday": 5,
	"sat": 6, "saturday": 6,
	"sun": 7, "sunday": 7,
}

var calendarAliases = map[string]string{
	"minutely":     "*-*-* *:*:00",
	"hourly":       "*-*-* *:00:00",
	"daily":        "*-*-* 00:00:00",
	"weekly":       "mon *-*-* 00:00:00",
	"monthly":      "*-*-01 00:00:00",
	"yearly":       "*-01-01 00:00:00",
	"annually":     "*-01-01 00:00:00",
	"quarterly":    "*-01,04,07,10-01 00:00:00",
	"semiannually": "*-01,07-01 00:00:00",
}

// CalendarSchedule is a parsed expression.
type CalendarSchedule struct {
	expression string

	weekdays []int // ISO 1-7; empty means any
	years    []int // nil means any
	months   []int
	days     []int
	hours    []int
	minutes  []int
	seconds  []int
}

// ParseCalendar parses an expression, or returns an error naming the problem.
func ParseCalendar(expression string) (*CalendarSchedule, error) {
	raw := strings.TrimSpace(expression)
	expr := strings.ToLower(raw)
	if expr == "" {
		return nil, errors.New("schedule expression is empty")
	}
	if len(expr) > 200 {
		return nil, errors.New("schedule expression is too long")
	}
	if a, ok := calendarAliases[expr]; ok {
		expr = a
	}

	s := &CalendarSchedule{expression: raw}

	tokens := strings.Fields(expr)
	if len(tokens) > 3 {
		return nil, errors.New("too many parts in schedule expression")
	}

	// The weekday part is the only one that can contain letters, so it is
	// identified by content rather than position. `*` alone is a date.
	var weekdayTok, dateTok, timeTok string
	var haveWeekday, haveDate, haveTime bool
	for _, tok := range tokens {
		switch {
		case strings.ContainsAny(tok, "abcdefghijklmnopqrstuvwxyz"):
			if haveWeekday {
				return nil, errors.New("more than one weekday part")
			}
			weekdayTok, haveWeekday = tok, true
		case strings.Contains(tok, ":"):
			if haveTime {
				return nil, errors.New("more than one time part")
			}
			timeTok, haveTime = tok, true
		default:
			if haveDate {
				return nil, errors.New("more than one date part")
			}
			dateTok, haveDate = tok, true
		}
	}
	if !haveWeekday && !haveDate && !haveTime {
		return nil, errors.New("schedule expression has no recognisable part")
	}

	if haveWeekday {
		wd, err := parseWeekdays(weekdayTok)
		if err != nil {
			return nil, err
		}
		s.weekdays = wd
	}

	s.years = nil
	s.months = rangeInts(1, 12)
	s.days = rangeInts(1, 31)
	if haveDate {
		parts := strings.Split(dateTok, "-")
		switch len(parts) {
		case 3:
			if parts[0] != "*" {
				y, err := expandField(parts[0], 1970, 2200, "year")
				if err != nil {
					return nil, err
				}
				s.years = y
			}
			m, err := expandField(parts[1], 1, 12, "month")
			if err != nil {
				return nil, err
			}
			d, err := expandField(parts[2], 1, 31, "day")
			if err != nil {
				return nil, err
			}
			s.months, s.days = m, d
		case 2:
			m, err := expandField(parts[0], 1, 12, "month")
			if err != nil {
				return nil, err
			}
			d, err := expandField(parts[1], 1, 31, "day")
			if err != nil {
				return nil, err
			}
			s.months, s.days = m, d
		default:
			return nil, errors.New("date part must be [year-]month-day")
		}
	}

	// No time part means midnight, matching systemd.
	s.hours, s.minutes, s.seconds = []int{0}, []int{0}, []int{0}
	if haveTime {
		parts := strings.Split(timeTok, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return nil, errors.New("time part must be hour:minute[:second]")
		}
		h, err := expandField(parts[0], 0, 23, "hour")
		if err != nil {
			return nil, err
		}
		mi, err := expandField(parts[1], 0, 59, "minute")
		if err != nil {
			return nil, err
		}
		s.hours, s.minutes = h, mi
		if len(parts) == 3 {
			sec, err := expandField(parts[2], 0, 59, "second")
			if err != nil {
				return nil, err
			}
			s.seconds = sec
		}
	}

	return s, nil
}

// CalendarIsValid reports whether an expression parses. For cheap validation.
func CalendarIsValid(expression string) bool {
	_, err := ParseCalendar(expression)
	return err == nil
}

// Expression returns the expression as given.
func (s *CalendarSchedule) Expression() string { return s.expression }

// NextOccurrences returns the next n occurrences strictly after from, in loc.
//
// Strictly after, not at-or-after: "next run" asked at exactly 02:00:00 of a
// 02:00 schedule means tomorrow, not this instant. That matters here more than
// on the server — a scheduler tick landing exactly on the boundary would
// otherwise re-fire the job it just ran.
func (s *CalendarSchedule) NextOccurrences(loc *time.Location, from time.Time, n int) []time.Time {
	if n < 1 {
		return nil
	}
	out := make([]time.Time, 0, n)
	s.walk(loc, from, maxScanDays, func(t time.Time) bool {
		out = append(out, t)
		return len(out) < n
	})
	return out
}

// NextOccurrence returns the single next occurrence, or zero if none is
// reachable within the scan ceiling.
func (s *CalendarSchedule) NextOccurrence(loc *time.Location, from time.Time) time.Time {
	got := s.NextOccurrences(loc, from, 1)
	if len(got) == 0 {
		return time.Time{}
	}
	return got[0]
}

// LargestGapSeconds is the largest gap between consecutive occurrences.
//
// The agent does not currently derive its own expectations — the server does,
// from the same expression — but this exists and is fixture-pinned because it
// is the single most load-bearing number in the grammar, and an agent that
// computed it differently from the server would be the exact divergence this
// file's header is about. Testing it here is how that divergence gets caught
// before it matters.
//
// Returns ok=false when fewer than two occurrences are reachable: an
// expression that can never fire again has no interval, and inventing one
// would mark the job overdue forever a fixed distance from now.
func (s *CalendarSchedule) LargestGapSeconds(loc *time.Location, from time.Time) (int64, bool) {
	windowEnd := from.Add(deriveWindowDays * 24 * time.Hour)

	var prev time.Time
	var largest int64
	havePrev, haveGap := false, false
	seen := 0

	s.walk(loc, from, maxScanDays, func(t time.Time) bool {
		seen++
		if havePrev {
			gap := t.Unix() - prev.Unix()
			if !haveGap || gap > largest {
				largest, haveGap = gap, true
			}
		}
		prev, havePrev = t, true
		return !(seen >= deriveMinOccurrences && !t.Before(windowEnd))
	})

	return largest, haveGap
}

// walk yields occurrences ascending, strictly after from, until fn returns
// false or the ceiling is reached.
//
// Day-at-a-time rather than minute-at-a-time: a 400-day ceiling is 576,000
// minutes, so testing every minute against every field would be the wrong
// shape by three orders of magnitude. Days are filtered first; only matching
// ones expand their (already small) hour/minute/second sets.
func (s *CalendarSchedule) walk(loc *time.Location, from time.Time, maxDays int, fn func(time.Time) bool) {
	local := from.In(loc)
	cursor := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)

	for day := 0; day <= maxDays; day++ {
		y, m, d := cursor.Date()
		w := int(cursor.Weekday())
		if w == 0 {
			w = 7 // Go counts Sunday as 0; the grammar counts it as 7
		}

		if (s.years == nil || containsInt(s.years, y)) &&
			containsInt(s.months, int(m)) &&
			containsInt(s.days, d) &&
			(len(s.weekdays) == 0 || containsInt(s.weekdays, w)) {

			for _, h := range s.hours {
				for _, mi := range s.minutes {
					for _, sec := range s.seconds {
						occ := normalizeForward(y, m, d, h, mi, sec, loc)
						if occ.After(from) {
							if !fn(occ) {
								return
							}
						}
					}
				}
			}
		}

		// AddDate rather than Add(24h): across a DST transition a day is
		// not 24 hours, and adding a fixed duration would drift the cursor
		// off midnight and eventually skip or repeat a calendar day.
		cursor = cursor.AddDate(0, 0, 1)
	}
}

// normalizeForward builds a local instant, rolling a DST-nonexistent wall
// time FORWARD.
//
// THE SHARED FIXTURES CAUGHT THIS ON THE FIRST RUN, and it is the exact class
// of divergence they exist for. Go's time.Date normalizes a skipped wall time
// BACKWARD — asking for 02:30 on 2026-03-08 in America/New_York yields 01:30
// EST. PHP's DateTimeImmutable rolls it FORWARD, to 03:30 EDT. Neither is
// wrong in isolation; two implementations disagreeing by an hour twice a year
// is very wrong indeed, and no independently written Go test suite would have
// thought to ask.
//
// FORWARD is the direction that was chosen (see the PHP class header): a
// daily schedule keeps its 24h derived gap across the transition. Rolling
// backward would put two occurrences within the same hour on the autumn
// transition and could make a daily job appear to run twice.
//
// The correction is exact rather than a guess at the offset: whatever wall
// clock Go landed on, adding the difference between that and the one asked
// for produces the instant a forward roll means.
func normalizeForward(y int, m time.Month, d, h, mi, sec int, loc *time.Location) time.Time {
	occ := time.Date(y, m, d, h, mi, sec, 0, loc)
	if occ.Day() != d {
		// A wall time that fell outside the day entirely is not something
		// the DST correction can reason about; take what Go gave.
		return occ
	}
	want := h*3600 + mi*60 + sec
	got := occ.Hour()*3600 + occ.Minute()*60 + occ.Second()
	if got == want {
		return occ
	}
	return occ.Add(time.Duration(want-got) * time.Second)
}

func parseWeekdays(spec string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(spec, ",") {
		if part == "" {
			return nil, errors.New("empty weekday in list")
		}
		if strings.Contains(part, "..") {
			ab := strings.SplitN(part, "..", 2)
			lo, err := weekdayNumber(ab[0])
			if err != nil {
				return nil, err
			}
			hi, err := weekdayNumber(ab[1])
			if err != nil {
				return nil, err
			}
			// sat..sun is 6..7; sun..tue WRAPS, and systemd allows it.
			if lo <= hi {
				out = append(out, rangeInts(lo, hi)...)
			} else {
				out = append(out, rangeInts(lo, 7)...)
				out = append(out, rangeInts(1, hi)...)
			}
			continue
		}
		n, err := weekdayNumber(part)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return uniqueSorted(out), nil
}

func weekdayNumber(name string) (int, error) {
	n, ok := weekdayNames[name]
	if !ok {
		return 0, fmt.Errorf("unknown weekday %q", name)
	}
	return n, nil
}

// expandField expands one numeric field to its sorted, unique value set.
func expandField(spec string, min, max int, label string) ([]int, error) {
	if spec == "" {
		return nil, fmt.Errorf("empty %s field", label)
	}
	var out []int
	for _, part := range strings.Split(spec, ",") {
		if part == "" {
			return nil, fmt.Errorf("empty %s in list", label)
		}

		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			stepStr := part[i+1:]
			part = part[:i]
			v, err := strconv.Atoi(stepStr)
			if err != nil || stepStr == "" {
				return nil, fmt.Errorf("%s step must be a number", label)
			}
			if v < 1 {
				return nil, fmt.Errorf("%s step must be at least 1", label)
			}
			step = v
		}

		var lo, hi int
		switch {
		case part == "*":
			lo, hi = min, max
		case strings.Contains(part, ".."):
			ab := strings.SplitN(part, "..", 2)
			a, err := fieldNum(ab[0], min, max, label)
			if err != nil {
				return nil, err
			}
			b, err := fieldNum(ab[1], min, max, label)
			if err != nil {
				return nil, err
			}
			if a > b {
				return nil, fmt.Errorf("%s range is inverted", label)
			}
			lo, hi = a, b
		default:
			a, err := fieldNum(part, min, max, label)
			if err != nil {
				return nil, err
			}
			// `2/6` means "from 2, every 6, to the end of the field";
			// a bare `2` with no step is just 2.
			lo = a
			if step > 1 {
				hi = max
			} else {
				hi = a
			}
		}

		for v := lo; v <= hi; v += step {
			out = append(out, v)
		}
	}
	out = uniqueSorted(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("%s field matches nothing", label)
	}
	return out, nil
}

func fieldNum(s string, min, max int, label string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("%s must be a number, got %q", label, s)
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%s must be a number, got %q", label, s)
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number, got %q", label, s)
	}
	if v < min || v > max {
		return 0, fmt.Errorf("%s %d is out of range %d-%d", label, v, min, max)
	}
	return v, nil
}

func rangeInts(lo, hi int) []int {
	out := make([]int, 0, hi-lo+1)
	for v := lo; v <= hi; v++ {
		out = append(out, v)
	}
	return out
}

func uniqueSorted(in []int) []int {
	if len(in) == 0 {
		return in
	}
	sort.Ints(in)
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

func containsInt(hay []int, v int) bool {
	for _, h := range hay {
		if h == v {
			return true
		}
	}
	return false
}
