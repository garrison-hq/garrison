//go:build integration

package supervisor_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEndToEndTicketFlow is the M1 smoke test: boot a real supervisor
// binary, insert one ticket, observe exactly one succeeded agent_instance
// and a processed event_outbox row. If this passes, the happy path
// (ticket → trigger → pg_notify → LISTEN → spawn → terminal tx) works
// end-to-end.
func TestEndToEndTicketFlow(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	startSupervisor(t, supervisorOpts{
		FakeAgentCmd: `sh -c "echo ok; exit 0"`,
		PollInterval: "1s",
	})

	dept := mustInsertDepartment(t, q, "engineering", 2)
	if _, err := q.InsertTicket(ctx, store.InsertTicketParams{
		DepartmentID: dept.ID, Objective: "golden path",
	}); err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}

	waitForTerminalCount(t, pool, 1, 10*time.Second, "succeeded")

	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT processed_at FROM event_outbox ORDER BY created_at DESC LIMIT 1`,
	).Scan(&processed); err != nil {
		t.Fatalf("fetch event_outbox: %v", err)
	}
	if !processed.Valid {
		t.Fatalf("event_outbox.processed_at is NULL; terminal tx did not commit")
	}
}

// TestConcurrencyCapEnforced (US2): with cap=2 and three tickets queued, at
// most two agent_instances should ever be simultaneously running. All three
// eventually reach status=succeeded.
func TestConcurrencyCapEnforced(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	startSupervisor(t, supervisorOpts{
		FakeAgentCmd: `sh -c "sleep 1; echo ok"`,
		PollInterval: "1s",
	})

	dept := mustInsertDepartment(t, q, "engineering", 2)
	for i := 0; i < 3; i++ {
		if _, err := q.InsertTicket(ctx, store.InsertTicketParams{
			DepartmentID: dept.ID, Objective: fmt.Sprintf("t%d", i),
		}); err != nil {
			t.Fatalf("InsertTicket %d: %v", i, err)
		}
	}

	maxRunning := sampleMaxRunning(t, pool, 3*time.Second)
	if maxRunning > 2 {
		t.Fatalf("cap=2 violated: observed max running=%d", maxRunning)
	}

	waitForTerminalCount(t, pool, 3, 15*time.Second, "succeeded")
}

// TestDeferredEventPickedUpOnPoll: with cap=1 and two tickets, the second
// one is deferred while the first runs. After the first finishes, the
// fallback poll picks the deferred event up and it too succeeds.
func TestDeferredEventPickedUpOnPoll(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	startSupervisor(t, supervisorOpts{
		FakeAgentCmd: `sh -c "sleep 1; echo ok"`,
		PollInterval: "1s",
	})

	dept := mustInsertDepartment(t, q, "engineering", 1)
	for i := 0; i < 2; i++ {
		if _, err := q.InsertTicket(ctx, store.InsertTicketParams{
			DepartmentID: dept.ID, Objective: fmt.Sprintf("t%d", i),
		}); err != nil {
			t.Fatalf("InsertTicket: %v", err)
		}
	}

	waitForTerminalCount(t, pool, 2, 15*time.Second, "succeeded")
}

// TestDepartmentNotExistMarksProcessed (invariant #5): an event_outbox row
// whose department_id does not resolve is marked processed with no
// agent_instances row written. Crafted by inserting the event directly so
// there is no parent ticket/department pair to satisfy the FK.
func TestDepartmentNotExistMarksProcessed(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()

	startSupervisor(t, supervisorOpts{
		FakeAgentCmd: `sh -c "echo should-not-run"`,
		PollInterval: "1s",
	})

	// Random UUIDs — neither resolves to a real department or ticket.
	payload := `{"ticket_id":"11111111-1111-1111-1111-111111111111","department_id":"22222222-2222-2222-2222-222222222222"}`
	var eventID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO event_outbox (channel, payload) VALUES ('work.ticket.created.engineering.todo', $1::jsonb) RETURNING id`,
		payload,
	).Scan(&eventID); err != nil {
		t.Fatalf("insert orphan event: %v", err)
	}

	// Wait for the poller to pick it up and mark it processed.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var processed pgtype.Timestamptz
		_ = pool.QueryRow(ctx,
			`SELECT processed_at FROM event_outbox WHERE id = $1`, eventID,
		).Scan(&processed)
		if processed.Valid {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT processed_at FROM event_outbox WHERE id = $1`, eventID,
	).Scan(&processed); err != nil {
		t.Fatalf("fetch processed_at: %v", err)
	}
	if !processed.Valid {
		t.Fatalf("orphan event was not marked processed within 10s")
	}

	var count int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM agent_instances`).Scan(&count); err != nil {
		t.Fatalf("count agent_instances: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 agent_instances rows, got %d", count)
	}
}

// TestCapZeroPauses (FR-003): cap=0 means all work for that department is
// paused. A ticket inserted against a cap=0 department produces an
// unprocessed event_outbox row and no agent_instance. Unpausing (cap=1)
// lets it drain via the next poll.
func TestCapZeroPauses(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	startSupervisor(t, supervisorOpts{
		FakeAgentCmd: `sh -c "echo ok"`,
		PollInterval: "1s",
	})

	dept := mustInsertDepartment(t, q, "engineering", 0)
	if _, err := q.InsertTicket(ctx, store.InsertTicketParams{
		DepartmentID: dept.ID, Objective: "paused",
	}); err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}

	// Give the supervisor plenty of time to attempt and defer the event.
	time.Sleep(2 * time.Second)

	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT processed_at FROM event_outbox ORDER BY created_at DESC LIMIT 1`,
	).Scan(&processed); err != nil {
		t.Fatalf("fetch processed_at: %v", err)
	}
	if processed.Valid {
		t.Fatalf("cap=0 should have left the event unprocessed, but processed_at is set")
	}
	var running int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM agent_instances`).Scan(&running); err != nil {
		t.Fatalf("count agent_instances: %v", err)
	}
	if running != 0 {
		t.Fatalf("cap=0 should have produced no agent_instances rows, got %d", running)
	}

	// Unpause by raising the cap; the next poll cycle should drain it.
	if _, err := pool.Exec(ctx,
		`UPDATE departments SET concurrency_cap = 1 WHERE id = $1`, dept.ID,
	); err != nil {
		t.Fatalf("raise cap: %v", err)
	}
	waitForTerminalCount(t, pool, 1, 10*time.Second, "succeeded")
}

// TestStartupFallbackPollBeforeListen (US3): events that arrived while the
// supervisor was down are drained by the initial fallback poll (before the
// LISTEN loop starts). Crafted by seeding the event directly before
// starting the supervisor.
func TestStartupFallbackPollBeforeListen(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	// Seed a valid ticket while the supervisor is NOT running. The
	// INSERT fires the trigger which writes an event_outbox row and
	// pg_notify — but with no listener, the notification is lost.
	// Only the event_outbox row survives.
	dept := mustInsertDepartment(t, q, "engineering", 2)
	if _, err := q.InsertTicket(ctx, store.InsertTicketParams{
		DepartmentID: dept.ID, Objective: "pre-existing",
	}); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	var seeded int64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM event_outbox WHERE processed_at IS NULL`,
	).Scan(&seeded); err != nil || seeded != 1 {
		t.Fatalf("seeded event_outbox: count=%d err=%v", seeded, err)
	}

	// Now start the supervisor — the initial poll should drain the row.
	startSupervisor(t, supervisorOpts{
		FakeAgentCmd: `sh -c "echo ok"`,
		PollInterval: "1s",
	})

	waitForTerminalCount(t, pool, 1, 10*time.Second, "succeeded")
}

// TestAdvisoryLockRejectsDoubleRun (FR-018): when one supervisor holds the
// advisory lock, a second one must exit with code 4 and not touch the DB.
func TestAdvisoryLockRejectsDoubleRun(t *testing.T) {
	_ = testdb.Start(t) // ensure container is up and migrations applied
	url := testdb.URL(t)

	// Supervisor A: long-lived, holds the lock.
	startSupervisor(t, supervisorOpts{
		FakeAgentCmd: `sh -c "echo ok"`,
		PollInterval: "1s",
	})

	// Supervisor B: direct exec (no startSupervisor helper so we can observe
	// the exit code rather than relying on the cleanup SIGTERM path).
	bin := buildSupervisorBinary(t)
	port := mustFreePort(t)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"GARRISON_DATABASE_URL="+url,
		`GARRISON_FAKE_AGENT_CMD=sh -c "echo ok"`,
		fmt.Sprintf("GARRISON_HEALTH_PORT=%d", port),
		"GARRISON_LOG_LEVEL=error",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("supervisor B exited 0 but should have exited 4; out=%s", out)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("unexpected error type: %v", err)
	}
	if got := exitErr.ExitCode(); got != 4 {
		t.Fatalf("supervisor B exit code got=%d want=4; out=%s", got, out)
	}
}

// TestRecoveryMarksStaleRunning (NFR-006 + FR-011): on startup, any
// agent_instance row left status=running from a prior supervisor process
// with started_at older than 5 minutes is reconciled to failed /
// supervisor_restarted.
func TestRecoveryMarksStaleRunning(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	dept := mustInsertDepartment(t, q, "engineering", 2)
	ticket, err := q.InsertTicket(ctx, store.InsertTicketParams{
		DepartmentID: dept.ID, Objective: "stale",
	})
	if err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}
	// Drain the event that InsertTicket fired so the supervisor's initial
	// poll doesn't pick it up and spawn a new run (which would interfere
	// with the stale-row assertion).
	if _, err := pool.Exec(ctx,
		`UPDATE event_outbox SET processed_at = NOW() WHERE processed_at IS NULL`,
	); err != nil {
		t.Fatalf("drain outbox: %v", err)
	}
	staleID, err := q.InsertRunningInstance(ctx, store.InsertRunningInstanceParams{
		DepartmentID: dept.ID, TicketID: ticket.ID,
	})
	if err != nil {
		t.Fatalf("InsertRunningInstance: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE agent_instances SET started_at = NOW() - INTERVAL '10 minutes' WHERE id = $1`,
		staleID,
	); err != nil {
		t.Fatalf("backdate stale row: %v", err)
	}

	startSupervisor(t, supervisorOpts{
		FakeAgentCmd: `sh -c "echo ok"`,
		PollInterval: "1s",
	})

	// Recovery runs before the first poll so the row should be terminal
	// almost immediately.
	deadline := time.Now().Add(5 * time.Second)
	var (
		status string
		reason *string
	)
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx,
			`SELECT status, exit_reason FROM agent_instances WHERE id = $1`, staleID,
		).Scan(&status, &reason); err == nil && status == "failed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != "failed" {
		t.Fatalf("stale row status got=%q want=failed", status)
	}
	if reason == nil || *reason != "supervisor_restarted" {
		t.Fatalf("stale row exit_reason got=%v want=supervisor_restarted", reason)
	}
}

// TestHealthReturns200WhenReady (FR-016 / Q2): /health is 200 after the
// first poll lands. 503 before that or when ping fails. This is the
// positive case.
func TestHealthReturns200WhenReady(t *testing.T) {
	_ = testdb.Start(t)
	port, _ := startSupervisor(t, supervisorOpts{
		FakeAgentCmd: `sh -c "echo ok"`,
		PollInterval: "1s",
	})
	// startSupervisor already waits for /health=200 via waitForHealth, so if
	// we reach here the positive case is covered. Add an explicit assertion
	// to document the intent.
	if !statusOK(fmt.Sprintf("http://127.0.0.1:%d/health", port)) {
		t.Fatalf("expected /health=200 after startup")
	}
}

// TestHundredTicketVolume (SC-002, SC-003): insert 100 tickets against a
// cap-2 department with fake agents that sleep 1s, then assert (i) all 100
// reach terminal state with matching event_outbox.processed_at, and (ii) a
// background sampler never observes running > cap+1.
func TestHundredTicketVolume(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping volume test in -short mode")
	}
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	startSupervisor(t, supervisorOpts{
		FakeAgentCmd: `sh -c "sleep 1; echo ok"`,
		PollInterval: "1s",
	})

	dept := mustInsertDepartment(t, q, "engineering", 2)

	// Start sampler before inserting tickets so we catch the ramp-up too.
	sampleCtx, stopSampler := context.WithCancel(ctx)
	defer stopSampler()
	var (
		samplerMu  sync.Mutex
		maxRunning int64
	)
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-sampleCtx.Done():
				return
			case <-tick.C:
				var n int64
				if err := pool.QueryRow(sampleCtx,
					`SELECT COUNT(*) FROM agent_instances WHERE status='running'`,
				).Scan(&n); err == nil {
					samplerMu.Lock()
					if n > maxRunning {
						maxRunning = n
					}
					samplerMu.Unlock()
				}
			}
		}
	}()

	for i := 0; i < 100; i++ {
		if _, err := q.InsertTicket(ctx, store.InsertTicketParams{
			DepartmentID: dept.ID, Objective: fmt.Sprintf("volume-%d", i),
		}); err != nil {
			t.Fatalf("InsertTicket %d: %v", i, err)
		}
	}

	// cap=2, 1s per subprocess, 100 tickets ≈ 50s ideal. Add headroom.
	waitForTerminalCount(t, pool, 100, 90*time.Second, "succeeded")

	stopSampler()
	<-samplerDone

	samplerMu.Lock()
	peak := maxRunning
	samplerMu.Unlock()
	// cap + 1 tolerance documented in the M1 plan: CheckCap + InsertRunningInstance
	// is not atomic across independent handlers, so a +1 transient overrun is
	// acceptable.
	if peak > 3 {
		t.Fatalf("cap-2 + tolerance: peak running=%d exceeds cap+1=3", peak)
	}

	// All 100 events should be processed.
	var processedCount int64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM event_outbox WHERE processed_at IS NOT NULL`,
	).Scan(&processedCount); err != nil {
		t.Fatalf("count processed: %v", err)
	}
	if processedCount != 100 {
		t.Fatalf("expected 100 processed events, got %d", processedCount)
	}
}

// ---------------------------------------------------------------------
// T015: M2.1 golden-path end-to-end test (mock claude + real pipeline)
// ---------------------------------------------------------------------

// TestM21HelloWorldEndToEnd is the load-bearing M2.1 acceptance test —
// covers criteria A1–A9 in a single run. It does not use the real
// claude binary (T018 owns the real-binary chaos tests); instead it
// points GARRISON_CLAUDE_BIN at the compiled mockclaude binary which
// replays a canned NDJSON stream AND performs the file-write side
// effect (writing hello.txt with the ticket ID) that the supervisor's
// post-run acceptance check looks for.
//
// What this test pins:
//   - A1: real claude binary (substituted here by an NDJSON-emitting
//     stand-in that speaks the same argv + stdout contract).
//   - A2: MCP config file written before spawn, removed on exit
//     (verified by checking the config dir is empty after).
//   - A3: init event observed with mcp_servers.postgres=connected.
//   - A4: agent_instances.pid populated (UpdatePID backfill).
//   - A5: result event parsed, total_cost_usd = 0.003.
//   - A6: the engineer writes hello.txt with the ticket id.
//   - A7: hello.txt contents = ticket ID exactly.
//   - A8: agent_instances row is succeeded / completed with the cost.
//   - A9: ticket_transitions row todo→done with hygiene_status NULL,
//     tickets.column_slug updated to 'done'.
func TestM21HelloWorldEndToEnd(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()

	workspaceDir := t.TempDir()
	mcpConfigDir := t.TempDir()
	deptID := testdb.SeedM21(t, workspaceDir)

	mockBin := buildMockClaudeBinary(t)
	scriptPath := mockClaudeScriptPath(t, "helloworld.ndjson")

	startSupervisor(t, supervisorOpts{
		ClaudeBin:        mockBin,
		AgentROPassword:  "integration-test-ro",
		MCPConfigDir:     mcpConfigDir,
		MockClaudeScript: scriptPath,
		PollInterval:     "1s",
		LogLevel:         "info",
	})

	ticket, err := (store.New(pool)).InsertTicket(ctx, store.InsertTicketParams{
		DepartmentID: deptID,
		Objective:    "write hello world",
	})
	if err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}
	ticketID := formatTicketID(t, ticket.ID)

	// Wait up to 20 s for the agent_instance to reach a terminal state.
	// Running the full suite on a loaded machine slows the LISTEN-notify
	// roundtrip; in isolation the happy path completes in ≈1 s.
	waitForTerminalCount(t, pool, 1, 20*time.Second, "succeeded")

	// A6/A7: hello.txt exists in the workspace with the ticket ID as
	// its exact contents.
	helloPath := fmt.Sprintf("%s/hello.txt", workspaceDir)
	got, err := os.ReadFile(helloPath)
	if err != nil {
		t.Fatalf("read %s: %v", helloPath, err)
	}
	if string(got) != ticketID {
		t.Errorf("hello.txt = %q; want exactly the ticket id %q", string(got), ticketID)
	}

	// A4/A5/A8: agent_instances row is succeeded/completed with pid
	// and total_cost_usd populated.
	var (
		status     string
		exitReason *string
		pid        *int32
		cost       *string
	)
	if err := pool.QueryRow(ctx, `
		SELECT status, exit_reason, pid, total_cost_usd::text
		FROM agent_instances
		WHERE ticket_id = $1`,
		ticket.ID,
	).Scan(&status, &exitReason, &pid, &cost); err != nil {
		t.Fatalf("fetch agent_instance: %v", err)
	}
	if status != "succeeded" {
		t.Errorf("agent_instance.status = %q; want succeeded", status)
	}
	if exitReason == nil || *exitReason != "completed" {
		t.Errorf("agent_instance.exit_reason = %v; want 'completed'", exitReason)
	}
	if pid == nil || *pid == 0 {
		t.Errorf("agent_instance.pid = %v; want non-zero (UpdatePID should have run)", pid)
	}
	if cost == nil {
		t.Errorf("agent_instance.total_cost_usd is NULL; want 0.003")
	} else if normaliseCost(*cost) != "0.003" {
		// NUMERIC(10,6) text rendering pads to "0.003000"; normalise by
		// trimming trailing zeros so the comparison is scale-agnostic.
		t.Errorf("agent_instance.total_cost_usd = %q; want 0.003 (±trailing zeros)", *cost)
	}

	// A9: ticket_transitions row todo→done with hygiene_status NULL,
	// tickets.column_slug = 'done'.
	var (
		fromCol       *string
		toCol         string
		hygieneStatus *string
	)
	if err := pool.QueryRow(ctx, `
		SELECT from_column, to_column, hygiene_status
		FROM ticket_transitions
		WHERE ticket_id = $1`,
		ticket.ID,
	).Scan(&fromCol, &toCol, &hygieneStatus); err != nil {
		t.Fatalf("fetch ticket_transition: %v", err)
	}
	if fromCol == nil || *fromCol != "todo" {
		t.Errorf("ticket_transition.from_column = %v; want 'todo'", fromCol)
	}
	if toCol != "done" {
		t.Errorf("ticket_transition.to_column = %q; want 'done'", toCol)
	}
	if hygieneStatus != nil {
		t.Errorf("ticket_transition.hygiene_status = %v; want NULL", hygieneStatus)
	}

	var columnSlug string
	if err := pool.QueryRow(ctx,
		`SELECT column_slug FROM tickets WHERE id = $1`, ticket.ID,
	).Scan(&columnSlug); err != nil {
		t.Fatalf("fetch ticket.column_slug: %v", err)
	}
	if columnSlug != "done" {
		t.Errorf("tickets.column_slug = %q; want 'done'", columnSlug)
	}

	// event_outbox.processed_at is set.
	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT processed_at FROM event_outbox ORDER BY created_at DESC LIMIT 1`,
	).Scan(&processed); err != nil {
		t.Fatalf("fetch event_outbox: %v", err)
	}
	if !processed.Valid {
		t.Error("event_outbox.processed_at is NULL; terminal tx did not commit")
	}

	// A2: the per-invocation MCP config file was removed on exit. The
	// mcpConfigDir should be empty now.
	entries, err := os.ReadDir(mcpConfigDir)
	if err != nil {
		t.Fatalf("read mcp config dir: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("mcp config dir not cleaned up; got %v", names)
	}
}

// formatTicketID renders a pgtype.UUID to the canonical hex form the
// mockclaude regex extracts. Shared with T016/T017 tests too.
func formatTicketID(t *testing.T, u pgtype.UUID) string {
	t.Helper()
	if !u.Valid {
		t.Fatalf("formatTicketID: invalid uuid")
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// normaliseCost trims the trailing zero-padding NUMERIC(scale) adds so
// tests can compare costs without caring about column scale. "0.003000"
// → "0.003"; "0.003" → "0.003"; "0.00" → "0" (then the caller should
// compare against "0"). Used by T015+ assertions.
func normaliseCost(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" {
		return "0"
	}
	return s
}

// ---------------------------------------------------------------------
// T016: M2.1 Claude failure-path integration tests
// ---------------------------------------------------------------------

// m21TestSetup bundles the common scaffolding every T016/T017 test
// needs: seed, build the mock, start the supervisor against a named
// script, insert a ticket, and return the ticket ID plus the open
// workspace/config dirs so the assertion block can inspect them.
type m21TestSetup struct {
	TicketID      string
	TicketUUID    pgtype.UUID
	DepartmentID  pgtype.UUID
	WorkspaceDir  string
	MCPConfigDir  string
	Pool          *pgxpool.Pool
	SupervisorBin string
}

func startM21Scenario(t *testing.T, scriptName string, extra func(*supervisorOpts)) m21TestSetup {
	t.Helper()
	pool := testdb.Start(t)
	workspaceDir := t.TempDir()
	mcpConfigDir := t.TempDir()
	deptID := testdb.SeedM21(t, workspaceDir)
	mockBin := buildMockClaudeBinary(t)
	scriptPath := mockClaudeScriptPath(t, scriptName)

	opts := supervisorOpts{
		ClaudeBin:        mockBin,
		AgentROPassword:  "integration-test-ro",
		MCPConfigDir:     mcpConfigDir,
		MockClaudeScript: scriptPath,
		PollInterval:     "1s",
		LogLevel:         "info",
	}
	if extra != nil {
		extra(&opts)
	}
	startSupervisor(t, opts)

	ticket, err := store.New(pool).InsertTicket(context.Background(), store.InsertTicketParams{
		DepartmentID: deptID,
		Objective:    "failure-scenario ticket",
	})
	if err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}
	return m21TestSetup{
		TicketID:     formatTicketID(t, ticket.ID),
		TicketUUID:   ticket.ID,
		DepartmentID: deptID,
		WorkspaceDir: workspaceDir,
		MCPConfigDir: mcpConfigDir,
		Pool:         pool,
	}
}

// waitForTerminalAny polls for any terminal status (succeeded | failed |
// timeout) against the supplied ticket_id. Returns the agent_instance
// row fields relevant to failure-path assertions. Fails the test if
// no terminal row lands within the budget.
type terminalRow struct {
	Status     string
	ExitReason *string
	Pid        *int32
	Cost       *string
}

func waitForTerminalByTicket(t *testing.T, pool *pgxpool.Pool, ticketID pgtype.UUID, within time.Duration) terminalRow {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(within)
	var row terminalRow
	for time.Now().Before(deadline) {
		err := pool.QueryRow(ctx, `
			SELECT status, exit_reason, pid, total_cost_usd::text
			FROM agent_instances
			WHERE ticket_id = $1 AND status <> 'running'`,
			ticketID,
		).Scan(&row.Status, &row.ExitReason, &row.Pid, &row.Cost)
		if err == nil {
			return row
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no terminal agent_instance row for ticket %v within %s", ticketID, within)
	return row
}

// assertNoTransition asserts there is no ticket_transitions row for the
// given ticket. Non-success exit reasons must not write a transition
// (FR-114).
func assertNoTransition(t *testing.T, pool *pgxpool.Pool, ticketID pgtype.UUID) {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM ticket_transitions WHERE ticket_id = $1`, ticketID,
	).Scan(&n); err != nil {
		t.Fatalf("count transitions: %v", err)
	}
	if n != 0 {
		t.Errorf("unexpected ticket_transitions rows: %d (want 0 for non-success exit)", n)
	}
}

// assertHelloTxtMissing asserts workspace/hello.txt does not exist.
func assertHelloTxtMissing(t *testing.T, workspaceDir string) {
	t.Helper()
	if _, err := os.Stat(fmt.Sprintf("%s/hello.txt", workspaceDir)); !os.IsNotExist(err) {
		t.Errorf("hello.txt should not exist on failure path; stat err = %v", err)
	}
}

// TestM21UnknownMCPStatusFailsClosed — FR-108 fail-closed on unknown
// MCP status in the init event. Mock emits status="banana"; supervisor
// bails the process group and records exit_reason=mcp_postgres_banana.
func TestM21UnknownMCPStatusFailsClosed(t *testing.T) {
	s := startM21Scenario(t, "mcp-bail.ndjson", nil)
	row := waitForTerminalByTicket(t, s.Pool, s.TicketUUID, 20*time.Second)
	if row.Status != "failed" {
		t.Errorf("status = %q; want failed", row.Status)
	}
	if row.ExitReason == nil || *row.ExitReason != "mcp_postgres_banana" {
		t.Errorf("exit_reason = %v; want mcp_postgres_banana", row.ExitReason)
	}
	assertHelloTxtMissing(t, s.WorkspaceDir)
	assertNoTransition(t, s.Pool, s.TicketUUID)
}

// TestM21ParseErrorBails — FR-106: a malformed NDJSON line mid-stream is
// fatal; the supervisor bails the process group and records exit_reason
// =parse_error.
func TestM21ParseErrorBails(t *testing.T) {
	s := startM21Scenario(t, "parse-error.ndjson", nil)
	row := waitForTerminalByTicket(t, s.Pool, s.TicketUUID, 20*time.Second)
	if row.Status != "failed" {
		t.Errorf("status = %q; want failed", row.Status)
	}
	if row.ExitReason == nil || *row.ExitReason != "parse_error" {
		t.Errorf("exit_reason = %v; want parse_error", row.ExitReason)
	}
	assertHelloTxtMissing(t, s.WorkspaceDir)
	assertNoTransition(t, s.Pool, s.TicketUUID)
}

// TestM21NoResultEventFailsClosed — clarify Q3: a subprocess that exits
// 0 without ever emitting a result event is failed with exit_reason=
// no_result. Cost stays NULL; no transition.
func TestM21NoResultEventFailsClosed(t *testing.T) {
	s := startM21Scenario(t, "no-result.ndjson", nil)
	row := waitForTerminalByTicket(t, s.Pool, s.TicketUUID, 20*time.Second)
	if row.Status != "failed" {
		t.Errorf("status = %q; want failed", row.Status)
	}
	if row.ExitReason == nil || *row.ExitReason != "no_result" {
		t.Errorf("exit_reason = %v; want no_result", row.ExitReason)
	}
	if row.Cost != nil {
		t.Errorf("cost = %v; want NULL (no result event means no billing)", *row.Cost)
	}
	assertNoTransition(t, s.Pool, s.TicketUUID)
}

// TestM21ClaudeErrorResult — result event with is_error=true. Cost
// captured from the event; no transition.
func TestM21ClaudeErrorResult(t *testing.T) {
	s := startM21Scenario(t, "result-error.ndjson", nil)
	row := waitForTerminalByTicket(t, s.Pool, s.TicketUUID, 20*time.Second)
	if row.Status != "failed" {
		t.Errorf("status = %q; want failed", row.Status)
	}
	if row.ExitReason == nil || *row.ExitReason != "claude_error" {
		t.Errorf("exit_reason = %v; want claude_error", row.ExitReason)
	}
	if row.Cost == nil || normaliseCost(*row.Cost) != "0.004" {
		t.Errorf("cost = %v; want 0.004 (captured from the error result event)", row.Cost)
	}
	assertNoTransition(t, s.Pool, s.TicketUUID)
}

// TestM21AcceptanceFailedWhenHelloTxtMissing — successful init+result but
// no file written. Adjudicate falls to the acceptance branch.
func TestM21AcceptanceFailedWhenHelloTxtMissing(t *testing.T) {
	s := startM21Scenario(t, "hello-missing.ndjson", nil)
	row := waitForTerminalByTicket(t, s.Pool, s.TicketUUID, 20*time.Second)
	if row.Status != "failed" {
		t.Errorf("status = %q; want failed", row.Status)
	}
	if row.ExitReason == nil || *row.ExitReason != "acceptance_failed" {
		t.Errorf("exit_reason = %v; want acceptance_failed", row.ExitReason)
	}
	if row.Cost == nil || normaliseCost(*row.Cost) != "0.002" {
		t.Errorf("cost = %v; want 0.002 (captured from the success result event)", row.Cost)
	}
	assertHelloTxtMissing(t, s.WorkspaceDir)
	assertNoTransition(t, s.Pool, s.TicketUUID)
}

// TestM21AcceptanceFailedWhenHelloTxtContentsWrong — mock writes
// hello.txt with "oops" instead of the ticket id. Same terminal as the
// missing-file case: exit_reason=acceptance_failed.
func TestM21AcceptanceFailedWhenHelloTxtContentsWrong(t *testing.T) {
	s := startM21Scenario(t, "hello-wrong-contents.ndjson", nil)
	row := waitForTerminalByTicket(t, s.Pool, s.TicketUUID, 20*time.Second)
	if row.Status != "failed" {
		t.Errorf("status = %q; want failed", row.Status)
	}
	if row.ExitReason == nil || *row.ExitReason != "acceptance_failed" {
		t.Errorf("exit_reason = %v; want acceptance_failed", row.ExitReason)
	}
	// The file exists but with wrong contents — the fail-closed check
	// reads and compares; we don't re-assert the on-disk form beyond
	// that.
	b, _ := os.ReadFile(fmt.Sprintf("%s/hello.txt", s.WorkspaceDir))
	if string(b) != "oops" {
		t.Errorf("hello.txt contents = %q; want %q", string(b), "oops")
	}
	assertNoTransition(t, s.Pool, s.TicketUUID)
}

// TestM21SpawnFailedOnConfigWriteError — clarify Q2: when mcpconfig.Write
// fails the supervisor writes a terminal row with exit_reason=spawn_failed
// and keeps running. The second ticket (after the config dir is made
// writable again) runs normally.
func TestM21SpawnFailedOnConfigWriteError(t *testing.T) {
	pool := testdb.Start(t)
	workspaceDir := t.TempDir()
	mcpConfigDir := t.TempDir()
	// Make the config dir non-writable BEFORE the supervisor starts, so
	// its startup check succeeds (MkdirAll + write probe at config load
	// time — the probe runs with the test user's perms before we
	// chmod), then read-only'd for the supervisor's spawn-time write.
	// Startup writability is gated by config.Load's ensureWritableDir
	// which tolerates pre-existing directories; spawn-time writes fail
	// because the dir ends up chmodded 0500 below.
	deptID := testdb.SeedM21(t, workspaceDir)

	mockBin := buildMockClaudeBinary(t)
	scriptPath := mockClaudeScriptPath(t, "helloworld.ndjson")

	// Startup probe needs write perms; chmod happens AFTER startup.
	startSupervisor(t, supervisorOpts{
		ClaudeBin:        mockBin,
		AgentROPassword:  "integration-test-ro",
		MCPConfigDir:     mcpConfigDir,
		MockClaudeScript: scriptPath,
		PollInterval:     "1s",
		LogLevel:         "info",
	})
	// Revoke write permission so the supervisor's per-invocation
	// mcpconfig.Write fails with EACCES, triggering the spawn_failed
	// terminal path.
	if err := os.Chmod(mcpConfigDir, 0o500); err != nil {
		t.Fatalf("chmod mcp dir: %v", err)
	}

	firstTicket, err := store.New(pool).InsertTicket(context.Background(), store.InsertTicketParams{
		DepartmentID: deptID,
		Objective:    "spawn-failed scenario",
	})
	if err != nil {
		t.Fatalf("InsertTicket first: %v", err)
	}
	row := waitForTerminalByTicket(t, pool, firstTicket.ID, 20*time.Second)
	if row.Status != "failed" {
		t.Errorf("first status = %q; want failed", row.Status)
	}
	if row.ExitReason == nil || *row.ExitReason != "spawn_failed" {
		t.Errorf("first exit_reason = %v; want spawn_failed", row.ExitReason)
	}

	// Restore write permission so a follow-on ticket succeeds normally
	// — pins that the dispatcher kept running after the spawn-failed
	// terminal.
	if err := os.Chmod(mcpConfigDir, 0o755); err != nil {
		t.Fatalf("chmod mcp dir back: %v", err)
	}
	secondTicket, err := store.New(pool).InsertTicket(context.Background(), store.InsertTicketParams{
		DepartmentID: deptID,
		Objective:    "spawn-failed follow-on",
	})
	if err != nil {
		t.Fatalf("InsertTicket second: %v", err)
	}
	row2 := waitForTerminalByTicket(t, pool, secondTicket.ID, 20*time.Second)
	if row2.Status != "succeeded" {
		t.Errorf("second status = %q; want succeeded (dispatcher must continue)", row2.Status)
	}
	if row2.ExitReason == nil || *row2.ExitReason != "completed" {
		t.Errorf("second exit_reason = %v; want completed", row2.ExitReason)
	}
}
