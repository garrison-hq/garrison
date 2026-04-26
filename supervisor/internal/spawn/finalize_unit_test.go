package spawn

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/finalize"
	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/jackc/pgx/v5/pgtype"
)

// testSKToken is a realistic sk-prefix token: sk- followed by ≥20 alphanumeric
// chars so it clears the minimum-length gate in vault.ScanAndRedact.
const testSKToken = "sk-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghij"

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

// --- M2.3 T009 scanner tests ---

// TestFinalizeHandlerRedactsSecretPatternsInDiary — an sk-prefix token in the
// rationale is replaced with [REDACTED:sk_prefix] and hygieneStatus becomes
// "suspected_secret_emitted" (FR-418 / FR-419).
func TestFinalizeHandlerRedactsSecretPatternsInDiary(t *testing.T) {
	payload := &finalize.FinalizePayload{
		DiaryEntry: finalize.DiaryEntry{
			Rationale: "Used token " + testSKToken + " to call the API.",
		},
		KGTriples: []finalize.KGTriple{
			{Subject: "agent", Predicate: "completed", Object: "ticket", ValidFrom: time.Now().UTC()},
		},
	}

	labels := scanAndRedactPayload(payload)
	if len(labels) == 0 {
		t.Fatal("expected scanner to find a match; got zero labels")
	}
	if strings.Contains(payload.DiaryEntry.Rationale, testSKToken) {
		t.Errorf("raw token still present after redaction: %s", payload.DiaryEntry.Rationale)
	}
	if !strings.Contains(payload.DiaryEntry.Rationale, "[REDACTED:") {
		t.Errorf("expected [REDACTED:...] in rationale; got: %s", payload.DiaryEntry.Rationale)
	}

	// Verify the diary body (as would be written to MemPalace) also contains the redacted form.
	body := serializeDiary("obj", pgtype.UUID{}, payload, time.Now().UTC())
	if strings.Contains(body, testSKToken) {
		t.Errorf("raw token leaked into serialized diary body")
	}
	if !strings.Contains(body, "[REDACTED:") {
		t.Errorf("expected [REDACTED:...] in serialized diary body; got: %s", body)
	}
}

// TestFinalizeHandlerRedactsInKGTriples — a secret pattern in a kg_triple
// Object is redacted in-place (FR-418).
func TestFinalizeHandlerRedactsInKGTriples(t *testing.T) {
	payload := &finalize.FinalizePayload{
		DiaryEntry: finalize.DiaryEntry{
			Rationale: "clean rationale with no secrets",
		},
		KGTriples: []finalize.KGTriple{
			{Subject: "agent", Predicate: "used_token", Object: "value=" + testSKToken, ValidFrom: time.Now().UTC()},
		},
	}

	labels := scanAndRedactPayload(payload)
	if len(labels) == 0 {
		t.Fatal("expected scanner to find a match in kg_triple.object; got zero labels")
	}
	if strings.Contains(payload.KGTriples[0].Object, testSKToken) {
		t.Errorf("raw token still present in kg_triple.object after redaction: %s", payload.KGTriples[0].Object)
	}
	if !strings.Contains(payload.KGTriples[0].Object, "[REDACTED:") {
		t.Errorf("expected [REDACTED:...] in kg_triple.object; got: %s", payload.KGTriples[0].Object)
	}
}

// TestFinalizeHandlerCleanPayloadUnchanged — a payload with no secret patterns
// is passed through byte-identical and scanAndRedactPayload returns nil.
func TestFinalizeHandlerCleanPayloadUnchanged(t *testing.T) {
	originalRationale := "The ticket asked for hello.md. I wrote it. No secrets involved."
	payload := &finalize.FinalizePayload{
		DiaryEntry: finalize.DiaryEntry{
			Rationale: originalRationale,
		},
		KGTriples: []finalize.KGTriple{
			{Subject: "agent_instance", Predicate: "completed", Object: "ticket_abc", ValidFrom: time.Now().UTC()},
		},
	}

	labels := scanAndRedactPayload(payload)
	if len(labels) != 0 {
		t.Errorf("expected no matches for clean payload; got labels: %v", labels)
	}
	if payload.DiaryEntry.Rationale != originalRationale {
		t.Errorf("rationale was modified for clean payload: got %q; want %q",
			payload.DiaryEntry.Rationale, originalRationale)
	}
	if payload.KGTriples[0].Object != "ticket_abc" {
		t.Errorf("kg_triple.object was modified for clean payload: got %q",
			payload.KGTriples[0].Object)
	}
}

// TestToMempalaceTriples — toMempalaceTriples maps finalize.KGTriple fields
// to mempalace.Triple verbatim.
func TestToMempalaceTriples(t *testing.T) {
	ts := time.Date(2026, 4, 25, 10, 0, 0, 0, time.UTC)
	in := []finalize.KGTriple{
		{Subject: "agent_a", Predicate: "completed", Object: "ticket_1", ValidFrom: ts},
		{Subject: "agent_b", Predicate: "created", Object: "file_x", ValidFrom: ts.Add(time.Second)},
	}
	out := toMempalaceTriples(in)
	if len(out) != len(in) {
		t.Fatalf("len = %d; want %d", len(out), len(in))
	}
	for i, got := range out {
		want := mempalace.Triple{
			Subject:   in[i].Subject,
			Predicate: in[i].Predicate,
			Object:    in[i].Object,
			ValidFrom: in[i].ValidFrom,
		}
		if got != want {
			t.Errorf("[%d] got %+v; want %+v", i, got, want)
		}
	}
}

// TestToMempalaceTriplesEmpty — empty input produces an empty (non-nil) slice.
func TestToMempalaceTriplesEmpty(t *testing.T) {
	out := toMempalaceTriples(nil)
	if out == nil {
		t.Fatal("expected non-nil empty slice; got nil")
	}
	if len(out) != 0 {
		t.Fatalf("len = %d; want 0", len(out))
	}
}

// TestIsCtxDeadlineExceeded — returns true only when ctx deadline exceeded.
func TestIsCtxDeadlineExceeded(t *testing.T) {
	if isCtxDeadlineExceeded(context.Background()) {
		t.Error("expected false for background context")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if !isCtxDeadlineExceeded(ctx) {
		t.Error("expected true for already-expired deadline context")
	}
}

// TestClassifyCtxErr — returns ExitFinalizeWriteTimeout when deadline exceeded,
// otherwise the caller-supplied default.
func TestClassifyCtxErr(t *testing.T) {
	bgCtx := context.Background()
	if got := classifyCtxErr(bgCtx, "default_reason"); got != "default_reason" {
		t.Errorf("got %q; want %q", got, "default_reason")
	}
	deadCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if got := classifyCtxErr(deadCtx, "default_reason"); got != ExitFinalizeWriteTimeout {
		t.Errorf("got %q; want %q", got, ExitFinalizeWriteTimeout)
	}
}

// TestScanAndRedactPayloadSubjectPredicate — secret patterns in
// kg_triple.subject and kg_triple.predicate are redacted in-place.
func TestScanAndRedactPayloadSubjectPredicate(t *testing.T) {
	payload := &finalize.FinalizePayload{
		DiaryEntry: finalize.DiaryEntry{
			Rationale: "clean rationale",
		},
		KGTriples: []finalize.KGTriple{
			{
				Subject:   "token=" + testSKToken,
				Predicate: "used_key=" + testSKToken,
				Object:    "some_object",
				ValidFrom: time.Now().UTC(),
			},
		},
	}
	labels := scanAndRedactPayload(payload)
	if len(labels) == 0 {
		t.Fatal("expected labels for secret in subject+predicate; got none")
	}
	if strings.Contains(payload.KGTriples[0].Subject, testSKToken) {
		t.Errorf("raw token still present in kg_triple.subject: %s", payload.KGTriples[0].Subject)
	}
	if strings.Contains(payload.KGTriples[0].Predicate, testSKToken) {
		t.Errorf("raw token still present in kg_triple.predicate: %s", payload.KGTriples[0].Predicate)
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
