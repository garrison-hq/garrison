//go:build integration

package throttle_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestFireIngressRateCap_WritesEvidence inserts a throttle_events row
// with kind='ingress_rate_cap_exceeded', asserts the JSON payload carries
// connector_id / rate_per_minute / burst, and observes the
// work.throttle.event pg_notify — mirroring the dept_weekly_test.go
// pattern for the M10 ingress evidence path (FR-601, tasks.md T012).
func TestFireIngressRateCap_WritesEvidence(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	truncateAll(t, ctx, pool)

	// Seed a company.
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'ingress-ratecap-co') RETURNING id`,
	).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}

	const connectorID = "github-sortie"
	const ratePerMin = 30
	const burst = 10

	// Arm the LISTEN before firing so we don't race the notify.
	notifyCh := listenForNotify(t, ctx, pool, throttle.ChannelThrottleEvent)

	q := store.New(pool)
	if err := throttle.FireIngressRateCap(ctx, q, companyID, connectorID, ratePerMin, burst); err != nil {
		t.Fatalf("FireIngressRateCap: %v", err)
	}

	// Assert the throttle_events row was written with the correct kind.
	rows, err := q.ListThrottleEventsByCompany(ctx, store.ListThrottleEventsByCompanyParams{
		CompanyID: companyID,
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("ListThrottleEventsByCompany: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("throttle_events rows = %d; want 1", len(rows))
	}
	if rows[0].Kind != throttle.KindIngressRateCapExceeded {
		t.Errorf("kind = %q; want %q", rows[0].Kind, throttle.KindIngressRateCapExceeded)
	}

	// Assert the JSON payload carries the three forensic fields.
	var payload map[string]any
	if err := json.Unmarshal(rows[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["connector_id"].(string); got != connectorID {
		t.Errorf("payload.connector_id = %q; want %q", got, connectorID)
	}
	// JSON numbers decode as float64 in map[string]any.
	if got, _ := payload["rate_per_minute"].(float64); int(got) != ratePerMin {
		t.Errorf("payload.rate_per_minute = %v; want %d", got, ratePerMin)
	}
	if got, _ := payload["burst"].(float64); int(got) != burst {
		t.Errorf("payload.burst = %v; want %d", got, burst)
	}

	// Assert the pg_notify arrived on work.throttle.event.
	select {
	case body := <-notifyCh:
		if !strings.Contains(body, throttle.KindIngressRateCapExceeded) {
			t.Errorf("notify body missing kind %q: %s", throttle.KindIngressRateCapExceeded, body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("work.throttle.event notify never arrived")
	}
}
