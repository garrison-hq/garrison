//go:build integration || chaos || live_acceptance

// Shared helpers for M2.2.1 integration and chaos test files.
// Complements integration_m2_2_shared_test.go (M2.2's helpers) — the
// M2.2.1 tests reuse requireSpikeStack, waitFor, tail, safeBuffer,
// buildSupervisorBinary, buildMockClaudeBinary, mustFreePort,
// waitForHealth, repoFile, uuidString from there.
//
// The only M2.2.1-specific helper is the hygiene-status wait helper
// below — the finalize flow writes hygiene_status='clean' synchronously
// inside the atomic tx (not via the hygiene goroutine), so tests can
// wait for the terminal row + check hygiene_status in one round-trip.

package supervisor_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// waitForAgentInstanceCount polls until at least `want` agent_instances
// rows for the ticket have reached a terminal status. Returns on
// timeout or context cancellation.
func waitForAgentInstanceCount(ctx context.Context, t *testing.T, pool *pgxpool.Pool, ticketID pgtype.UUID, want int, timeout time.Duration) {
	t.Helper()
	err := waitFor(ctx, timeout, func() (bool, error) {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM agent_instances
			WHERE ticket_id=$1 AND status IN ('succeeded','failed','timeout')`,
			ticketID,
		).Scan(&n); err != nil {
			return false, err
		}
		return n >= want, nil
	})
	if err != nil {
		t.Fatalf("waiting for %d terminal agent_instances: %v", want, err)
	}
}

// m221InsertTicketAtInDev inserts a ticket directly at column='in_dev',
// returns its id. Mirrors M2.2's pattern for engineer-starting tickets.
func m221InsertTicketAtInDev(ctx context.Context, t *testing.T, pool *pgxpool.Pool, deptID pgtype.UUID, objective string) pgtype.UUID {
	t.Helper()
	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, $2, 'in_dev')
		RETURNING id`, deptID, objective,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}
	return ticketID
}
