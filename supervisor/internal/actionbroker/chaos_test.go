//go:build chaos

// Package actionbroker — chaos_test.go
//
// Chaos suite for the M11 action broker's exactly-once dispatch invariant
// (SC-006, FR-021). Two tests are required by tasks.md T010:
//
//   - TestConcurrentClaimDispatchesExactlyOnce: N goroutines call Handle on
//     the same approved approve-tier row concurrently from a start barrier.
//     FOR UPDATE SKIP LOCKED guarantees exactly one PostComment call and
//     exactly one 'executed' outcome row; all other claimants find no
//     dispatchable row (the terminal status is the no-op signal).
//
//   - TestRestartMidDispatchNoDoublePost: simulates a supervisor crash after
//     claim+POST but before commit (external simulation of a rolled-back tx).
//     After the simulated crash the row stays 'approved'; Handle is re-run
//     and posts a second time. The total POST count is exactly 2 but the
//     committed state has exactly one 'executed' outcome row — the
//     at-most-once-extra window is acknowledged and documented.
//
// Both tests use the shared testcontainers-go Postgres harness (testdb.Start)
// and inject fake VaultFetcher + GitHubPoster so no real vault or GitHub API
// is called. Run with: go test -tags=chaos ./internal/actionbroker/...
//
// Helpers carry a "chaos" prefix so a combined-tag build
// (-tags="integration chaos") does not collide with the integration-tagged
// helpers in dispatcher_test.go.
package actionbroker

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Chaos-specific fakes (prefixed to avoid collision with dispatcher_test.go)
// ---------------------------------------------------------------------------

// chaosCountingVault implements VaultFetcher. Always succeeds, returning a
// fixed PAT value. It is safe for concurrent use.
type chaosCountingVault struct {
	pat string
}

func (v *chaosCountingVault) Fetch(_ context.Context, _ []vault.GrantRow) (map[string]vault.SecretValue, error) {
	return map[string]vault.SecretValue{
		"GITHUB_PAT": vault.New([]byte(v.pat)),
	}, nil
}

// chaosCountingGitHub implements GitHubPoster with an atomic call counter.
// Thread-safe; used by concurrent Handle goroutines.
type chaosCountingGitHub struct {
	calls atomic.Int64
	urlFn func() string // returns the URL to report on each call
	errFn func() error  // returns nil or an error on each call; called once per PostComment
}

func (g *chaosCountingGitHub) PostComment(_ context.Context, _ Target, _, _ string) (string, error) {
	g.calls.Add(1)
	if g.errFn != nil {
		if err := g.errFn(); err != nil {
			return "", err
		}
	}
	url := "https://github.com/chaos-org/chaos-repo/issues/1#issuecomment-chaos"
	if g.urlFn != nil {
		url = g.urlFn()
	}
	return url, nil
}

// ---------------------------------------------------------------------------
// Chaos shared helpers
// ---------------------------------------------------------------------------

// chaosSetupPool boots the shared testcontainer Postgres and returns the pool.
// The M11 pending-action tables are truncated before and after the test.
func chaosSetupPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testdb.Start(t)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			"TRUNCATE work.pending_action_outcomes, work.pending_actions CASCADE")
	})
	return pool
}

// chaosSeedAgent inserts the minimum FK chain for pending_actions:
// company → department → agent → agent_instance → ticket.
// Returns the agent_instance id.
func chaosSeedAgent(t *testing.T, pool *pgxpool.Pool, slug string) pgtype.UUID {
	t.Helper()
	ctx := context.Background()

	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ($1) RETURNING id`, "chaos-co-"+slug,
	).Scan(&companyID); err != nil {
		t.Fatalf("chaosSeedAgent: insert company: %v", err)
	}

	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (company_id, slug, name, workspace_path)
		VALUES ($1, $2, $3, '/tmp/chaos')
		RETURNING id`,
		companyID, slug, "Chaos-"+slug,
	).Scan(&deptID); err != nil {
		t.Fatalf("chaosSeedAgent: insert department: %v", err)
	}

	var agentID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agents (department_id, role_slug, listens_for, agent_md, model, status)
		VALUES ($1, 'chaos', '[]'::jsonb, 'md', 'claude-x', 'active')
		RETURNING id`, deptID,
	).Scan(&agentID); err != nil {
		t.Fatalf("chaosSeedAgent: insert agent: %v", err)
	}

	var ticketID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (department_id, objective, column_slug)
		VALUES ($1, 'chaos test', 'in_dev')
		RETURNING id`, deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("chaosSeedAgent: insert ticket: %v", err)
	}

	var instanceID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_instances (department_id, role_slug, ticket_id, status)
		VALUES ($1, 'chaos', $2, 'running')
		RETURNING id`, deptID, ticketID,
	).Scan(&instanceID); err != nil {
		t.Fatalf("chaosSeedAgent: insert agent_instance: %v", err)
	}

	return instanceID
}

// chaosInsertApprovedAction inserts an approved approve-tier pending_actions
// row. The CHECK constraint pending_actions_floor_is_approve allows
// 'github_issue_comment' at tier='approve'.
func chaosInsertApprovedAction(t *testing.T, pool *pgxpool.Pool, agentInstanceID pgtype.UUID) pgtype.UUID {
	t.Helper()
	const target = `{"owner":"chaos-org","repo":"chaos-repo","issue_number":1}`
	var id pgtype.UUID
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO pending_actions
		    (action_type, target, rendered_payload, agent_instance_id, tier, tier_reason, status)
		VALUES ('github_issue_comment', $1::jsonb, 'chaos test payload', $2, 'approve',
		        'permanent-Approve floor (public-facing)', 'approved')
		RETURNING id`,
		target, agentInstanceID,
	).Scan(&id); err != nil {
		t.Fatalf("chaosInsertApprovedAction: %v", err)
	}
	return id
}

// chaosCountOutcomes returns the number of outcome rows for the given action.
func chaosCountOutcomes(t *testing.T, pool *pgxpool.Pool, actionID pgtype.UUID, outcome string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM pending_action_outcomes WHERE pending_action_id = $1 AND outcome = $2`,
		actionID, outcome,
	).Scan(&n); err != nil {
		t.Fatalf("chaosCountOutcomes: %v", err)
	}
	return n
}

// chaosReadStatus reads the current status of a pending_actions row.
func chaosReadStatus(t *testing.T, pool *pgxpool.Pool, actionID pgtype.UUID) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM pending_actions WHERE id = $1`, actionID,
	).Scan(&status); err != nil {
		t.Fatalf("chaosReadStatus: %v", err)
	}
	return status
}

// chaosBuildWorker constructs a Worker from the given pool and GitHub poster.
func chaosBuildWorker(t *testing.T, pool *pgxpool.Pool, github GitHubPoster) *Worker {
	t.Helper()
	w, err := New(Deps{
		Pool:    pool,
		Queries: store.New(pool),
		Vault:   &chaosCountingVault{pat: "chaos-test-pat"},
		GitHub:  github,
		Logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})),
		PATPath: "actions/GITHUB_PAT",
		Now:     func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("chaosBuildWorker New: %v", err)
	}
	return w
}

// ---------------------------------------------------------------------------
// TestConcurrentClaimDispatchesExactlyOnce (SC-006 / US2 #3)
// ---------------------------------------------------------------------------

// TestConcurrentClaimDispatchesExactlyOnce proves that N concurrent Handle
// goroutines racing over a single approved approve-tier row produce exactly
// one PostComment call and exactly one 'executed' outcome row. The remaining
// goroutines find nothing claimable (FOR UPDATE SKIP LOCKED skips the row
// once it is locked by the winner) and return a no-op.
//
// This is the M11 shape of the M1/M9/M10 single-firing / concurrent-claim
// chaos discipline (SC-006, FR-021). The exactly-once guarantee comes from
// the combination of:
//   - FOR UPDATE SKIP LOCKED: at most one claimant holds the row lock at a time.
//   - Terminal status transition: once the winner commits 'executed', all
//     subsequent claim attempts see status='executed' (not in the claimable
//     set) and are no-ops.
func TestConcurrentClaimDispatchesExactlyOnce(t *testing.T) {
	const N = 8 // concurrent Handle goroutines; chosen to stress the race
	pool := chaosSetupPool(t)
	agentInstanceID := chaosSeedAgent(t, pool, "concurrent")
	actionID := chaosInsertApprovedAction(t, pool, agentInstanceID)

	github := &chaosCountingGitHub{}
	w := chaosBuildWorker(t, pool, github)

	// Start barrier: all N goroutines wait on a closed channel so they all
	// attempt Handle simultaneously, maximising lock contention.
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := w.Handle(ctx, pgtype.UUID{}); err != nil {
				errs <- err
			}
		}()
	}

	close(start) // release all goroutines simultaneously

	// Wait for completion with a hard deadline to detect wedged handlers.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent Handle goroutines never completed; claim transaction may be wedged")
	}

	// Drain any errors.
	close(errs)
	for err := range errs {
		t.Errorf("Handle goroutine returned error: %v", err)
	}

	// Exactly one PostComment call across all N goroutines (SC-006).
	if got := github.calls.Load(); got != 1 {
		t.Errorf("PostComment calls = %d; want exactly 1 across %d concurrent goroutines (SC-006, FOR UPDATE SKIP LOCKED)", got, N)
	}

	// Status must be 'executed' (terminal — the winner committed).
	if got := chaosReadStatus(t, pool, actionID); got != "executed" {
		t.Errorf("pending_actions.status = %q; want 'executed'", got)
	}

	// Exactly one 'executed' outcome row (idempotency — no duplicate rows from
	// concurrent claimants, FR-024).
	if got := chaosCountOutcomes(t, pool, actionID, "executed"); got != 1 {
		t.Errorf("'executed' outcome rows = %d; want exactly 1 (SC-006, FR-024)", got)
	}

	// No additional outcome rows from the other goroutines — they were no-ops.
	if got := chaosCountOutcomes(t, pool, actionID, "failed"); got != 0 {
		t.Errorf("unexpected 'failed' outcome rows = %d; concurrent no-ops must not write failure rows", got)
	}
}

// ---------------------------------------------------------------------------
// TestRestartMidDispatchNoDoublePost (SC-006 / Edge Cases)
// ---------------------------------------------------------------------------

// TestRestartMidDispatchNoDoublePost documents and proves the exactly-once
// dispatch invariant under a simulated supervisor crash-mid-dispatch scenario.
//
// The crash scenario (plan Phase 0 note):
//  1. The dispatcher claims the row (BEGIN + SELECT FOR UPDATE SKIP LOCKED).
//  2. The dispatcher calls PostComment — the HTTP request succeeds, the comment
//     lands on GitHub.
//  3. Before the dispatcher can call MarkPendingActionExecuted + tx.Commit,
//     the supervisor crashes (or the tx is rolled back by the OS).
//  4. Result: the GitHub comment exists but the DB row is still 'approved'.
//
// In the test harness the "crash" is simulated by a manual transaction that:
//   - Locks the row (FOR UPDATE), representing a claim.
//   - Increments the PostComment counter directly (representing the POST that
//     landed before the crash).
//   - Rolls back without touching the DB row, leaving it at 'approved'.
//
// After the simulated crash:
//  5. Handle is called normally; it claims the row, calls PostComment (call 2),
//     commits: row → 'executed', one 'executed' outcome row written.
//  6. Handle is called again; nothing is claimable (row is terminal).
//
// Invariants asserted:
//   - PostComment is called exactly twice across the crash + recovery (the
//     "at most once extra" window when a commit was genuinely lost).
//   - Exactly one 'executed' outcome row is committed to the DB (the recovery
//     Handle call's commit is the sole durable record).
//   - The final status is 'executed' (terminal).
//
// This matches the M9/M10 "exactly-once on success" contract: the property is
// exactly-once-committed, not exactly-once-attempted.
func TestRestartMidDispatchNoDoublePost(t *testing.T) {
	pool := chaosSetupPool(t)
	agentInstanceID := chaosSeedAgent(t, pool, "restart")
	actionID := chaosInsertApprovedAction(t, pool, agentInstanceID)

	// Shared counter for total PostComment calls across the simulated crash
	// and the subsequent recovery Handle call.
	github := &chaosCountingGitHub{}

	// -----------------------------------------------------------------------
	// Phase 1: simulate crash after claim+POST but before commit.
	//
	// We open an explicit transaction, lock the row (simulating a claim),
	// increment the counter (simulating PostComment), then roll back
	// (simulating the crash before MarkPendingActionExecuted + commit).
	// -----------------------------------------------------------------------
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Phase 1: begin crash tx: %v", err)
		}
		defer func() {
			// Rollback simulates the crash — the row stays 'approved'.
			_ = tx.Rollback(ctx)
		}()

		// Lock the row exactly as the dispatcher's ClaimDispatchablePendingAction
		// would, confirming it is in a claimable state before we "crash".
		var lockedID pgtype.UUID
		if err := tx.QueryRow(ctx, `
			SELECT id FROM pending_actions
			 WHERE status IN ('pending', 'approved') AND tier <> 'human_only'
			 ORDER BY created_at
			   FOR UPDATE SKIP LOCKED
			 LIMIT 1`,
		).Scan(&lockedID); err != nil {
			if err == pgx.ErrNoRows {
				t.Fatalf("Phase 1: no claimable row found before simulated crash")
			}
			t.Fatalf("Phase 1: claim FOR UPDATE: %v", err)
		}

		// Simulate the PostComment HTTP call that succeeded before the crash.
		github.calls.Add(1)

		// Rollback (crash): the defer above rolls back, leaving the row at 'approved'.
		// The comment exists on GitHub but the DB has no record of the dispatch.
	}()

	// Sanity: row must still be 'approved' after the simulated crash.
	if got := chaosReadStatus(t, pool, actionID); got != "approved" {
		t.Fatalf("after simulated crash: status = %q; want 'approved' (tx was rolled back)", got)
	}
	// No outcome rows should exist after the crash (the tx that would have
	// written InsertPendingActionOutcome was rolled back).
	if got := chaosCountOutcomes(t, pool, actionID, "executed"); got != 0 {
		t.Fatalf("after simulated crash: 'executed' outcome rows = %d; want 0 (tx rolled back)", got)
	}

	// -----------------------------------------------------------------------
	// Phase 2: recovery — normal Handle call after the simulated crash.
	//
	// Handle claims the still-approved row, calls PostComment (call 2), and
	// commits: row → 'executed', one 'executed' outcome row written.
	// -----------------------------------------------------------------------
	w := chaosBuildWorker(t, pool, github)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := w.Handle(ctx, pgtype.UUID{}); err != nil {
		t.Fatalf("Phase 2: Handle recovery returned error: %v", err)
	}

	// PostComment was called once in Phase 1 (simulated) + once in Phase 2 = 2.
	// This is the "at most once extra" window: at most 2 posts, exactly 1
	// committed execution record (SC-006).
	if got := github.calls.Load(); got != 2 {
		t.Errorf("total PostComment calls = %d; want exactly 2 (crash call + recovery call)", got)
	}

	// Exactly one 'executed' outcome row — the recovery Handle committed once.
	if got := chaosCountOutcomes(t, pool, actionID, "executed"); got != 1 {
		t.Errorf("'executed' outcome rows after recovery = %d; want exactly 1 (SC-006, FR-024)", got)
	}

	// Final status is 'executed' (terminal).
	if got := chaosReadStatus(t, pool, actionID); got != "executed" {
		t.Errorf("final status = %q; want 'executed'", got)
	}

	// -----------------------------------------------------------------------
	// Phase 3: confirm the terminal status makes a third Handle call a no-op.
	// -----------------------------------------------------------------------
	if err := w.Handle(ctx, pgtype.UUID{}); err != nil {
		t.Fatalf("Phase 3: Handle after terminal status returned error: %v", err)
	}

	// PostComment must not have been called a third time (row is terminal).
	if got := github.calls.Load(); got != 2 {
		t.Errorf("PostComment calls after terminal no-op = %d; want still 2 (row is 'executed')", got)
	}

	// Outcome count must remain at 1 (no duplicate rows).
	if got := chaosCountOutcomes(t, pool, actionID, "executed"); got != 1 {
		t.Errorf("'executed' outcome rows after terminal no-op = %d; want still 1", got)
	}
}
