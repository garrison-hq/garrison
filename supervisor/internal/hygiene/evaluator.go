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

	// M2.2.1 additions per FR-267. Written for rows whose exit_reason is
	// one of the finalize_* values (or the related completed / timeout
	// values). Legacy M2.2 values above remain valid on historical rows.
	StatusFinalizeFailed  Status = "finalize_failed"
	StatusFinalizePartial Status = "finalize_partial"
	StatusStuck           Status = "stuck"

	// ThinBodyThreshold is the per-FR-214 boundary: diary body < 100
	// chars is flagged as 'thin' regardless of KG-triple presence. The
	// rule applies only when a matching diary exists in the first place.
	ThinBodyThreshold = 100
)

// AgentInstanceFinalizeSignal carries the exit_reason-derived input
// EvaluateFinalizeOutcome needs. ExitReason is the canonical string
// from internal/spawn/exitreason.go (finalize_invalid,
// finalize_palace_write_failed, etc.); HasTransition is the
// EXISTS(ticket_transitions ...) result from the
// SelectAgentInstanceFinalizedState query added in T001. The
// listener/sweep populate these from the store.SelectAgentInstance
// FinalizedStateRow returned by that query.
type AgentInstanceFinalizeSignal struct {
	ExitReason    string
	HasTransition bool
}

// EvaluateFinalizeOutcome maps an agent_instances row's (exit_reason,
// has_transition) tuple to the M2.2.1 hygiene_status vocabulary per
// FR-269. Called by the listener/sweep for rows whose exit_reason
// starts with "finalize_" or equals one of the related reasons that
// signal "this spawn was in the finalize-expected flow."
//
// Rules:
//   - exit_reason="completed" AND has_transition → StatusClean
//   - exit_reason="finalize_invalid" → StatusFinalizeFailed
//   - exit_reason ∈ {finalize_palace_write_failed,
//     finalize_commit_failed, finalize_write_timeout} → StatusFinalizePartial
//   - !has_transition AND exit_reason ∈ {finalize_never_called, timeout} → StatusStuck
//   - otherwise → StatusPending (transient; sweep re-evaluates)
func EvaluateFinalizeOutcome(sig AgentInstanceFinalizeSignal) Status {
	switch sig.ExitReason {
	case "completed":
		if sig.HasTransition {
			return StatusClean
		}
		return StatusPending
	case "finalize_invalid":
		return StatusFinalizeFailed
	case "finalize_palace_write_failed",
		"finalize_commit_failed",
		"finalize_write_timeout":
		return StatusFinalizePartial
	case "finalize_never_called":
		if !sig.HasTransition {
			return StatusStuck
		}
		return StatusPending
	case "timeout":
		if !sig.HasTransition {
			return StatusStuck
		}
		return StatusPending
	default:
		return StatusPending
	}
}

// IsFinalizeExitReason returns true when the exit_reason belongs to
// the M2.2.1 finalize-shaped family. Listener/sweep use this to
// dispatch to EvaluateFinalizeOutcome rather than the M2.2 Evaluate
// path. `completed` and `timeout` are included because they can land
// on finalize-expected rows (completed via the happy path, timeout
// via a subprocess timeout before finalize).
func IsFinalizeExitReason(exitReason string) bool {
	switch exitReason {
	case "completed", "timeout",
		"finalize_invalid",
		"finalize_palace_write_failed",
		"finalize_commit_failed",
		"finalize_write_timeout",
		"finalize_never_called",
		"finalize_transition_conflict":
		return true
	}
	return false
}

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
