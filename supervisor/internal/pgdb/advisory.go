package pgdb

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// AdvisoryLockKey is the fixed FR-018 single-instance-per-database lock key.
// The value is the ASCII bytes of "garrison" packed into a bigint.
const AdvisoryLockKey int64 = 0x6761727269736f6e

// ErrAdvisoryLockHeld signals that another supervisor process already holds
// the FR-018 advisory lock on this database. Callers exit non-zero.
var ErrAdvisoryLockHeld = errors.New("pgdb: advisory lock already held; another supervisor is running against this database")

// AcquireAdvisoryLock wraps pg_try_advisory_lock on the given connection. The
// lock is session-scoped: closing conn releases it implicitly. A false return
// from Postgres maps to ErrAdvisoryLockHeld; query-level failures propagate
// wrapped.
func AcquireAdvisoryLock(ctx context.Context, conn *pgx.Conn) error {
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", AdvisoryLockKey).Scan(&ok); err != nil {
		return fmt.Errorf("pgdb: pg_try_advisory_lock: %w", err)
	}
	if !ok {
		return ErrAdvisoryLockHeld
	}
	return nil
}
