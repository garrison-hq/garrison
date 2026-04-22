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

// TestSelectUnprocessedEvents relies on the emit_ticket_created trigger
// installed in migration 0002: inserting a ticket must write one
// event_outbox row with channel=work.ticket.created, and
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
	if events[0].Channel != "work.ticket.created" {
		t.Fatalf("expected channel work.ticket.created, got %q", events[0].Channel)
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

// TestRecoverStaleRunning verifies NFR-006: a running agent_instance whose
// started_at is older than 5 minutes is flipped to failed +
// supervisor_restarted by RecoverStaleRunning. A row inside the window is
// left untouched.
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

	// Fresh running row (should NOT be reconciled).
	freshID, err := q.InsertRunningInstance(ctx, store.InsertRunningInstanceParams{
		DepartmentID: dept.ID,
		TicketID:     ticket.ID,
	})
	if err != nil {
		t.Fatalf("InsertRunningInstance fresh: %v", err)
	}

	// Stale running row: insert then rewrite started_at to 10 minutes ago.
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
	if n != 1 {
		t.Fatalf("expected 1 reconciled row, got %d", n)
	}

	// Verify the stale row is now failed/supervisor_restarted and the fresh
	// row is still running. We go through the pool directly because the
	// generated queries don't expose a generic "get instance" by id.
	var staleStatus, staleReason string
	if err := pool.QueryRow(ctx,
		"SELECT status, exit_reason FROM agent_instances WHERE id = $1", staleID,
	).Scan(&staleStatus, &staleReason); err != nil {
		t.Fatalf("fetch stale: %v", err)
	}
	if staleStatus != "failed" || staleReason != "supervisor_restarted" {
		t.Fatalf("stale row: got status=%q reason=%q", staleStatus, staleReason)
	}

	var freshStatus string
	if err := pool.QueryRow(ctx,
		"SELECT status FROM agent_instances WHERE id = $1", freshID,
	).Scan(&freshStatus); err != nil {
		t.Fatalf("fetch fresh: %v", err)
	}
	if freshStatus != "running" {
		t.Fatalf("fresh row status got=%q want=running", freshStatus)
	}
}
