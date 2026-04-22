//go:build integration

package supervisor_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
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

	dept := mustInsertDepartment(t, q, "eng", 2)
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

	dept := mustInsertDepartment(t, q, "eng", 2)
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

	dept := mustInsertDepartment(t, q, "eng", 1)
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
		`INSERT INTO event_outbox (channel, payload) VALUES ('work.ticket.created', $1::jsonb) RETURNING id`,
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

	dept := mustInsertDepartment(t, q, "eng", 0)
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
	dept := mustInsertDepartment(t, q, "eng", 2)
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
		"ORG_OS_DATABASE_URL="+url,
		`ORG_OS_FAKE_AGENT_CMD=sh -c "echo ok"`,
		fmt.Sprintf("ORG_OS_HEALTH_PORT=%d", port),
		"ORG_OS_LOG_LEVEL=error",
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

	dept := mustInsertDepartment(t, q, "eng", 2)
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

	dept := mustInsertDepartment(t, q, "eng", 2)

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

// ---------- helpers ----------

// supervisorOpts bundles the common env overrides tests need. Any field
// left blank gets the supervisor's default.
type supervisorOpts struct {
	FakeAgentCmd      string
	PollInterval      string
	SubprocessTimeout string
	LogLevel          string
}

// startSupervisor builds the binary, execs it with a free /health port
// and the supplied options, waits for /health=200, and registers a
// t.Cleanup that SIGTERMs the process with a 5s escalation to SIGKILL.
// Returns the health port and the *exec.Cmd so tests that need to
// interact with the process (e.g. to signal it explicitly) can.
func startSupervisor(t *testing.T, opts supervisorOpts) (int, *exec.Cmd) {
	t.Helper()
	url := testdb.URL(t)
	bin := buildSupervisorBinary(t)
	port := mustFreePort(t)

	env := append(os.Environ(),
		"ORG_OS_DATABASE_URL="+url,
		"ORG_OS_FAKE_AGENT_CMD="+opts.FakeAgentCmd,
		fmt.Sprintf("ORG_OS_HEALTH_PORT=%d", port),
	)
	if opts.PollInterval != "" {
		env = append(env, "ORG_OS_POLL_INTERVAL="+opts.PollInterval)
	}
	if opts.SubprocessTimeout != "" {
		env = append(env, "ORG_OS_SUBPROCESS_TIMEOUT="+opts.SubprocessTimeout)
	}
	if opts.LogLevel != "" {
		env = append(env, "ORG_OS_LOG_LEVEL="+opts.LogLevel)
	}

	cmd := exec.Command(bin)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start supervisor: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})

	if err := waitForHealth(port, 15*time.Second); err != nil {
		t.Fatalf("supervisor never became healthy: %v", err)
	}
	return port, cmd
}

func buildSupervisorBinary(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	supervisorDir := filepath.Dir(thisFile)
	out := filepath.Join(t.TempDir(), "supervisor")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/supervisor")
	cmd.Dir = supervisorDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build supervisor: %v", err)
	}
	return out
}

func mustFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func waitForHealth(port int, within time.Duration) error {
	deadline := time.Now().Add(within)
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			if statusOK(url) {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", url)
}

func statusOK(url string) bool {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

func mustInsertDepartment(t *testing.T, q *store.Queries, slug string, cap int32) store.Department {
	t.Helper()
	dept, err := q.InsertDepartment(context.Background(), store.InsertDepartmentParams{
		Slug: slug, Name: slug, ConcurrencyCap: cap,
	})
	if err != nil {
		t.Fatalf("InsertDepartment: %v", err)
	}
	return dept
}

// waitForTerminalCount polls until COUNT(agent_instances WHERE status = wantStatus) reaches want,
// or the deadline elapses.
func waitForTerminalCount(t *testing.T, pool *pgxpool.Pool, want int64, within time.Duration, wantStatus string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(within)
	var got int64
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM agent_instances WHERE status = $1`, wantStatus,
		).Scan(&got); err == nil && got >= want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d rows with status=%s; got %d", want, wantStatus, got)
}

// sampleMaxRunning polls COUNT(status='running') every 50ms for the given
// duration and returns the observed peak. Used by cap-enforcement tests to
// assert the supervisor never runs more than N simultaneous children.
func sampleMaxRunning(t *testing.T, pool *pgxpool.Pool, within time.Duration) int64 {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(within)
	var peak int64
	for time.Now().Before(deadline) {
		var n int64
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM agent_instances WHERE status='running'`,
		).Scan(&n); err == nil && n > peak {
			peak = n
		}
		time.Sleep(50 * time.Millisecond)
	}
	return peak
}
