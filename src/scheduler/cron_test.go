package scheduler

import (
	"testing"
	"time"
)

// mustParse parses a schedule expression or fails the test.
func mustParse(t *testing.T, expr string) Schedule {
	t.Helper()
	s, err := ParseSchedule(expr)
	if err != nil {
		t.Fatalf("ParseSchedule(%q): %v", expr, err)
	}
	return s
}

// TestCronDayOfWeekOnly verifies that a restricted day-of-week with a star
// day-of-month only fires on the matching weekday (standard cron semantics).
func TestCronDayOfWeekOnly(t *testing.T) {
	// "0 2 * * 1" = 02:00 every Monday.
	s := mustParse(t, "0 2 * * 1")
	// Wednesday 2026-07-08 10:00 UTC → next Monday 2026-07-13 02:00 UTC.
	from := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	want := time.Date(2026, 7, 13, 2, 0, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", from, got, want)
	}
}

// TestCronDayOfMonthOnly verifies that a restricted day-of-month with a star
// day-of-week only fires on the matching day of the month.
func TestCronDayOfMonthOnly(t *testing.T) {
	// "30 4 15 * *" = 04:30 on the 15th of every month.
	s := mustParse(t, "30 4 15 * *")
	from := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	want := time.Date(2026, 8, 15, 4, 30, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", from, got, want)
	}
}

// TestCronDomDowBothRestrictedIsOr verifies vixie-cron OR semantics: when both
// day fields are restricted, the schedule fires when EITHER matches.
func TestCronDomDowBothRestrictedIsOr(t *testing.T) {
	// "0 0 13 * 5" = midnight on the 13th OR any Friday.
	s := mustParse(t, "0 0 13 * 5")
	// From Wed 2026-07-08: Friday 2026-07-10 comes before Monday the 13th.
	from := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	want := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	got := s.Next(from)
	if !got.Equal(want) {
		t.Errorf("Next(%v) = %v, want Friday %v", from, got, want)
	}
	// From Sat 2026-07-11: Monday the 13th comes before the next Friday.
	from = time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	want = time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Errorf("Next(%v) = %v, want 13th %v", from, got, want)
	}
}

// TestCronBothDayFieldsStar verifies that star day fields match every day.
func TestCronBothDayFieldsStar(t *testing.T) {
	// "0 2 * * *" = 02:00 every day.
	s := mustParse(t, "0 2 * * *")
	from := time.Date(2026, 7, 8, 3, 0, 0, 0, time.UTC)
	want := time.Date(2026, 7, 9, 2, 0, 0, 0, time.UTC)
	if got := s.Next(from); !got.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", from, got, want)
	}
}

// TestCronDayAdvanceKeepsLocation verifies day skipping lands on local
// midnight, not UTC midnight, in a non-UTC location.
func TestCronDayAdvanceKeepsLocation(t *testing.T) {
	loc := time.FixedZone("UTC+9", 9*3600)
	// "0 2 * * 1" = 02:00 every Monday, evaluated in UTC+9.
	s := mustParse(t, "0 2 * * 1")
	from := time.Date(2026, 7, 8, 10, 0, 0, 0, loc)
	want := time.Date(2026, 7, 13, 2, 0, 0, 0, loc)
	if got := s.Next(from); !got.Equal(want) {
		t.Errorf("Next(%v) = %v, want %v", from, got, want)
	}
}

// TestCronNextDSTFallBack guards against Next never terminating (or moving
// backwards) on the US fall-back day, when 01:00-02:00 occurs twice.
func TestCronNextDSTFallBack(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	cases := []struct {
		name string
		expr string
		from time.Time
		want time.Time
	}{
		{"daily after midnight", "@daily", time.Date(2026, 11, 1, 8, 0, 0, 0, loc), time.Date(2026, 11, 2, 0, 0, 0, 0, loc)},
		{"hour match across transition", "0 3 * * *", time.Date(2026, 11, 1, 0, 10, 0, 0, loc), time.Date(2026, 11, 1, 3, 0, 0, 0, loc)},
		{"minute inside repeated hour", "30 * * * *", time.Date(2026, 11, 1, 1, 45, 0, 0, loc).Add(time.Hour), time.Date(2026, 11, 1, 2, 30, 0, 0, loc)},
		{"hour miss late in day", "0 1 * * *", time.Date(2026, 11, 1, 23, 30, 0, 0, loc), time.Date(2026, 11, 2, 1, 0, 0, 0, loc)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mustParse(t, tc.expr).Next(tc.from)
			if !got.After(tc.from) {
				t.Fatalf("Next(%v) = %v, not after input", tc.from, got)
			}
			if !got.Equal(tc.want) {
				t.Errorf("Next(%v) = %v, want %v", tc.from, got, tc.want)
			}
		})
	}
}
