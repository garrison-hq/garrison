//go:build integration

package store_test

import (
	"context"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
)

// TestInsertAndGetDepartment exercises the two sqlc-generated department
// queries against real Postgres: insert round-trips slug/name/cap faithfully
// and GetDepartmentByID returns a row that matches the inserted values.
func TestInsertAndGetDepartment(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	inserted, err := q.InsertDepartment(ctx, store.InsertDepartmentParams{
		Slug:           "eng",
		Name:           "Engineering",
		ConcurrencyCap: 2,
	})
	if err != nil {
		t.Fatalf("InsertDepartment: %v", err)
	}
	if !inserted.ID.Valid {
		t.Fatalf("InsertDepartment returned invalid UUID")
	}

	got, err := q.GetDepartmentByID(ctx, inserted.ID)
	if err != nil {
		t.Fatalf("GetDepartmentByID: %v", err)
	}
	if got.Slug != "eng" || got.Name != "Engineering" || got.ConcurrencyCap != 2 {
		t.Fatalf("GetDepartmentByID mismatch: got %+v", got)
	}
}

// TestSelectUnprocessedEvents relies on the emit_ticket_created trigger.
// Starting with M2.1 (migration 0003) the trigger emits a qualified channel
// of shape work.ticket.created.<dept_slug>.<column_slug>; this test inserts
// a department with slug="eng" and a ticket whose column_slug defaults to
// 'todo', so the expected channel is work.ticket.created.eng.todo.
// SelectUnprocessedEvents must return exactly that row.
func TestSelectUnprocessedEvents(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	dept, err := q.InsertDepartment(ctx, store.InsertDepartmentParams{
		Slug: "eng", Name: "Engineering", ConcurrencyCap: 2,
	})
	if err != nil {
		t.Fatalf("InsertDepartment: %v", err)
	}

	ticket, err := q.InsertTicket(ctx, store.InsertTicketParams{
		DepartmentID: dept.ID,
		Objective:    "hello",
	})
	if err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}

	events, err := q.SelectUnprocessedEvents(ctx)
	if err != nil {
		t.Fatalf("SelectUnprocessedEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Channel != "work.ticket.created.eng.todo" {
		t.Fatalf("expected channel work.ticket.created.eng.todo, got %q", events[0].Channel)
	}

	if err := q.MarkEventProcessed(ctx, events[0].ID); err != nil {
		t.Fatalf("MarkEventProcessed: %v", err)
	}
	after, err := q.SelectUnprocessedEvents(ctx)
	if err != nil {
		t.Fatalf("SelectUnprocessedEvents (after): %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("expected 0 events after mark, got %d", len(after))
	}
	_ = ticket
}

// TestMarkEventProcessedIdempotent covers the FR-006 dedupe contract from
// the other side: a second MarkEventProcessed on the same id is a silent
// no-op (UPDATE ... WHERE processed_at IS NULL matches zero rows) and
// must not error.
func TestMarkEventProcessedIdempotent(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	dept, _ := q.InsertDepartment(ctx, store.InsertDepartmentParams{
		Slug: "eng", Name: "Engineering", ConcurrencyCap: 2,
	})
	if _, err := q.InsertTicket(ctx, store.InsertTicketParams{
		DepartmentID: dept.ID, Objective: "t",
	}); err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}
	events, err := q.SelectUnprocessedEvents(ctx)
	if err != nil || len(events) != 1 {
		t.Fatalf("setup: SelectUnprocessedEvents err=%v len=%d", err, len(events))
	}

	if err := q.MarkEventProcessed(ctx, events[0].ID); err != nil {
		t.Fatalf("first MarkEventProcessed: %v", err)
	}
	if err := q.MarkEventProcessed(ctx, events[0].ID); err != nil {
		t.Fatalf("second MarkEventProcessed: %v", err)
	}
}

// TestRecoverStaleRunning verifies startup recovery under the amended
// NFR-006 semantics: RunOnce executes after the advisory lock is
// acquired and before this process spawns anything, so EVERY
// status='running' row belongs to a dead predecessor and is flipped to
// failed + supervisor_restarted regardless of age. The original
// 5-minute window left young rows stranded across fast crash+restart
// cycles, wedging their department on the concurrency cap (observed
// live, 2026-06-10 acceptance run).
func TestRecoverStaleRunning(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	dept, _ := q.InsertDepartment(ctx, store.InsertDepartmentParams{
		Slug: "eng", Name: "Engineering", ConcurrencyCap: 2,
	})
	ticket, err := q.InsertTicket(ctx, store.InsertTicketParams{
		DepartmentID: dept.ID, Objective: "stale",
	})
	if err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}

	// Young running row: reconciled too under the amended semantics
	// (it cannot belong to a live supervisor at startup).
	freshID, err := q.InsertRunningInstance(ctx, store.InsertRunningInstanceParams{
		DepartmentID: dept.ID,
		TicketID:     ticket.ID,
	})
	if err != nil {
		t.Fatalf("InsertRunningInstance fresh: %v", err)
	}

	// Old running row: reconciled before and after the amendment.
	staleID, err := q.InsertRunningInstance(ctx, store.InsertRunningInstanceParams{
		DepartmentID: dept.ID,
		TicketID:     ticket.ID,
	})
	if err != nil {
		t.Fatalf("InsertRunningInstance stale: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE agent_instances SET started_at = NOW() - INTERVAL '10 minutes' WHERE id = $1",
		staleID,
	); err != nil {
		t.Fatalf("backdate stale row: %v", err)
	}

	n, err := q.RecoverStaleRunning(ctx)
	if err != nil {
		t.Fatalf("RecoverStaleRunning: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 reconciled rows, got %d", n)
	}

	for name, id := range map[string]interface{}{"fresh": freshID, "stale": staleID} {
		var status, reason string
		if err := pool.QueryRow(ctx,
			"SELECT status, exit_reason FROM agent_instances WHERE id = $1", id,
		).Scan(&status, &reason); err != nil {
			t.Fatalf("fetch %s: %v", name, err)
		}
		if status != "failed" || reason != "supervisor_restarted" {
			t.Fatalf("%s row: got status=%q reason=%q", name, status, reason)
		}
	}
}
