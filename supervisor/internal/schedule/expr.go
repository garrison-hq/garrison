package schedule

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ParseError is the typed error returned by Parse for any input
// outside the bounded named-cadence vocabulary (FR-103). Callers map
// it to validation_failed; it never escapes as a generic error.
type ParseError struct {
	Input string
	Msg   string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("invalid schedule expression %q: %s", e.Input, e.Msg)
}

// exprKind enumerates the three grammar forms.
type exprKind int

const (
	kindDaily exprKind = iota
	kindWeekly
	kindEvery
)

// Expr is a parsed schedule expression. All semantics are UTC-only
// (FR-103): no timezone or DST ambiguity exists by construction.
type Expr struct {
	kind    exprKind
	hh, mm  int
	weekday time.Weekday
	every   time.Duration
}

// weekdays maps the grammar's three-letter day tokens to time.Weekday.
var weekdays = map[string]time.Weekday{
	"mon": time.Monday,
	"tue": time.Tuesday,
	"wed": time.Wednesday,
	"thu": time.Thursday,
	"fri": time.Friday,
	"sat": time.Saturday,
	"sun": time.Sunday,
}

// Parse accepts exactly three forms, all UTC (FR-103):
//
//	daily@HH:MM
//	weekly@{mon|tue|wed|thu|fri|sat|sun}@HH:MM
//	every@<N>{m|h}        (N >= 1)
//
// Anything else — including full cron syntax — returns a typed
// *ParseError. No schedule-parsing dependency is used (stdlib only,
// per the AGENTS.md locked-deps soft rule).
func Parse(s string) (Expr, error) {
	parts := strings.Split(s, "@")
	switch {
	case len(parts) == 2 && parts[0] == "daily":
		hh, mm, err := parseHHMM(s, parts[1])
		if err != nil {
			return Expr{}, err
		}
		return Expr{kind: kindDaily, hh: hh, mm: mm}, nil

	case len(parts) == 3 && parts[0] == "weekly":
		wd, ok := weekdays[parts[1]]
		if !ok {
			return Expr{}, &ParseError{Input: s, Msg: fmt.Sprintf("unknown weekday %q (want mon..sun)", parts[1])}
		}
		hh, mm, err := parseHHMM(s, parts[2])
		if err != nil {
			return Expr{}, err
		}
		return Expr{kind: kindWeekly, weekday: wd, hh: hh, mm: mm}, nil

	case len(parts) == 2 && parts[0] == "every":
		every, err := parseEvery(s, parts[1])
		if err != nil {
			return Expr{}, err
		}
		return Expr{kind: kindEvery, every: every}, nil

	default:
		return Expr{}, &ParseError{Input: s, Msg: "want daily@HH:MM, weekly@{mon..sun}@HH:MM, or every@<N>{m|h}"}
	}
}

// parseHHMM parses a strict two-digit HH:MM (00:00 .. 23:59).
func parseHHMM(input, s string) (hh, mm int, err error) {
	if len(s) != 5 || s[2] != ':' || !allDigits(s[:2]) || !allDigits(s[3:]) {
		return 0, 0, &ParseError{Input: input, Msg: fmt.Sprintf("malformed time %q (want two-digit HH:MM)", s)}
	}
	hh, _ = strconv.Atoi(s[:2])
	mm, _ = strconv.Atoi(s[3:])
	if hh > 23 {
		return 0, 0, &ParseError{Input: input, Msg: fmt.Sprintf("hour %02d out of range (want 00..23)", hh)}
	}
	if mm > 59 {
		return 0, 0, &ParseError{Input: input, Msg: fmt.Sprintf("minute %02d out of range (want 00..59)", mm)}
	}
	return hh, mm, nil
}

// parseEvery parses the <N>{m|h} suffix of an every@ expression.
func parseEvery(input, s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, &ParseError{Input: input, Msg: fmt.Sprintf("malformed interval %q (want <N>m or <N>h)", s)}
	}
	var unit time.Duration
	switch s[len(s)-1] {
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	default:
		return 0, &ParseError{Input: input, Msg: fmt.Sprintf("unknown interval unit %q (want m or h)", s[len(s)-1:])}
	}
	digits := s[:len(s)-1]
	if !allDigits(digits) {
		return 0, &ParseError{Input: input, Msg: fmt.Sprintf("malformed interval count %q (want digits)", digits)}
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n > math.MaxInt64/int(unit) {
		return 0, &ParseError{Input: input, Msg: fmt.Sprintf("interval count %q out of range", digits)}
	}
	if n < 1 {
		return 0, &ParseError{Input: input, Msg: "interval count must be >= 1"}
	}
	return time.Duration(n) * unit, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Next returns the next slot strictly after `after`, in UTC. Pure
// date arithmetic — recovery collapse (FR-104) falls out for free:
// Next(now) after five missed days returns the single next future
// slot, never a past one.
func (e Expr) Next(after time.Time) time.Time {
	after = after.UTC()
	switch e.kind {
	case kindDaily:
		c := time.Date(after.Year(), after.Month(), after.Day(), e.hh, e.mm, 0, 0, time.UTC)
		if !c.After(after) {
			c = c.AddDate(0, 0, 1)
		}
		return c
	case kindWeekly:
		c := time.Date(after.Year(), after.Month(), after.Day(), e.hh, e.mm, 0, 0, time.UTC)
		for !c.After(after) || c.Weekday() != e.weekday {
			c = c.AddDate(0, 0, 1)
		}
		return c
	default: // kindEvery
		return after.Add(e.every)
	}
}

// MinInterval returns the effective firing interval per kind:
// daily=24h, weekly=168h, every@N=N. Compared against the
// operator-tunable minimum (GARRISON_SCHED_MIN_INTERVAL) in
// validation (FR-105/FR-404).
func (e Expr) MinInterval() time.Duration {
	switch e.kind {
	case kindDaily:
		return 24 * time.Hour
	case kindWeekly:
		return 7 * 24 * time.Hour
	default: // kindEvery
		return e.every
	}
}
