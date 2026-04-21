// Package recovery owns the FR-011 startup recovery step: reconcile any
// agent_instances row left in 'running' by a previously-crashed supervisor.
// It runs exactly once per process, between advisory-lock acquisition and
// the initial fallback poll.
package recovery

import (
	"context"
	"fmt"
	"time"
)

// RecoveryWindow is the NFR-006 stale-row grace period. Any agent_instances
// row with status='running' and started_at older than this is reconciled to
// 'failed' / 'supervisor_restarted' at startup. The window is baked into the
// SQL of store.RecoverStaleRunning; this constant exists so Go callers can
// reference the same value without duplicating the literal.
const RecoveryWindow = 5 * time.Minute

// Querier is the subset of *store.Queries that RunOnce needs. Keeping the
// interface narrow here means unit tests can stub it without a real Postgres
// and without coupling to the full generated surface.
type Querier interface {
	RecoverStaleRunning(ctx context.Context) (int64, error)
}

// RunOnce executes the FR-011 startup recovery query via the supplied
// Querier and returns the reconciled-row count. Callers pass store.New(pool)
// in production; tests substitute a stub. The name is a contract: calling
// this more than once per process is a supervisor-lifecycle bug.
func RunOnce(ctx context.Context, q Querier) (int, error) {
	n, err := q.RecoverStaleRunning(ctx)
	if err != nil {
		return 0, fmt.Errorf("recovery: RecoverStaleRunning: %w", err)
	}
	return int(n), nil
}
