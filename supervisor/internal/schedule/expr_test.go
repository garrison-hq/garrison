package schedule

import (
	"errors"
	"testing"
	"time"
)

func TestParseAcceptsGrammar(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		minInterval time.Duration
	}{
		{"daily boundary low", "daily@00:00", 24 * time.Hour},
		{"daily boundary high", "daily@23:59", 24 * time.Hour},
		{"daily mid", "daily@09:30", 24 * time.Hour},
		{"weekly monday", "weekly@mon@08:00", 7 * 24 * time.Hour},
		{"weekly sunday boundary", "weekly@sun@23:59", 7 * 24 * time.Hour},
		{"weekly thursday", "weekly@thu@00:00", 7 * 24 * time.Hour},
		{"every minutes", "every@15m", 15 * time.Minute},
		{"every single minute", "every@1m", time.Minute},
		{"every hours", "every@2h", 2 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expr, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse(%q) returned error: %v", tc.input, err)
			}
			if got := expr.MinInterval(); got != tc.minInterval {
				t.Fatalf("Parse(%q).MinInterval() = %v, want %v", tc.input, got, tc.minInterval)
			}
		})
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"full cron", "*/5 * * * *"},
		{"cron five fields", "0 9 * * 1"},
		{"bad weekday", "weekly@funday@08:00"},
		{"missing at separator", "daily 08:00"},
		{"daily no time", "daily"},
		{"hour out of range", "daily@25:00"},
		{"minute out of range", "daily@10:60"},
		{"single digit hour", "daily@9:30"},
		{"weekly missing time", "weekly@mon"},
		{"weekly hour out of range", "weekly@mon@24:00"},
		{"every zero", "every@0m"},
		{"every no count", "every@m"},
		{"every bad unit", "every@15s"},
		{"every no unit", "every@15"},
		{"every negative", "every@-5m"},
		{"unknown keyword", "monthly@01@00:00"},
		{"trailing garbage", "daily@09:30@mon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.input)
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want *ParseError", tc.input)
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("Parse(%q) error type = %T, want *ParseError", tc.input, err)
			}
			if pe.Input != tc.input {
				t.Fatalf("ParseError.Input = %q, want %q", pe.Input, tc.input)
			}
		})
	}
}

func TestNextDailyComputesStrictlyFuture(t *testing.T) {
	expr, err := Parse("daily@09:00")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Before today's slot: same-day slot.
	after := time.Date(2026, 6, 11, 7, 30, 0, 0, time.UTC)
	want := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	if got := expr.Next(after); !got.Equal(want) {
		t.Fatalf("Next(%v) = %v, want %v", after, got, want)
	}

	// Exactly on the slot: strictly future means tomorrow.
	after = time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	want = time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)
	got := expr.Next(after)
	if !got.Equal(want) {
		t.Fatalf("Next(on-slot %v) = %v, want %v", after, got, want)
	}
	if !got.After(after) {
		t.Fatalf("Next(%v) = %v is not strictly future", after, got)
	}
	if got.Location() != time.UTC {
		t.Fatalf("Next returned location %v, want UTC", got.Location())
	}

	// After today's slot: tomorrow.
	after = time.Date(2026, 6, 11, 9, 0, 1, 0, time.UTC)
	want = time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)
	if got := expr.Next(after); !got.Equal(want) {
		t.Fatalf("Next(%v) = %v, want %v", after, got, want)
	}
}

func TestNextWeeklyWalksToWeekday(t *testing.T) {
	// 2026-06-08 is a Monday; 2026-06-12 is a Friday.
	expr, err := Parse("weekly@fri@10:00")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	cases := []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		{
			"mid-week walks forward",
			time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC), // Wed
			time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC), // this Fri
		},
		{
			"same day before time fires today",
			time.Date(2026, 6, 12, 8, 0, 0, 0, time.UTC),  // Fri 08:00
			time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC), // Fri 10:00
		},
		{
			"exactly on slot is strictly future",
			time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC), // Fri 10:00
			time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC), // next Fri
		},
		{
			"same day after time walks a week",
			time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC), // Fri 11:00
			time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC), // next Fri
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expr.Next(tc.after)
			if !got.Equal(tc.want) {
				t.Fatalf("Next(%v) = %v, want %v", tc.after, got, tc.want)
			}
			if got.Weekday() != time.Friday {
				t.Fatalf("Next(%v) landed on %v, want Friday", tc.after, got.Weekday())
			}
		})
	}
}

func TestNextCollapsesMissedSlots(t *testing.T) {
	// FR-104 arithmetic: five missed daily slots collapse to the
	// single next future slot — no backfill, no past slot.
	expr, err := Parse("daily@09:00")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	after := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC) // 5+ days past 2026-06-11
	want := time.Date(2026, 6, 17, 9, 0, 0, 0, time.UTC)
	got := expr.Next(after)
	if !got.Equal(want) {
		t.Fatalf("Next(%v) = %v, want single next future slot %v", after, got, want)
	}

	// Weekly: three missed Fridays collapse to the next Friday.
	weekly, err := Parse("weekly@fri@10:00")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	after = time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) // Sat, weeks past 2026-06-12
	want = time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	if got := weekly.Next(after); !got.Equal(want) {
		t.Fatalf("weekly Next(%v) = %v, want %v", after, got, want)
	}
}

func TestMinIntervalPerKind(t *testing.T) {
	cases := []struct {
		input string
		want  time.Duration
	}{
		{"daily@09:00", 24 * time.Hour},
		{"weekly@mon@09:00", 168 * time.Hour},
		{"every@45m", 45 * time.Minute},
		{"every@2h", 2 * time.Hour},
	}
	for _, tc := range cases {
		expr, err := Parse(tc.input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.input, err)
		}
		if got := expr.MinInterval(); got != tc.want {
			t.Fatalf("Parse(%q).MinInterval() = %v, want %v", tc.input, got, tc.want)
		}
	}
}
