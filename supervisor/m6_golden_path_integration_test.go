//go:build integration

package supervisor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/garrisonmutate"
	"github.com/garrison-hq/garrison/supervisor/internal/spawn"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestM6GoldenPathDecompositionAndThrottle is the M6 T018 golden-path
// integration test: chat decomposes a goal into 3 child tickets, the
// supervisor's dispatcher would claim children to spawn, and the
// per-customer cost throttle defers the next claim once today's spend
// would exceed the daily budget.
//
// The test exercises the verb handlers + the spawn-prep gate directly
// (no daemon boot) so the assertion timing is deterministic. Behaviour
// of the live dispatcher loop is covered by TestEndToEndTicketFlow +
// the chaos suite. This test pins the M6-specific composition:
// decomposition (T010 + T011) → audit (M5.3 carryover) →
// throttle.Check defer + audit row + pg_notify (T004 + T007).
func TestM6GoldenPathDecompositionAndThrottle(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	q := store.New(pool)

	// Wipe M6 + M5.3 tables that testdb.Start doesn't truncate so the
	// test is order-independent in the integration suite.
	if _, err := pool.Exec(ctx,
		"TRUNCATE throttle_events, chat_mutation_audit, chat_messages, chat_sessions CASCADE"); err != nil {
		t.Fatalf("truncate M6 + M5.3 tables: %v", err)
	}

	// 1. Seed: company with $2.00 daily budget, engineering dept,
	//    one engineer agent, one CEO chat session + operator message.
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO companies (id, name, daily_budget_usd)
		VALUES (gen_random_uuid(), 'm6 golden path co', 2.00)
		RETURNING id`,
	).Scan(&companyID); err != nil {
		t.Fatalf("insert company: %v", err)
	}
	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		VALUES (gen_random_uuid(), $1, 'engineering', 'Engineering', 5, '/tmp/m6-golden')
		RETURNING id`, companyID,
	).Scan(&deptID); err != nil {
		t.Fatalf("insert department: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agents (department_id, role_slug, agent_md, model, status, listens_for)
		VALUES ($1, 'engineer', 'engineer.md', 'claude-haiku-4-5-20251001', 'active',
			'["work.ticket.created.engineering.todo"]'::jsonb)`, deptID,
	); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	operatorID := pgtype.UUID{Valid: true, Bytes: [16]byte{0xee}}
	var sessionID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO chat_sessions (started_by_user_id, status, total_cost_usd)
		VALUES ($1, 'active', 0)
		RETURNING id`, operatorID,
	).Scan(&sessionID); err != nil {
		t.Fatalf("insert chat_session: %v", err)
	}
	var messageID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO chat_messages (session_id, turn_index, role, status, content)
		VALUES ($1, 0, 'operator', 'completed', 'build me a payment system')
		RETURNING id`, sessionID,
	).Scan(&messageID); err != nil {
		t.Fatalf("insert chat_message: %v", err)
	}

	// 2. Chat-side decomposition: 3 create_ticket calls. The first
	//    is the parent; the other two link to it via parent_ticket_id.
	mutDeps := garrisonmutate.Deps{
		Pool:          pool,
		ChatSessionID: sessionID,
		ChatMessageID: messageID,
	}
	parentArgs := `{"objective":"build payment system","department_slug":"engineering"}`
	parentRes, err := callCreateTicket(ctx, mutDeps, parentArgs)
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if !parentRes.Success {
		t.Fatalf("parent ticket failed: %+v", parentRes)
	}
	parentID := parentRes.AffectedResourceID

	for i := 1; i <= 2; i++ {
		args := fmt.Sprintf(
			`{"objective":"child %d - implement piece","department_slug":"engineering","parent_ticket_id":"%s"}`,
			i, parentID,
		)
		r, err := callCreateTicket(ctx, mutDeps, args)
		if err != nil {
			t.Fatalf("create child %d: %v", i, err)
		}
		if !r.Success {
			t.Fatalf("child %d failed: %+v", i, r)
		}
	}

	// (a) all 3 tickets exist; the two children reference parent.
	var totalTickets int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM tickets WHERE created_via_chat_session_id = $1`, sessionID,
	).Scan(&totalTickets); err != nil {
		t.Fatalf("count tickets: %v", err)
	}
	if totalTickets != 3 {
		t.Errorf("tickets count = %d; want 3", totalTickets)
	}
	var childCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM tickets t
		WHERE t.created_via_chat_session_id = $1
		  AND t.parent_ticket_id IS NOT NULL`, sessionID,
	).Scan(&childCount); err != nil {
		t.Fatalf("count children: %v", err)
	}
	if childCount != 2 {
		t.Errorf("child tickets count = %d; want 2", childCount)
	}

	// (b) chat_mutation_audit carries 3 rows for the create_ticket
	//     verb invocations.
	var auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM chat_mutation_audit
		WHERE chat_session_id = $1 AND verb = 'create_ticket'`, sessionID,
	).Scan(&auditCount); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 3 {
		t.Errorf("audit rows = %d; want 3", auditCount)
	}

	// (c) at least one event_outbox row was created by the
	//     emit_ticket_created trigger (one per InsertChatTicket).
	var outboxCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM event_outbox eo
		JOIN tickets t ON t.id::text = (eo.payload->>'ticket_id')
		WHERE t.created_via_chat_session_id = $1`, sessionID,
	).Scan(&outboxCount); err != nil {
		t.Fatalf("count event_outbox rows: %v", err)
	}
	if outboxCount < 1 {
		t.Errorf("event_outbox rows for chat session = %d; want >=1", outboxCount)
	}

	// (d) Force the rolling-24h spend close to the budget (2.00) by
	//     pre-loading an agent_instance row at $1.95. The spawn-prep
	//     throttle gate should defer the next claim with a
	//     company_budget_exceeded throttle_events row + a live
	//     work.throttle.event notify.
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_instances (id, department_id, ticket_id, role_slug, status,
			started_at, finished_at, total_cost_usd)
		SELECT gen_random_uuid(), $1, t.id, 'engineer', 'succeeded',
		       NOW() - INTERVAL '1 hour', NOW(), 1.95::NUMERIC
		FROM tickets t
		WHERE t.created_via_chat_session_id = $2
		LIMIT 1`,
		deptID, sessionID,
	); err != nil {
		t.Fatalf("preload $1.95 agent_instance: %v", err)
	}

	// Subscribe to work.throttle.event before the gate fires.
	notifyConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire notify conn: %v", err)
	}
	defer notifyConn.Release()
	if _, err := notifyConn.Exec(ctx, `LISTEN "work.throttle.event"`); err != nil {
		t.Fatalf("LISTEN work.throttle.event: %v", err)
	}

	// Pick the first unprocessed event_outbox row for this chat
	// session and run prepareSpawn against it. The gate should defer.
	var eventID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		SELECT eo.id FROM event_outbox eo
		JOIN tickets t ON t.id::text = (eo.payload->>'ticket_id')
		WHERE t.created_via_chat_session_id = $1
		  AND eo.processed_at IS NULL
		ORDER BY eo.created_at ASC
		LIMIT 1`, sessionID,
	).Scan(&eventID); err != nil {
		t.Fatalf("select event_outbox: %v", err)
	}

	defaultCost, err := parseNumeric("0.10")
	if err != nil {
		t.Fatalf("parse default cost: %v", err)
	}
	spawnDeps := spawn.Deps{
		Pool:    pool,
		Queries: q,
		Logger:  slog.New(slog.DiscardHandler),
		Throttle: throttle.Deps{
			Pool:                pool,
			Logger:              slog.New(slog.DiscardHandler),
			DefaultSpawnCostUSD: defaultCost,
			RateLimitBackOff:    60 * time.Second,
			Now:                 time.Now,
		},
		FakeAgentCmd: `sh -c "exit 0"`, // Use fake-agent to avoid claude requirements
		UseFakeAgent: true,
	}
	// spawn.Spawn returns nil on a deferred event (Spawn translates
	// ErrSpawnDeferred → nil for the dispatcher). The deferral
	// signature is in the side effects: throttle_events row +
	// pg_notify + processed_at NULL.
	if err := spawn.Spawn(ctx, spawnDeps, eventID, "engineer"); err != nil {
		t.Fatalf("spawn.Spawn: %v", err)
	}

	// Audit row written?
	var deferredKind string
	if err := pool.QueryRow(ctx, `
		SELECT kind FROM throttle_events
		WHERE company_id = $1 AND kind = 'company_budget_exceeded'
		ORDER BY fired_at DESC LIMIT 1`, companyID,
	).Scan(&deferredKind); err != nil {
		t.Fatalf("read throttle_events: %v", err)
	}
	if deferredKind != "company_budget_exceeded" {
		t.Errorf("throttle_events kind = %q; want company_budget_exceeded", deferredKind)
	}

	// processed_at stayed NULL → next poll retries.
	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT processed_at FROM event_outbox WHERE id = $1`, eventID,
	).Scan(&processed); err != nil {
		t.Fatalf("read event_outbox: %v", err)
	}
	if processed.Valid {
		t.Errorf("event_outbox.processed_at should remain NULL after defer; got %v", processed.Time)
	}

	// LISTEN-side fake observed the notify.
	notifyCtx, cancelNotify := context.WithTimeout(ctx, 3*time.Second)
	defer cancelNotify()
	notif, err := notifyConn.Conn().WaitForNotification(notifyCtx)
	if err != nil {
		t.Fatalf("WaitForNotification: %v", err)
	}
	if !strings.Contains(notif.Payload, "company_budget_exceeded") {
		t.Errorf("notify payload missing kind: %s", notif.Payload)
	}
}

// callCreateTicket dispatches the create_ticket verb via the
// public registry's FindVerb lookup. Mirrors how the M5.3
// MCP server routes a tools/call request.
func callCreateTicket(ctx context.Context, deps garrisonmutate.Deps, argsJSON string) (garrisonmutate.Result, error) {
	verb := garrisonmutate.FindVerb("create_ticket")
	if verb == nil {
		return garrisonmutate.Result{}, fmt.Errorf("create_ticket verb not registered")
	}
	return verb.Handler(ctx, deps, json.RawMessage(argsJSON))
}

// parseNumeric is a tiny helper that decodes a decimal string into
// pgtype.Numeric without importing the spawn helper (which is
// unexported).
func parseNumeric(s string) (pgtype.Numeric, error) {
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		return pgtype.Numeric{}, err
	}
	return n, nil
}

// pgxConnUnused silences any unused-import lint — the pgx import is
// part of the testdb harness contract via spawn.Deps but the test
// body doesn't reference it directly.
var _ = pgx.ErrNoRows
