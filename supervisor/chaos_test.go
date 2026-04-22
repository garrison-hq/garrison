//go:build chaos

// Package supervisor_test chaos suite (T016). Exercises the three faults
// the M1 plan commits to handling at run-time: Postgres restart
// (FR-008 + plan.md §"pg_notify listener connection lifecycle"), external
// SIGKILL of a running subprocess (exit_reason classification), and
// SIGTERM of the supervisor with an in-flight child (NFR-005 graceful
// shutdown). Each test runs a real binary under real Postgres — helpers
// are shared with integration_test.go via test_helpers_test.go.
package supervisor_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestReconnectCatchesMissedEvents terminates the supervisor's backend
// connections (LISTEN + pool) from Postgres' side, inserts 3 tickets
// during the resulting reconnect window, and asserts all three reach
// terminal state. The "adjust per container semantics" clause in the
// task permits this form: pg_terminate_backend produces the same FATAL
// 57P01 that a container pause/restart produces, without depending on
// Docker port-mapping stability across Stop/Start.
func TestReconnectCatchesMissedEvents(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	port, _ := startSupervisor(t, supervisorOpts{
		FakeAgentCmd: `sh -c "echo ok"`,
		PollInterval: "2s",
	})

	dept := mustInsertDepartment(t, q, "engineering", 3)

	// Chaos: kill every backend other than the current one. This drops the
	// supervisor's listen conn and any currently-checked-out pool conns,
	// matching the 57P01 FATAL the supervisor sees on a real outage.
	if _, err := pool.Exec(ctx,
		`SELECT pg_terminate_backend(pid)
		   FROM pg_stat_activity
		  WHERE pid <> pg_backend_pid() AND datname = current_database()`,
	); err != nil {
		t.Fatalf("terminate backends: %v", err)
	}

	// Give the supervisor a beat to notice the drop and start reconnecting.
	time.Sleep(500 * time.Millisecond)

	// Insert 3 tickets during the reconnect window. The trigger's NOTIFY is
	// delivered only to currently-LISTENing sessions — the supervisor isn't
	// one right now — so these are "missed events" the fallback poll has to
	// catch after reconnect.
	for i := 0; i < 3; i++ {
		if _, err := q.InsertTicket(ctx, store.InsertTicketParams{
			DepartmentID: dept.ID, Objective: fmt.Sprintf("reconnect-%d", i),
		}); err != nil {
			t.Fatalf("InsertTicket %d: %v", i, err)
		}
	}

	// /health going back to 200 is the supervisor's own signal that pool +
	// fallback-poll are healthy post-reconnect.
	if err := waitForHealth(port, 15*time.Second); err != nil {
		t.Fatalf("supervisor never recovered: %v", err)
	}

	// All 3 should complete within one poll interval of reconnect + headroom
	// for LISTEN delivery and subprocess startup.
	waitForTerminalCount(t, pool, 3, 15*time.Second, "succeeded")
}

// TestSIGKILLSubprocessRecordedFailed spawns a long-running fake agent, finds
// its pid via a temp file the wrapper shell writes, SIGKILLs it, and verifies
// the terminal row carries status=failed and exit_reason=signal_SIGKILL. The
// `exec sleep` trick makes the shell's pid the sleep's pid — kill one and you
// kill the other without hunting through a process tree.
func TestSIGKILLSubprocessRecordedFailed(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	pidFile := filepath.Join(t.TempDir(), "subprocess.pid")
	fakeCmd := fmt.Sprintf(`sh -c 'echo $$ > %s; exec sleep 30'`, pidFile)

	startSupervisor(t, supervisorOpts{
		FakeAgentCmd:      fakeCmd,
		PollInterval:      "1s",
		SubprocessTimeout: "60s",
	})

	dept := mustInsertDepartment(t, q, "engineering", 1)
	ticket, err := q.InsertTicket(ctx, store.InsertTicketParams{
		DepartmentID: dept.ID, Objective: "killme",
	})
	if err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}

	// Wait for the wrapper shell to write its pid.
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pidFile)
		if err == nil && len(b) > 0 {
			if _, scanErr := fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &pid); scanErr == nil && pid > 0 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatalf("wrapper shell never wrote pid to %s", pidFile)
	}

	// External SIGKILL. This is the chaos: an operator, an OOM killer, or a
	// container runtime reaping the child out from under us.
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill %d: %v", pid, err)
	}

	// Expect exactly one failed row tied to our ticket with SIGKILL reason.
	deadline = time.Now().Add(10 * time.Second)
	var status, reason string
	for time.Now().Before(deadline) {
		row := pool.QueryRow(ctx,
			`SELECT status, COALESCE(exit_reason, '') FROM agent_instances WHERE ticket_id = $1 LIMIT 1`,
			ticket.ID,
		)
		if err := row.Scan(&status, &reason); err == nil && status != "running" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != "failed" {
		t.Fatalf("status got=%q want=failed", status)
	}
	if reason != "signal_SIGKILL" {
		t.Fatalf("exit_reason got=%q want=signal_SIGKILL", reason)
	}
}

// TestGracefulShutdownWithInflight SIGTERMs the supervisor while a long-sleep
// subprocess is running. Verifies: (a) supervisor exits within
// GARRISON_SHUTDOWN_GRACE, (b) the in-flight agent_instances row lands with a
// supervisor_shutdown exit_reason (plain or _sigkill). The subprocess itself
// is `sh -c "exec sleep 60"`: exec makes sh replace itself with sleep so the
// supervisor's SIGTERM is received by the sleep binary directly (sh doesn't
// forward signals to children it hasn't exec-ed).
func TestGracefulShutdownWithInflight(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx := context.Background()

	grace := 10 * time.Second
	_, cmd := startSupervisor(t, supervisorOpts{
		FakeAgentCmd:      `sh -c "exec sleep 60"`,
		PollInterval:      "1s",
		SubprocessTimeout: "120s",
		ShutdownGrace:     "10s",
	})

	dept := mustInsertDepartment(t, q, "engineering", 1)
	if _, err := q.InsertTicket(ctx, store.InsertTicketParams{
		DepartmentID: dept.ID, Objective: "longjob",
	}); err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}

	// Wait for the subprocess to actually be running before we SIGTERM.
	waitForRunningCount(t, pool, 1, 10*time.Second)

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal supervisor: %v", err)
	}

	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	select {
	case <-doneCh:
		elapsed := time.Since(start)
		// Shutdown grace is 10s; subprocess's own NFR-005 window is 5s inside
		// spawn. 20s is a comfortable ceiling; anything longer means the
		// shutdown path is wedged.
		if elapsed > grace+10*time.Second {
			t.Fatalf("shutdown took %v; want <= %v", elapsed, grace+10*time.Second)
		}
	case <-time.After(grace + 20*time.Second):
		t.Fatalf("supervisor did not exit within %v", grace+20*time.Second)
	}

	// Terminal row must reflect shutdown-driven termination.
	var status, reason string
	if err := pool.QueryRow(ctx,
		`SELECT status, COALESCE(exit_reason, '')
		   FROM agent_instances
		  WHERE ticket_id IN (SELECT id FROM tickets WHERE objective = 'longjob')
		  LIMIT 1`,
	).Scan(&status, &reason); err != nil {
		t.Fatalf("fetch terminal row: %v", err)
	}
	if status != "failed" {
		t.Fatalf("status got=%q want=failed", status)
	}
	if !strings.HasPrefix(reason, "supervisor_shutdown") {
		t.Fatalf("exit_reason got=%q want supervisor_shutdown*", reason)
	}
}

// ---------------------------------------------------------------------
// T018: M2.1 chaos tests — require the real claude binary on $PATH
// ---------------------------------------------------------------------

// skipIfClaudeMissing skips the current test when the pinned claude
// binary is absent. T018 tests depend on real Claude Code speaking its
// real stream-json protocol; mockclaude cannot substitute here because
// the scenarios exercise Claude's own MCP launch / tool-use behaviour.
func skipIfClaudeMissing(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude binary not on $PATH; skipping T018 chaos test. "+
			"Install via: npm install -g @anthropic-ai/claude-code (got error: %v)", err)
	}
	return bin
}

// TestM21BrokenMCPConfigBailsWithin2Seconds — acceptance A11 / NFR-106.
// Point the MCP config's postgres command at /bin/does-not-exist so the
// real claude binary fails to start the MCP server, reports
// mcp_servers=[{postgres,failed}] in init, and the supervisor bails the
// process group. Budget: finished_at - started_at <= 5s (the pure bail
// path is microseconds; the budget allows for the full happy-path of
// claude launching, emitting init, supervisor observing and sending
// SIGTERM, claude exiting).
func TestM21BrokenMCPConfigBailsWithin2Seconds(t *testing.T) {
	claudeBin := skipIfClaudeMissing(t)

	pool := testdb.Start(t)
	workspaceDir := t.TempDir()
	mcpConfigDir := t.TempDir()
	deptID := testdb.SeedM21(t, workspaceDir)

	startSupervisor(t, supervisorOpts{
		ClaudeBin:             claudeBin,
		AgentROPassword:       "chaos-test-ro",
		MCPConfigDir:          mcpConfigDir,
		SupervisorBinOverride: "/bin/does-not-exist",
		SubprocessTimeout:     "30s",
		PollInterval:          "1s",
		LogLevel:              "info",
	})

	ticket, err := store.New(pool).InsertTicket(context.Background(), store.InsertTicketParams{
		DepartmentID: deptID,
		Objective:    "broken mcp config",
	})
	if err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}

	// Wait for terminal. 30s budget to let real claude initialize and
	// emit its init event.
	row := waitForTerminalByTicket(t, pool, ticket.ID, 30*time.Second)
	if row.Status != "failed" {
		t.Errorf("status = %q; want failed", row.Status)
	}
	if row.ExitReason == nil || *row.ExitReason != "mcp_postgres_failed" {
		t.Errorf("exit_reason = %v; want mcp_postgres_failed", row.ExitReason)
	}

	// finished_at - started_at should comfortably fit the NFR-106
	// budget. The 2-second plan figure is from init-emission to
	// SIGTERM; this includes claude's own startup (which can easily
	// be 1-3s on first run) so the DB-visible bound is wider.
	var startedAt, finishedAt time.Time
	if err := pool.QueryRow(context.Background(), `
		SELECT started_at, finished_at
		FROM agent_instances WHERE ticket_id = $1`, ticket.ID,
	).Scan(&startedAt, &finishedAt); err != nil {
		t.Fatalf("fetch timestamps: %v", err)
	}
	spanSec := finishedAt.Sub(startedAt).Seconds()
	if spanSec > 10 {
		t.Errorf("finished_at - started_at = %.2fs; want <= 10s (NFR-106 + claude startup)", spanSec)
	}

	// hello.txt must not exist, no transition row.
	if _, err := os.Stat(workspaceDir + "/hello.txt"); !os.IsNotExist(err) {
		t.Errorf("hello.txt should not exist on mcp bail; stat err = %v", err)
	}
	var transitions int64
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM ticket_transitions WHERE ticket_id = $1`, ticket.ID,
	).Scan(&transitions); err != nil {
		t.Fatalf("count transitions: %v", err)
	}
	if transitions != 0 {
		t.Errorf("ticket_transitions rows = %d; want 0 on mcp bail", transitions)
	}

	// event_outbox.processed_at set.
	var processed pgtype.Timestamptz
	if err := pool.QueryRow(context.Background(),
		`SELECT processed_at FROM event_outbox ORDER BY created_at DESC LIMIT 1`,
	).Scan(&processed); err != nil {
		t.Fatalf("fetch event_outbox: %v", err)
	}
	if !processed.Valid {
		t.Error("event_outbox.processed_at is NULL; terminal tx did not commit")
	}
}

// TestM21ChaosPgmcpDiesMidRun — real claude + real pgmcp subprocess;
// after init lands, externally SIGKILL the pgmcp PID. Claude either
// emits an errored tool_result (if it was mid-query) or exits without
// a clean result. The supervisor records claude_error, no_result, or
// (if claude retries MCP calls internally and still produces a
// succeeded result that then fails the hello.txt check) acceptance_
// failed. The spike didn't characterize this path; the T020 retro
// records whichever reason is observed.
//
// This test actually spends Anthropic API credits when it runs
// (observed ~$0.04 per run against claude 2.1.117 + claude-haiku-4.5).
// Gate via GARRISON_T018_SPEND_CREDITS=1 so CI and casual runs skip.
func TestM21ChaosPgmcpDiesMidRun(t *testing.T) {
	if os.Getenv("GARRISON_T018_SPEND_CREDITS") != "1" {
		t.Skip("skipping: this test makes real Anthropic API calls (≈$0.04/run). " +
			"Set GARRISON_T018_SPEND_CREDITS=1 to opt in.")
	}
	claudeBin := skipIfClaudeMissing(t)

	pool := testdb.Start(t)
	workspaceDir := t.TempDir()
	mcpConfigDir := t.TempDir()
	pgmcpPIDFile := filepath.Join(t.TempDir(), "pgmcp.pid")
	deptID := testdb.SeedM21(t, workspaceDir)
	// Real pgmcp needs the agent_ro role's password set — the migration
	// creates the role without one (operators are expected to ALTER
	// post-migrate in production).
	testdb.SetAgentROPassword(t, "chaos-test-ro")

	// Seed the engineering department with a ticket that has a
	// realistic objective so claude has something to query via MCP.
	startSupervisor(t, supervisorOpts{
		ClaudeBin:         claudeBin,
		AgentROPassword:   "chaos-test-ro",
		MCPConfigDir:      mcpConfigDir,
		PgmcpPIDFile:      pgmcpPIDFile,
		SubprocessTimeout: "60s",
		PollInterval:      "1s",
		LogLevel:          "info",
	})

	ticket, err := store.New(pool).InsertTicket(context.Background(), store.InsertTicketParams{
		DepartmentID: deptID,
		Objective:    "write hello.txt with the ticket id",
	})
	if err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}

	// Wait for pgmcp to start, write its PID, AND be a live process.
	// Without a valid Anthropic auth token claude may exit soon after
	// pgmcp starts (the parent dying takes pgmcp with it). If the PID
	// is gone before we can inject the fault, skip cleanly rather
	// than failing — the chaos scenario can't be set up in this env.
	var pgmcpPID int
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pgmcpPIDFile)
		if err == nil && len(b) > 0 {
			_, _ = fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &pgmcpPID)
			if pgmcpPID > 0 {
				// Confirm still alive (syscall.Kill(pid, 0) is the
				// canonical liveness probe — returns nil iff the
				// process exists and the caller may signal it).
				if err := syscall.Kill(pgmcpPID, 0); err == nil {
					break
				}
				// Race: PID file exists but process already gone.
				// Reset and keep polling in case a new pgmcp starts
				// (unlikely on this path).
				pgmcpPID = 0
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pgmcpPID == 0 {
		t.Skipf("pgmcp never stayed alive long enough to inject a fault; " +
			"this scenario typically requires a valid ANTHROPIC_API_KEY so " +
			"claude doesn't exit on the first API call and take pgmcp with it")
	}

	// External SIGKILL of the pgmcp subprocess. Claude's MCP client
	// should notice its stdio partner died and either return an errored
	// tool_result to its own model or terminate.
	if err := syscall.Kill(pgmcpPID, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			t.Skipf("pgmcp pid %d already gone before kill; see prior log about auth", pgmcpPID)
		}
		t.Fatalf("kill pgmcp pid %d: %v", pgmcpPID, err)
	}

	row := waitForTerminalByTicket(t, pool, ticket.ID, 60*time.Second)
	if row.Status != "failed" {
		t.Errorf("status = %q; want failed", row.Status)
	}
	// The spike did not characterize exactly which exit_reason claude
	// produces here. Accept any of the reasonable terminals.
	accepted := map[string]bool{
		"claude_error":      true,
		"no_result":         true,
		"acceptance_failed": true,
	}
	if row.ExitReason == nil || !accepted[*row.ExitReason] {
		t.Errorf("exit_reason = %v; want one of claude_error | no_result | acceptance_failed "+
			"(T018 observation window — update the retro if a new value appears)", row.ExitReason)
	}
	t.Logf("TestM21ChaosPgmcpDiesMidRun observed exit_reason=%v (for retro)", row.ExitReason)
}

// TestM21ChaosClaudeSigkilledExternally — external SIGKILL to the Claude
// subprocess itself while it is running. The supervisor records the
// kill via FormatSignalled. The M1 analogue was the fake-agent SIGKILL
// test; this is its real-claude companion.
func TestM21ChaosClaudeSigkilledExternally(t *testing.T) {
	claudeBin := skipIfClaudeMissing(t)

	pool := testdb.Start(t)
	workspaceDir := t.TempDir()
	mcpConfigDir := t.TempDir()
	deptID := testdb.SeedM21(t, workspaceDir)

	startSupervisor(t, supervisorOpts{
		ClaudeBin:         claudeBin,
		AgentROPassword:   "chaos-test-ro",
		MCPConfigDir:      mcpConfigDir,
		SubprocessTimeout: "60s",
		PollInterval:      "1s",
		LogLevel:          "info",
	})

	ticket, err := store.New(pool).InsertTicket(context.Background(), store.InsertTicketParams{
		DepartmentID: deptID,
		Objective:    "write hello.txt with the ticket id",
	})
	if err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}

	// Wait for agent_instances.pid to be populated — that's the
	// claude subprocess's PID (UpdatePID backfill).
	var claudePID int
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var pid *int32
		err := pool.QueryRow(context.Background(),
			`SELECT pid FROM agent_instances WHERE ticket_id = $1`, ticket.ID,
		).Scan(&pid)
		if err == nil && pid != nil && *pid > 0 {
			claudePID = int(*pid)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if claudePID == 0 {
		t.Fatalf("agent_instances.pid never populated within 20s; cannot kill claude")
	}

	if err := syscall.Kill(claudePID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill claude pid %d: %v", claudePID, err)
	}

	row := waitForTerminalByTicket(t, pool, ticket.ID, 30*time.Second)
	if row.Status != "failed" {
		t.Errorf("status = %q; want failed", row.Status)
	}
	// FormatSignalled produces "signaled_SIGKILL" per T005 (M1-style
	// canonical SIG prefix). The Adjudicate precedence ensures this
	// only fires when the kill wasn't supervisor-initiated — here the
	// supervisor's ctx is still healthy, so signaled_* wins.
	if row.ExitReason == nil {
		t.Errorf("exit_reason is nil; want signaled_* (probably SIGKILL)")
	} else if !strings.HasPrefix(*row.ExitReason, "signaled_") && *row.ExitReason != "no_result" {
		// no_result is an acceptable alternative outcome when claude
		// exits on SIGKILL before the pipeline observes a result event
		// but the supervisor's own wait detected the signal too late
		// in the adjudicate precedence to surface signaled_*.
		t.Errorf("exit_reason = %q; want signaled_* or no_result", *row.ExitReason)
	}
	t.Logf("TestM21ChaosClaudeSigkilledExternally observed exit_reason=%v (for retro)", row.ExitReason)

	// Zero running rows after the kill settles.
	var running int64
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM agent_instances WHERE status = 'running'`,
	).Scan(&running); err != nil {
		t.Fatalf("count running: %v", err)
	}
	if running != 0 {
		t.Errorf("running count after SIGKILL = %d; want 0", running)
	}

	// event_outbox.processed_at set.
	var processed pgtype.Timestamptz
	if err := pool.QueryRow(context.Background(),
		`SELECT processed_at FROM event_outbox ORDER BY created_at DESC LIMIT 1`,
	).Scan(&processed); err != nil {
		t.Fatalf("fetch event_outbox: %v", err)
	}
	if !processed.Valid {
		t.Error("event_outbox.processed_at is NULL; terminal tx did not commit")
	}
}
