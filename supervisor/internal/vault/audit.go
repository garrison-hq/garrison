package vault

import (
	"context"
	"fmt"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Outcome constants for vault_access_log.outcome column (D3.3 / FR-412).
type Outcome = string

const (
	OutcomeGranted         Outcome = "granted"
	OutcomeDeniedNoGrant   Outcome = "denied_no_grant"
	OutcomeDeniedInfisical Outcome = "denied_infisical"
	OutcomeErrorFetching   Outcome = "error_fetching"
	OutcomeErrorInjecting  Outcome = "error_injecting"
	OutcomeErrorAuditing   Outcome = "error_auditing"
)

// AuditRow carries the values for one vault_access_log row.
// AgentInstanceID may be zero-value when the spawn aborts before an
// agent_instances row is written; TicketID is nullable.
type AuditRow struct {
	AgentInstanceID pgtype.UUID
	TicketID        pgtype.UUID // Valid=false for non-ticket-bound ops
	SecretPath      string
	CustomerID      pgtype.UUID
	Outcome         Outcome
	Timestamp       time.Time
}

// WriteAuditRow inserts one vault_access_log row and, when outcome is
// OutcomeGranted, updates secret_metadata.last_accessed_at — both in
// the same caller-owned transaction (D3.4 / Q9 fail-closed).
//
// On any SQL error, returns fmt.Errorf wrapping ErrVaultAuditFailed so
// ClassifyExitReason routes to ExitVaultAuditFailed.
func WriteAuditRow(ctx context.Context, tx pgx.Tx, row AuditRow) error {
	q := store.New(tx)

	ts := pgtype.Timestamptz{}
	if err := ts.Scan(row.Timestamp); err != nil {
		return fmt.Errorf("%w: timestamp: %s", ErrVaultAuditFailed, err)
	}

	if err := q.InsertVaultAccessLog(ctx, store.InsertVaultAccessLogParams{
		AgentInstanceID: row.AgentInstanceID,
		TicketID:        row.TicketID,
		SecretPath:      row.SecretPath,
		CustomerID:      row.CustomerID,
		Outcome:         row.Outcome,
		Timestamp:       ts,
	}); err != nil {
		return fmt.Errorf("%w: %s", ErrVaultAuditFailed, err)
	}

	if row.Outcome == OutcomeGranted {
		if err := q.TouchSecretLastAccessed(ctx, store.TouchSecretLastAccessedParams{
			LastAccessedAt: ts,
			SecretPath:     row.SecretPath,
			CustomerID:     row.CustomerID,
		}); err != nil {
			return fmt.Errorf("%w: touch last_accessed_at: %s", ErrVaultAuditFailed, err)
		}
	}

	return nil
}
