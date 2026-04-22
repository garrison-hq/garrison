// Package concurrency owns the per-department cap-accounting query pair used
// before every subprocess spawn. It does not own the defer-vs-spawn decision
// (that lives in internal/events) and does not own advisory locking for
// concurrency — M1 accepts the documented +1 race from plan.md
// §"Concurrency accounting".
package concurrency

import (
	"context"
	"fmt"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// Querier is the subset of *store.Queries that CheckCap needs. Narrow here so
// tests can substitute a stub without a real Postgres. Either a pool-backed
// *store.Queries or a tx-backed one satisfies it: sqlc's DBTX covers both.
type Querier interface {
	GetDepartmentByID(ctx context.Context, id pgtype.UUID) (store.Department, error)
	CountRunningByDepartment(ctx context.Context, departmentID pgtype.UUID) (int64, error)
}

// CheckCap reads concurrency_cap and the current running count for the given
// department and returns whether a new spawn is allowed, along with both
// numbers for logging. A cap of 0 always blocks (FR-003 pause semantics); no
// special-case code needed because 0 < 0 is false.
func CheckCap(ctx context.Context, q Querier, departmentID pgtype.UUID) (allowed bool, cap int, running int, err error) {
	dept, err := q.GetDepartmentByID(ctx, departmentID)
	if err != nil {
		return false, 0, 0, fmt.Errorf("concurrency: GetDepartmentByID: %w", err)
	}
	count, err := q.CountRunningByDepartment(ctx, departmentID)
	if err != nil {
		return false, 0, 0, fmt.Errorf("concurrency: CountRunningByDepartment: %w", err)
	}
	cap = int(dept.ConcurrencyCap)
	running = int(count)
	allowed = running < cap
	return allowed, cap, running, nil
}
