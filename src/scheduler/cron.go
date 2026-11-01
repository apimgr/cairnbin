// Package scheduler provides a built-in cron-compatible task scheduler with
// persistent state. No external cron libraries are used (PART 18).
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron/interval expression. Call Next to compute the
// next fire time after a given instant.
type Schedule interface {
	// Next returns the next activation time after t. Returns zero if the
	// schedule is effectively disabled.
	Next(t time.Time) time.Time

	// String returns the original expression.
	String() string
}

// ParseSchedule parses a schedule expression. Supported forms:
//
//	"@hourly"         – every hour at :00
//	"@daily"          – every day at 00:00
//	"@weekly"         – every Sunday at 00:00
//	"@monthly"        – first of month at 00:00
//	"@every Xm"       – every X minutes
//	"@every Xh"       – every X hours
//	"@every Xd"       – every X days
//	"m h d M w"       – 5-field cron (minute hour day month weekday)
func ParseSchedule(expr string) (Schedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("empty schedule expression")
	}

	switch expr {
	case "@hourly":
		return &cronExpr{raw: expr, minute: single(0), hour: star(), dom: star(), month: star(), dow: star()}, nil
	case "@daily", "@midnight":
		return &cronExpr{raw: expr, minute: single(0), hour: single(0), dom: star(), month: star(), dow: star()}, nil
	case "@weekly":
		return &cronExpr{raw: expr, minute: single(0), hour: single(0), dom: star(), month: star(), dow: single(0)}, nil
	case "@monthly":
		return &cronExpr{raw: expr, minute: single(0), hour: single(0), dom: single(1), month: star(), dow: star()}, nil
	case "@yearly", "@annually":
		return &cronExpr{raw: expr, minute: single(0), hour: single(0), dom: single(1), month: single(1), dow: star()}, nil
	}

	if strings.HasPrefix(expr, "@every ") {
		d, err := parseDuration(strings.TrimPrefix(expr, "@every "))
		if err != nil {
			return nil, fmt.Errorf("schedule %q: %w", expr, err)
		}
		return &intervalSched{raw: expr, d: d}, nil
	}

	return parseCron(expr)
}

// parseDuration parses duration strings like "5m", "2h", "1d".
func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}

// ── Interval schedule ────────────────────────────────────────────────────────

type intervalSched struct {
	raw string
	d   time.Duration
}

func (s *intervalSched) Next(t time.Time) time.Time { return t.Add(s.d) }
func (s *intervalSched) String() string             { return s.raw }

// ── Cron expression ──────────────────────────────────────────────────────────

// fieldSet is a bit-set representation of allowed values for a cron field.
// Minute: 0-59, Hour: 0-23, DOM: 1-31, Month: 1-12, DOW: 0-6 (0=Sunday).
type fieldSet struct {
	bits [64]bool
	// true = every value allowed
	star bool
}

func star() fieldSet { return fieldSet{star: true} }
func single(v int) fieldSet {
	var f fieldSet
	f.bits[v] = true
	return f
}

func (f *fieldSet) contains(v int) bool {
	if f.star {
		return true
	}
	if v < 0 || v >= len(f.bits) {
		return false
	}
	return f.bits[v]
}

// cronExpr holds the parsed five-field cron schedule.
type cronExpr struct {
	raw                           string
	minute, hour, dom, month, dow fieldSet
}

func (c *cronExpr) String() string { return c.raw }

// Next computes the first time at or after t+1s that matches the expression.
// We advance by one minute at a time — safe for a 5-field cron parser.
func (c *cronExpr) Next(t time.Time) time.Time {
	// Truncate to the next whole minute.
	next := t.Truncate(time.Minute).Add(time.Minute)

	// Safety: don't loop more than ~4 years worth of minutes.
	limit := next.Add(4 * 365 * 24 * time.Hour)
	for next.Before(limit) {
		if !c.month.contains(int(next.Month())) {
			// Advance to the first day of the next matching month.
			next = advanceToMonth(next, &c.month)
			continue
		}
		if !c.dayMatches(next) {
			// Advance to local midnight of the next day.
			next = time.Date(next.Year(), next.Month(), next.Day()+1, 0, 0, 0, 0, next.Location())
			continue
		}
		if !c.hour.contains(next.Hour()) {
			next = advanceToHour(next, &c.hour)
			continue
		}
		if !c.minute.contains(next.Minute()) {
			next = advanceToMinute(next, &c.minute)
			continue
		}
		return next
	}
	// should never happen for valid expressions
	return time.Time{}
}

// dayMatches applies standard (vixie) cron day semantics: when both the
// day-of-month and day-of-week fields are restricted (non-star), the day
// matches if EITHER field matches; when only one is restricted, that field
// alone decides; when both are stars, every day matches.
func (c *cronExpr) dayMatches(t time.Time) bool {
	domOK := c.dom.contains(t.Day())
	dowOK := c.dow.contains(int(t.Weekday()))
	switch {
	case c.dom.star && c.dow.star:
		return true
	case c.dom.star:
		return dowOK
	case c.dow.star:
		return domOK
	default:
		return domOK || dowOK
	}
}

func advanceToMonth(t time.Time, f *fieldSet) time.Time {
	m := int(t.Month()) + 1
	year := t.Year()
	for i := 0; i < 12; i++ {
		if m > 12 {
			m = 1
			year++
		}
		if f.contains(m) {
			return time.Date(year, time.Month(m), 1, 0, 0, 0, 0, t.Location())
		}
		m++
	}
	return t.Add(365 * 24 * time.Hour)
}

// startOfHour returns the beginning of t's wall-clock hour using only
// duration arithmetic, so it stays on t's own side of a DST transition.
func startOfHour(t time.Time) time.Time {
	return t.Add(-(time.Duration(t.Minute())*time.Minute +
		time.Duration(t.Second())*time.Second +
		time.Duration(t.Nanosecond())))
}

// advanceToHour returns the first later time whose hour is in f, or local
// midnight of the next day when none remains today. Candidates are built by
// adding whole hours to t (never by rebuilding a wall-clock date), so the
// result is always strictly after t, including on DST fall-back days.
func advanceToHour(t time.Time, f *fieldSet) time.Time {
	base := startOfHour(t)
	for i := 1; i <= 25; i++ {
		cand := base.Add(time.Duration(i) * time.Hour)
		if cand.Day() != t.Day() {
			break
		}
		if f.contains(cand.Hour()) {
			return cand
		}
	}
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
}

// advanceToMinute returns the first later time whose minute is in f, or the
// start of the next hour when none remains in this hour. It advances by
// duration from t so the result is always strictly after t, including inside
// the repeated hour of a DST fall-back transition.
func advanceToMinute(t time.Time, f *fieldSet) time.Time {
	base := startOfHour(t)
	for m := t.Minute() + 1; m < 60; m++ {
		if f.contains(m) {
			return base.Add(time.Duration(m) * time.Minute)
		}
	}
	return base.Add(time.Hour)
}

// parseCron parses a 5-field cron expression.
func parseCron(expr string) (*cronExpr, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("schedule %q: expected 5 fields, got %d", expr, len(fields))
	}

	minute, err := parseField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("schedule %q: minute: %w", expr, err)
	}
	hour, err := parseField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("schedule %q: hour: %w", expr, err)
	}
	dom, err := parseField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("schedule %q: day-of-month: %w", expr, err)
	}
	month, err := parseField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("schedule %q: month: %w", expr, err)
	}
	dow, err := parseField(fields[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("schedule %q: day-of-week: %w", expr, err)
	}

	return &cronExpr{
		raw: expr, minute: minute, hour: hour, dom: dom, month: month, dow: dow,
	}, nil
}

// parseField parses a single cron field (e.g. "5", "*", "1-5", "*/2", "1,3,5").
func parseField(s string, lo, hi int) (fieldSet, error) {
	if s == "*" {
		return star(), nil
	}

	var f fieldSet

	for _, part := range strings.Split(s, ",") {
		if strings.HasPrefix(part, "*/") {
			step, err := strconv.Atoi(part[2:])
			if err != nil || step <= 0 {
				return f, fmt.Errorf("invalid step in %q", s)
			}
			for v := lo; v <= hi; v += step {
				f.bits[v] = true
			}
			continue
		}

		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			start, err1 := strconv.Atoi(bounds[0])
			end, err2 := strconv.Atoi(bounds[1])
			if err1 != nil || err2 != nil || start < lo || end > hi || start > end {
				return f, fmt.Errorf("invalid range %q in field %q", part, s)
			}
			for v := start; v <= end; v++ {
				f.bits[v] = true
			}
			continue
		}

		n, err := strconv.Atoi(part)
		if err != nil || n < lo || n > hi {
			return f, fmt.Errorf("invalid value %q in field %q", part, s)
		}
		f.bits[n] = true
	}

	return f, nil
}
