package throttle

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// insertEventAndNotify is the shared path used by FirePause and
// FireBudgetDefer. INSERTs the throttle_events row, then PERFORMs
// pg_notify(work.throttle.event, payload) on the same tx. The
// payload shape matches the M5.x work.* convention so the dashboard
// SSE bridge (M6 T015) can consume it the same way.
func insertEventAndNotify(
	ctx context.Context,
	q *store.Queries,
	companyID pgtype.UUID,
	kind string,
	payload []byte,
) error {
	row, err := q.InsertThrottleEvent(ctx, store.InsertThrottleEventParams{
		CompanyID: companyID,
		Kind:      kind,
		Payload:   payload,
	})
	if err != nil {
		return fmt.Errorf("InsertThrottleEvent: %w", err)
	}
	notifyBody, err := json.Marshal(map[string]string{
		"event_id":   uuidString(row.ID),
		"company_id": uuidString(companyID),
		"kind":       kind,
		"fired_at":   row.FiredAt.Time.Format(time.RFC3339Nano),
	})
	if err != nil {
		return fmt.Errorf("marshal notify body: %w", err)
	}
	if err := q.NotifyThrottleEvent(ctx, string(notifyBody)); err != nil {
		return fmt.Errorf("pg_notify work.throttle.event: %w", err)
	}
	return nil
}

// uuidString formats a pgtype.UUID as the canonical 36-char hex
// representation. Mirrors the helper in internal/chat/persistence.go;
// kept local to avoid cross-package internal coupling.
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u.Bytes[0:4], u.Bytes[4:6], u.Bytes[6:8], u.Bytes[8:10], u.Bytes[10:16])
}
