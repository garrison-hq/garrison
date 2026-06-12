package throttle

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// KindIngressRateCapExceeded is the throttle-event kind emitted when the
// per-connector token bucket fires. Added to the M10 migration's
// throttle_events_kind_check alongside the existing three-value set
// {company_budget_exceeded, rate_limit_pause,
// dept_weekly_ticket_budget_exceeded}.
const KindIngressRateCapExceeded = "ingress_rate_cap_exceeded"

// FireIngressRateCap writes a throttle_events row with
// kind='ingress_rate_cap_exceeded' and the connector-specific payload, then
// emits the work.throttle.event pg_notify. The caller (ingress handler step 7)
// invokes this after RateCap.Allow returns false, before returning 429.
//
// q must be an *store.Queries bound to an open connection (NOT a transaction:
// the handler is not inside a tx at step 7 — the delivery row has not yet been
// inserted). q can be the pool-level Queries; insertEventAndNotify does not
// require a tx boundary.
//
// companyID is resolved once at boot and carried in the handler deps (the M6
// pattern — no per-request company query). ratePerMin and burst are the
// connector's configured parameters, for forensic clarity in the payload.
func FireIngressRateCap(
	ctx context.Context,
	q *store.Queries,
	companyID pgtype.UUID,
	connectorID string,
	ratePerMin, burst int,
) error {
	payload, err := json.Marshal(map[string]any{
		"connector_id":    connectorID,
		"rate_per_minute": ratePerMin,
		"burst":           burst,
	})
	if err != nil {
		return fmt.Errorf("throttle: FireIngressRateCap: marshal payload: %w", err)
	}
	if err := insertEventAndNotify(ctx, q, companyID, KindIngressRateCapExceeded, payload); err != nil {
		return fmt.Errorf("throttle: FireIngressRateCap: %w", err)
	}
	return nil
}
