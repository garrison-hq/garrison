package ingress

import (
	"context"
	"errors"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// insertDelivery inserts the idempotency record for a delivery. It is called
// as the first step inside the handler transaction (plan decision 4) so the
// unique-constraint-on-insert is the M1-race-safe dedup signal: a concurrent
// second delivery of the same GUID blocks on the index until the first tx
// commits or rolls back, then sees 23505 — ErrDuplicateDelivery (FR-201,
// FR-202, SR2).
//
// Returns the new ingress_deliveries.id for use by BackfillIngressDeliveryTicket.
// On 23505 returns (zero UUID, ErrDuplicateDelivery); any other error is
// returned as-is.
func insertDelivery(ctx context.Context, q *store.Queries, connID, deliveryID string) (pgtype.UUID, error) {
	row, err := q.InsertIngressDelivery(ctx, store.InsertIngressDeliveryParams{
		ConnectorID:        connID,
		ExternalDeliveryID: deliveryID,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Unique-violation: this delivery was already recorded.
			// Return ErrDuplicateDelivery so the handler can abort the
			// ticket-insert path and return 200 with no further side
			// effects (FR-202, plan decision 4).
			return pgtype.UUID{}, ErrDuplicateDelivery
		}
		return pgtype.UUID{}, err
	}
	return row.ID, nil
}
