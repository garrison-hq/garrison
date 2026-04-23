//go:build integration

// M2.2.2 T011 — retry integration tests against the M2.2.2 richer
// error shape + new seeds.
//
//   - TestM222RetryAfterSchemaError: 1 malformed + 1 valid →
//     hygiene_status=clean, transition commits (US1 / SC-305). Pins
//     the recovery-in-one-retry scenario M2.2.2's thesis depends on.
//   - TestM222ThreeRetriesExhausted: 3 malformed → exit_reason=
//     finalize_invalid, no transition, ticket stays at in_dev
//     (US2 / SC-306). Regression check that the 3-attempt cap still
//     works with the Q9-additive richer error shape.
//
// Reuses M2.2.1's m221RunFixture helper (milestone-agnostic spawner)
// with the two new m2_2_2_* fixtures from T010.

package supervisor_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestM222RetryAfterSchemaError — SC-305 / US1. One malformed attempt
// followed by one well-formed attempt. After the engineer turn, the
// agent_instances row is succeeded and the in_dev→qa_review
// transition carries hygiene_status='clean'. Mirrors M2.2.1's
// retryThenSuccess pattern; the difference is the number of failed
// attempts (1 vs 2) — M2.2.2's rich hints should make a single
// correction cycle sufficient.
func TestM222RetryAfterSchemaError(t *testing.T) {
	m221RunFixture(t, "m2_2_2_retry_after_schema_error.ndjson",
		m222RetryAfterSchemaAssertions)
}

// TestM222ThreeRetriesExhausted — SC-306 / US2. Three consecutive
// malformed attempts exhaust the supervisor-side retry counter. The
// new error fields are Q9-additive, so the supervisor's pipeline
// observer parses `ok` identically to M2.2.1; the cap behaviour is
// unchanged. Reuses M2.2.1's retryExhaustedAssertions verbatim.
func TestM222ThreeRetriesExhausted(t *testing.T) {
	m221RunFixture(t, "m2_2_2_three_retries_exhausted.ndjson",
		retryExhaustedAssertions)
}

// m222RetryAfterSchemaAssertions mirrors the M2.2.1
// retryThenSuccessAssertions but spells out the M2.2.2 expectation
// clause-by-clause for SC-305 readability — one agent_instances row
// succeeded, one ticket_transitions row with hygiene='clean'.
func m222RetryAfterSchemaAssertions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ticketID pgtype.UUID) {
	var status, exitReason, hygStatus string
	if err := pool.QueryRow(ctx, `
		SELECT ai.status, COALESCE(ai.exit_reason,''), COALESCE(tt.hygiene_status,'(null)')
		FROM agent_instances ai
		JOIN ticket_transitions tt ON tt.triggered_by_agent_instance_id = ai.id
		WHERE ai.ticket_id=$1 AND ai.role_slug='engineer'`,
		ticketID,
	).Scan(&status, &exitReason, &hygStatus); err != nil {
		t.Fatalf("query engineer row: %v", err)
	}
	if status != "succeeded" {
		t.Errorf("status=%q; want succeeded (SC-305)", status)
	}
	if hygStatus != "clean" {
		t.Errorf("hygiene_status=%q; want clean (SC-305)", hygStatus)
	}

	// Verify the transition landed where expected.
	var fromCol, toCol string
	if err := pool.QueryRow(ctx, `
		SELECT from_column, to_column
		FROM ticket_transitions
		WHERE ticket_id=$1`,
		ticketID,
	).Scan(&fromCol, &toCol); err != nil {
		t.Fatalf("query transition row: %v", err)
	}
	if fromCol != "in_dev" || toCol != "qa_review" {
		t.Errorf("transition = %s → %s; want in_dev → qa_review", fromCol, toCol)
	}
}
