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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
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
