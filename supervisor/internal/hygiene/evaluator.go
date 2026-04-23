// Package hygiene owns the post-transition palace-hygiene evaluator.
//
// The LISTEN goroutine (listener.go) and periodic sweep (sweep.go — both
// in this package, arriving in T008) observe ticket_transitions INSERTs,
// query MemPalace for the expected writes via the palace Client (palace.go),
// feed an EvaluationInput into Evaluate, and UPDATE
// ticket_transitions.hygiene_status with the returned terminal value.
//
// This file contains ONLY the pure rule logic so the evaluation is
// exhaustively unit-testable without docker, postgres, or MemPalace.
package hygiene

import (
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
)

// Status is the value written to ticket_transitions.hygiene_status per
// FR-214. Terminal values: Clean, MissingDiary, MissingKG, Thin. Non-
// terminal: Pending (palace query failed; sweep re-evaluates).
//
// M2.2.1 adds the FinalizeFailed / FinalizePartial / Stuck vocabulary
// (per FR-267); those live alongside and are set by EvaluateFinalizeOutcome
// (T008), not by Evaluate.
type Status string

const (
	StatusClean        Status = "clean"
	StatusMissingDiary Status = "missing_diary"
	StatusMissingKG    Status = "missing_kg"
	StatusThin         Status = "thin"
	StatusPending      Status = "pending"

	// ThinBodyThreshold is the per-FR-214 boundary: diary body < 100
	// chars is flagged as 'thin' regardless of KG-triple presence. The
	// rule applies only when a matching diary exists in the first place.
	ThinBodyThreshold = 100
)

// PalaceDrawer is a type alias for mempalace.Drawer (M2.2.1 T003).
// Preserved so evaluator_test.go literals like PalaceDrawer{...} keep
// compiling; new code should prefer mempalace.Drawer directly.
type PalaceDrawer = mempalace.Drawer

// PalaceTriple is a type alias for mempalace.Triple (M2.2.1 T003).
type PalaceTriple = mempalace.Triple

// unused import guard so goimports/gopls doesn't strip "time".
var _ = time.Time{}

// EvaluationInput is the pure-logic input to Evaluate. TicketIDText is
// the "ticket_<uuid>" form — the evaluator uses substring matching
// against drawer bodies and direction-agnostic matching against KG
// subject/object. PalaceErr != nil short-circuits to StatusPending.
type EvaluationInput struct {
	TicketIDText   string
	RunWindowStart time.Time
	RunWindowEnd   time.Time
	PalaceWing     string

	Drawers   []PalaceDrawer
	KGTriples []PalaceTriple

	PalaceErr error
}

// Evaluate applies the FR-214 rule set:
//
//  1. PalaceErr != nil → Pending (palace unreachable; sweep retries)
//  2. no matching diary drawer in PalaceWing with body mentioning
//     TicketIDText and CreatedAt in [RunWindowStart, RunWindowEnd] →
//     MissingDiary
//  3. matching diary found, body length < ThinBodyThreshold → Thin
//     (this precedence explicitly overrides MissingKG per FR-214
//     clause b — Thin flags regardless of KG state)
//  4. no matching KG triple whose subject or object equals TicketIDText
//     and ValidFrom in run window → MissingKG
//  5. otherwise → Clean
//
// Pure function: no I/O, no ambient state, no slog. Safe to call many
// times per row and inside test loops.
func Evaluate(in EvaluationInput) Status {
	if in.PalaceErr != nil {
		return StatusPending
	}

	var matchingDiary *PalaceDrawer
	for i := range in.Drawers {
		d := &in.Drawers[i]
		if d.Wing != in.PalaceWing {
			continue
		}
		if !withinWindow(d.CreatedAt, in.RunWindowStart, in.RunWindowEnd) {
			continue
		}
		if !containsSubstring(d.Body, in.TicketIDText) {
			continue
		}
		matchingDiary = d
		break
	}

	if matchingDiary == nil {
		return StatusMissingDiary
	}
	if len(matchingDiary.Body) < ThinBodyThreshold {
		return StatusThin
	}

	for i := range in.KGTriples {
		tr := &in.KGTriples[i]
		if !withinWindow(tr.ValidFrom, in.RunWindowStart, in.RunWindowEnd) {
			continue
		}
		if tr.Subject == in.TicketIDText || tr.Object == in.TicketIDText {
			return StatusClean
		}
	}
	return StatusMissingKG
}

// withinWindow is an inclusive [start, end] check. Zero-valued end is
// treated as open-ended (unlikely in practice since finished_at is set
// during the terminal write, but a safe default for test fixtures that
// leave it at zero).
func withinWindow(t, start, end time.Time) bool {
	if t.Before(start) {
		return false
	}
	if !end.IsZero() && t.After(end) {
		return false
	}
	return true
}

// containsSubstring wraps strings.Contains without pulling the strings
// import into evaluator.go's top. Keeps the file obviously-pure.
func containsSubstring(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	return indexOf(haystack, needle) >= 0
}

// indexOf is a small hand-rolled substring search (O(n*m)); inputs are
// short (diary bodies are a few hundred chars, ticket-id strings are
// ~50 chars), so the naive algorithm is fine and has no allocation.
func indexOf(haystack, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	if len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
