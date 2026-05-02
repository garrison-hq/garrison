//go:build integration

package supervisor_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestM6RateLimitRejectedFiresPauseActuator is the M6 T009 rate-
// limit-pause integration test (deferred-from-M6.1 follow-up landing
// here).  It boots a real supervisor binary with mockclaude pointed
// at the rate-limit-rejected.ndjson fixture; mockclaude emits a
// rate_limit_event with status=rejected mid-stream so the
// supervisor's pipeline.OnRateLimit fires the M6 T008 actuator,
// which writes companies.pause_until + a throttle_events row +
// emits work.throttle.event in a short independent tx.
//
// Why integration scope (not //go:build chaos): the chaos suite
// scope is fault-injection (Postgres restart, SIGKILL, SIGTERM
// shutdown). This test exercises the live supervisor binary
// against a deterministic mockclaude script — same shape as the
// M2.1 hello-world end-to-end test.
//
// Concurrent-events sub-test deferred to M6.x: the unit-level
// last-write-wins semantics are pinned by TestRateLimitPauseFiresOn
// Rejected (in internal/spawn/pipeline_test.go) + the chained-tx
// audit shape by TestSpawnDeferredDuringRateLimitPause (in
// internal/throttle/integration_test.go); the live-binary
// concurrent-events case adds little incremental confidence over
// what those two tests cover.
func TestM6RateLimitRejectedFiresPauseActuator(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	// Wipe the M6 throttle_events table so the count assertion is
	// deterministic (testdb.Start truncates companies + departments
	// CASCADE on each test, which removes throttle_events
	// transitively, but be explicit).
	if _, err := pool.Exec(ctx, "TRUNCATE throttle_events RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate throttle_events: %v", err)
	}

	workspaceDir := t.TempDir()
	mcpConfigDir := t.TempDir()
	deptID := testdb.SeedM21(t, workspaceDir)

	// Resolve the company_id the M2.1 seed wired the dept to so we
	// can assert pause_until + the throttle_events row.
	dept, err := q.GetDepartmentByID(ctx, deptID)
	if err != nil {
		t.Fatalf("GetDepartmentByID: %v", err)
	}
	if !dept.CompanyID.Valid {
		t.Fatal("seed department has no company_id")
	}
	companyID := dept.CompanyID

	mockBin := buildMockClaudeBinary(t)
	scriptPath := mockClaudeScriptPath(t, "rate-limit-rejected.ndjson")

	startSupervisor(t, supervisorOpts{
		ClaudeBin:        mockBin,
		AgentROPassword:  "integration-test-ro",
		MCPConfigDir:     mcpConfigDir,
		MockClaudeScript: scriptPath,
		PollInterval:     "1s",
		LogLevel:         "info",
	})

	// Insert a ticket so the supervisor spawns. The mockclaude
	// fixture emits the rate_limit_event after one assistant text
	// block; the actuator fires before finalize lands.
	ticket, err := q.InsertTicket(ctx, store.InsertTicketParams{
		DepartmentID: deptID,
		Objective:    "rate-limit-rejected fixture",
	})
	if err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}
	_ = ticket

	// Subscribe to work.throttle.event so we can assert the live
	// notify hits.
	notifyConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire notify conn: %v", err)
	}
	defer notifyConn.Release()
	if _, err := notifyConn.Exec(ctx, `LISTEN "work.throttle.event"`); err != nil {
		t.Fatalf("LISTEN work.throttle.event: %v", err)
	}

	// Wait for the actuator to write the audit row. The mockclaude
	// fixture sleeps 200ms after the rate_limit_event before the
	// finalize line, so the actuator side-effects land within ~1s.
	deadline := time.Now().Add(15 * time.Second)
	var auditCount int
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM throttle_events WHERE company_id = $1 AND kind = 'rate_limit_pause'`,
			companyID,
		).Scan(&auditCount); err != nil {
			t.Fatalf("count throttle_events: %v", err)
		}
		if auditCount >= 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if auditCount < 1 {
		t.Fatal("rate-limit-pause throttle_events row never written")
	}

	// pause_until set + in the future.
	var pauseUntil pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT pause_until FROM companies WHERE id = $1`, companyID,
	).Scan(&pauseUntil); err != nil {
		t.Fatalf("read companies.pause_until: %v", err)
	}
	if !pauseUntil.Valid {
		t.Fatal("companies.pause_until is NULL after rate-limit-rejected event")
	}
	if !pauseUntil.Time.After(time.Now()) {
		t.Errorf("pause_until = %v should be in the future", pauseUntil.Time)
	}

	// LISTEN-side fake observed the notify (within the same window
	// the actuator's audit-row write committed).
	notifyCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	notif, err := notifyConn.Conn().WaitForNotification(notifyCtx)
	if err != nil {
		t.Fatalf("WaitForNotification: %v", err)
	}
	if !strings.Contains(notif.Payload, "rate_limit_pause") {
		t.Errorf("notify payload missing rate_limit_pause kind: %s", notif.Payload)
	}

	// In-flight spawn keeps running per FR-043: the fixture's
	// finalize lands AFTER the rate_limit_event so the spawn
	// terminates cleanly. We don't wait for terminal classification
	// here — the test's contract is the actuator side-effects, not
	// the spawn outcome (the existing M2.x tests cover that path).
}
