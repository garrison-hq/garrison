//go:build integration

// M2.2.1 T015 — stuck integration test.
// TestM221FinalizeStuckWhenNeverCalled: agent exits without calling
// finalize_ticket → exit_reason=finalize_never_called, hygiene_status=
// stuck, no ticket_transitions row (SC-254).

package supervisor_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM221FinalizeStuckWhenNeverCalled(t *testing.T) {
	m221RunFixture(t, "m2_2_1_finalize_never_called.ndjson",
		func(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ticketID pgtype.UUID) {
			var status, exitReason string
			_ = pool.QueryRow(ctx, `
				SELECT status, COALESCE(exit_reason,'')
				FROM agent_instances
				WHERE ticket_id=$1 AND role_slug='engineer'`,
				ticketID,
			).Scan(&status, &exitReason)
			// The subprocess may have exited cleanly (status=succeeded
			// by stream vocab) or been classified failed; either
			// outcome is acceptable per spec US5 scenario 1 so long as
			// exit_reason is finalize_never_called.
			if exitReason != "finalize_never_called" {
				t.Errorf("exit_reason=%q; want finalize_never_called (SC-254)", exitReason)
			}
			// No transition row.
			var n int
			_ = pool.QueryRow(ctx,
				`SELECT COUNT(*) FROM ticket_transitions WHERE ticket_id=$1`,
				ticketID,
			).Scan(&n)
			if n != 0 {
				t.Errorf("ticket_transitions rows=%d; want 0 on never-called path", n)
			}
			t.Logf("SC-254: status=%s exit_reason=%s transitions=%d", status, exitReason, n)
		})
}
