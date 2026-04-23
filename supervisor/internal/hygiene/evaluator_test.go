package hygiene

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Standard run window the per-test inputs share.
func baseWindow() (time.Time, time.Time) {
	start := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	return start, end
}

// Body of plenty length so it doesn't accidentally trigger Thin.
const longBody = "This ticket was worked on today. I implemented the feature and wrote " +
	"this diary entry for ticket_" + "abc-123" + " so future instances know what happened. " +
	"The approach was straightforward; no blockers."

func ticketIDText() string { return "ticket_abc-123" }

func TestEvaluatePalaceError(t *testing.T) {
	start, end := baseWindow()
	got := Evaluate(EvaluationInput{
		TicketIDText:   ticketIDText(),
		RunWindowStart: start,
		RunWindowEnd:   end,
		PalaceWing:     "wing_frontend_engineer",
		PalaceErr:      errors.New("docker exec: container not running"),
	})
	if got != StatusPending {
		t.Fatalf("got %s; want %s", got, StatusPending)
	}
}

func TestEvaluateMissingDiary(t *testing.T) {
	start, end := baseWindow()
	got := Evaluate(EvaluationInput{
		TicketIDText:   ticketIDText(),
		RunWindowStart: start,
		RunWindowEnd:   end,
		PalaceWing:     "wing_frontend_engineer",
		// No drawers at all.
	})
	if got != StatusMissingDiary {
		t.Fatalf("got %s; want %s", got, StatusMissingDiary)
	}
}

func TestEvaluateThinDiaryOverridesMissingKG(t *testing.T) {
	// A matching diary exists but is too short (< 100 chars). Thin wins
	// regardless of KG state per FR-214 clause b.
	start, end := baseWindow()
	mid := start.Add(1 * time.Minute)
	thin := "short " + ticketIDText() // < 100 chars, still mentions the ticket
	if len(thin) >= ThinBodyThreshold {
		t.Fatalf("fixture error: 'thin' body is %d chars (≥ threshold)", len(thin))
	}
	got := Evaluate(EvaluationInput{
		TicketIDText:   ticketIDText(),
		RunWindowStart: start,
		RunWindowEnd:   end,
		PalaceWing:     "wing_frontend_engineer",
		Drawers: []PalaceDrawer{
			{Wing: "wing_frontend_engineer", Body: thin, CreatedAt: mid},
		},
		// No KG triples — but Thin wins.
	})
	if got != StatusThin {
		t.Fatalf("got %s; want %s", got, StatusThin)
	}
}

func TestEvaluateMissingKG(t *testing.T) {
	start, end := baseWindow()
	mid := start.Add(1 * time.Minute)
	got := Evaluate(EvaluationInput{
		TicketIDText:   ticketIDText(),
		RunWindowStart: start,
		RunWindowEnd:   end,
		PalaceWing:     "wing_frontend_engineer",
		Drawers: []PalaceDrawer{
			{Wing: "wing_frontend_engineer", Body: longBody, CreatedAt: mid},
		},
		// KGTriples empty → diary present but no triple → MissingKG.
	})
	if got != StatusMissingKG {
		t.Fatalf("got %s; want %s", got, StatusMissingKG)
	}
}

func TestEvaluateClean(t *testing.T) {
	start, end := baseWindow()
	mid := start.Add(1 * time.Minute)
	got := Evaluate(EvaluationInput{
		TicketIDText:   ticketIDText(),
		RunWindowStart: start,
		RunWindowEnd:   end,
		PalaceWing:     "wing_frontend_engineer",
		Drawers: []PalaceDrawer{
			{Wing: "wing_frontend_engineer", Body: longBody, CreatedAt: mid},
		},
		KGTriples: []PalaceTriple{
			{Subject: "agent_instance_xyz", Predicate: "completed", Object: ticketIDText(), ValidFrom: mid},
		},
	})
	if got != StatusClean {
		t.Fatalf("got %s; want %s", got, StatusClean)
	}
}

func TestEvaluateDiaryOutsideWindow(t *testing.T) {
	// Drawer body matches the ticket but was created before the run
	// window → it's from a previous invocation, not this one. Missing.
	start, end := baseWindow()
	before := start.Add(-10 * time.Minute)
	got := Evaluate(EvaluationInput{
		TicketIDText:   ticketIDText(),
		RunWindowStart: start,
		RunWindowEnd:   end,
		PalaceWing:     "wing_frontend_engineer",
		Drawers: []PalaceDrawer{
			{Wing: "wing_frontend_engineer", Body: longBody, CreatedAt: before},
		},
	})
	if got != StatusMissingDiary {
		t.Fatalf("got %s; want %s", got, StatusMissingDiary)
	}
}

func TestEvaluateKGDirectionAgnostic(t *testing.T) {
	// The triple has ticketIDText in the OBJECT position (not subject).
	// Per clarification 2026-04-22 Q3, the evaluator must still match.
	start, end := baseWindow()
	mid := start.Add(1 * time.Minute)
	got := Evaluate(EvaluationInput{
		TicketIDText:   ticketIDText(),
		RunWindowStart: start,
		RunWindowEnd:   end,
		PalaceWing:     "wing_frontend_engineer",
		Drawers: []PalaceDrawer{
			{Wing: "wing_frontend_engineer", Body: longBody, CreatedAt: mid},
		},
		KGTriples: []PalaceTriple{
			// Subject is NOT the ticket; object IS the ticket.
			{Subject: "changes/hello.md", Predicate: "created_in", Object: ticketIDText(), ValidFrom: mid},
		},
	})
	if got != StatusClean {
		t.Fatalf("got %s; want %s (direction-agnostic match failed)", got, StatusClean)
	}
}

// Sanity check on longBody fixture — it must mention the ticket id.
func TestLongBodyMentionsTicket(t *testing.T) {
	if !strings.Contains(longBody, "abc-123") {
		t.Fatalf("fixture error: longBody does not mention ticket id")
	}
	if len(longBody) < ThinBodyThreshold {
		t.Fatalf("fixture error: longBody is thin (%d chars)", len(longBody))
	}
}

// -------- M2.2.1 EvaluateFinalizeOutcome ---------------------------------

func TestEvaluateFinalizeOutcomeClean(t *testing.T) {
	s := EvaluateFinalizeOutcome(AgentInstanceFinalizeSignal{
		ExitReason: "completed", HasTransition: true,
	})
	if s != StatusClean {
		t.Errorf("got %q; want clean", s)
	}
}

func TestEvaluateFinalizeOutcomeFinalizeFailed(t *testing.T) {
	s := EvaluateFinalizeOutcome(AgentInstanceFinalizeSignal{
		ExitReason: "finalize_invalid", HasTransition: false,
	})
	if s != StatusFinalizeFailed {
		t.Errorf("got %q; want finalize_failed", s)
	}
}

// TestEvaluateFinalizeOutcomeFinalizePartial covers all three
// triggering exit_reasons as sub-cases.
func TestEvaluateFinalizeOutcomeFinalizePartial(t *testing.T) {
	cases := []string{
		"finalize_palace_write_failed",
		"finalize_commit_failed",
		"finalize_write_timeout",
	}
	for _, er := range cases {
		t.Run(er, func(t *testing.T) {
			s := EvaluateFinalizeOutcome(AgentInstanceFinalizeSignal{
				ExitReason: er, HasTransition: false,
			})
			if s != StatusFinalizePartial {
				t.Errorf("exit_reason=%q → %q; want finalize_partial", er, s)
			}
		})
	}
}

// TestEvaluateFinalizeOutcomeStuck covers both trigger conditions
// (finalize_never_called and timeout) with has_transition=false.
func TestEvaluateFinalizeOutcomeStuck(t *testing.T) {
	cases := []string{"finalize_never_called", "timeout"}
	for _, er := range cases {
		t.Run(er, func(t *testing.T) {
			s := EvaluateFinalizeOutcome(AgentInstanceFinalizeSignal{
				ExitReason: er, HasTransition: false,
			})
			if s != StatusStuck {
				t.Errorf("exit_reason=%q no-transition → %q; want stuck", er, s)
			}
		})
	}
}

// TestEvaluateFinalizeOutcomeLegacyPassthrough — the IsFinalizeExitReason
// routing helper distinguishes finalize-shaped rows (→ EvaluateFinalize
// Outcome) from legacy M2.2 rows (→ Evaluate). This test pins the
// routing contract so the listener dispatches correctly.
func TestEvaluateFinalizeOutcomeLegacyPassthrough(t *testing.T) {
	if IsFinalizeExitReason("claude_error") {
		t.Error("IsFinalizeExitReason(claude_error) = true; want false — non-finalize reasons must fall through to Evaluate")
	}
	if IsFinalizeExitReason("acceptance_failed") {
		t.Error("IsFinalizeExitReason(acceptance_failed) = true; want false")
	}
	if !IsFinalizeExitReason("finalize_invalid") {
		t.Error("IsFinalizeExitReason(finalize_invalid) = false; want true")
	}
	if !IsFinalizeExitReason("completed") {
		t.Error("IsFinalizeExitReason(completed) = false; want true — happy path needs finalize routing")
	}
}
