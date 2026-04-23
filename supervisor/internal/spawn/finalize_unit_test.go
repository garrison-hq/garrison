package spawn

import (
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/finalize"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestWriteFinalizeSerializesDiaryWithObjectivePrepend — M2.2.1 T007:
// the drawer body starts with the ticket's objective text, followed
// by `---\n<yaml>---\n\n<rationale>` per FR-263 + spec Clarification
// 2026-04-23 Q6. Pure unit test — no DB or MemPalace needed.
func TestWriteFinalizeSerializesDiaryWithObjectivePrepend(t *testing.T) {
	objective := "Write hello.md describing the feature"
	tid := pgtype.UUID{Valid: true, Bytes: [16]byte{0x11, 0x11, 0x11, 0x11,
		0x22, 0x22, 0x43, 0x33, 0x84, 0x44, 0x55, 0x55, 0x66, 0x66, 0x77, 0x77}}
	payload := &finalize.FinalizePayload{
		TicketID: "11111111-2222-4333-8444-555566667777",
		Outcome:  "Implemented hello.md per spec",
		DiaryEntry: finalize.DiaryEntry{
			Rationale:   "The ticket asked for hello.md. I wrote changes/hello.md with the ticket id embedded so QA can verify. No blockers.",
			Artifacts:   []string{"changes/hello.md"},
			Blockers:    []string{},
			Discoveries: []string{},
		},
		KGTriples: []finalize.KGTriple{
			{Subject: "agent_instance_abc", Predicate: "completed", Object: "ticket_111", ValidFrom: time.Now().UTC()},
		},
	}
	completed := time.Date(2026, 4, 23, 12, 34, 56, 0, time.UTC)
	body := serializeDiary(objective, tid, payload, completed)

	// The body MUST open with the objective prose (load-bearing for
	// mempalace_search to land per Clarify Q6 + FR-263).
	if !strings.HasPrefix(body, objective) {
		t.Errorf("body does not start with objective prose:\n%s", body)
	}
	// After the objective comes `\n\n---\n` starting the YAML block.
	if !strings.Contains(body, objective+"\n\n---\n") {
		t.Errorf("body missing YAML delimiter after objective:\n%s", body)
	}
	// YAML block contains the ticket_id + outcome + completed_at.
	if !strings.Contains(body, "ticket_id: 11111111-2222-4333-8444-555566667777") {
		t.Errorf("body missing ticket_id in YAML:\n%s", body)
	}
	if !strings.Contains(body, "outcome: Implemented hello.md per spec") {
		t.Errorf("body missing outcome in YAML:\n%s", body)
	}
	if !strings.Contains(body, "completed_at: 2026-04-23T12:34:56Z") {
		t.Errorf("body missing completed_at in YAML:\n%s", body)
	}
	// Rationale appears after the closing `---\n\n`.
	if !strings.Contains(body, "---\n\nThe ticket asked for hello.md.") {
		t.Errorf("body missing rationale after closing delimiter:\n%s", body)
	}
	// Artifacts block has the one path.
	if !strings.Contains(body, "  - changes/hello.md") {
		t.Errorf("body missing artifact entry:\n%s", body)
	}
}

// TestSerializeDiaryEscapesYAMLSignificantChars — diary content with
// colons, quotes, or newlines still produces a valid YAML scalar. The
// escapeYAML helper falls back to JSON-string form for unsafe content.
func TestSerializeDiaryEscapesYAMLSignificantChars(t *testing.T) {
	tid := pgtype.UUID{Valid: true, Bytes: [16]byte{0xaa}}
	payload := &finalize.FinalizePayload{
		TicketID: "aa",
		Outcome:  "Outcome with: colons \"quotes\" and\nnewlines",
		DiaryEntry: finalize.DiaryEntry{
			Rationale: "rationale body",
			Artifacts: []string{"path:weird", "# hash prefix"},
		},
	}
	body := serializeDiary("obj", tid, payload, time.Unix(0, 0).UTC())
	// The problematic outcome must be quoted (either with \" or with
	// valid JSON-escape form); bare `Outcome with: colons ...` is NOT
	// a valid YAML scalar.
	if strings.Contains(body, "outcome: Outcome with: colons") {
		t.Errorf("escapeYAML failed to quote colons/quotes: body=%q", body)
	}
}
