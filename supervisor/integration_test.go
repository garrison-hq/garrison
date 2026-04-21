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
	"syscall"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestEndToEndTicketFlow is the M1 smoke test: boot a real supervisor
// binary, insert one ticket, observe exactly one succeeded agent_instance
// and a processed event_outbox row. If this passes, the happy path
// (ticket → trigger → pg_notify → LISTEN → spawn → terminal tx) works
// end-to-end.
func TestEndToEndTicketFlow(t *testing.T) {
	pool := testdb.Start(t)
	url := testdb.URL(t)
	q := store.New(pool)
	ctx := context.Background()

	bin := buildSupervisorBinary(t)
	fakeAgent := `sh -c "echo ok; exit 0"`
	port := mustFreePort(t)

	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(),
		"ORG_OS_DATABASE_URL="+url,
		"ORG_OS_FAKE_AGENT_CMD="+fakeAgent,
		fmt.Sprintf("ORG_OS_HEALTH_PORT=%d", port),
		"ORG_OS_POLL_INTERVAL=1s",
		"ORG_OS_LOG_LEVEL=info",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Put supervisor in its own process group so we can SIGTERM it and any
	// subprocesses without touching the test process.
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

	// Wait for the supervisor to reach steady state via its own /health
	// endpoint. /health flips to 200 only once the first fallback poll has
	// landed, so this is the correct readiness signal.
	if err := waitForHealth(port, 10*time.Second); err != nil {
		t.Fatalf("supervisor never became healthy: %v", err)
	}

	// Seed a department and a ticket. The event_outbox row is written
	// transactionally by the emit_ticket_created trigger; the pg_notify
	// fires on commit and the supervisor's LISTEN loop picks it up.
	dept, err := q.InsertDepartment(ctx, store.InsertDepartmentParams{
		Slug: "eng", Name: "Engineering", ConcurrencyCap: 2,
	})
	if err != nil {
		t.Fatalf("InsertDepartment: %v", err)
	}
	if _, err := q.InsertTicket(ctx, store.InsertTicketParams{
		DepartmentID: dept.ID,
		Objective:    "golden path",
	}); err != nil {
		t.Fatalf("InsertTicket: %v", err)
	}

	// Poll up to 10s for exactly one succeeded agent_instances row.
	deadline := time.Now().Add(10 * time.Second)
	var (
		instanceID pgtype.UUID
		status     string
		reason     *string
	)
	for time.Now().Before(deadline) {
		err := pool.QueryRow(ctx,
			`SELECT id, status, exit_reason FROM agent_instances ORDER BY started_at DESC LIMIT 1`,
		).Scan(&instanceID, &status, &reason)
		if err == nil && status == "succeeded" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != "succeeded" {
		t.Fatalf("expected agent_instances.status=succeeded, got %q (reason=%v)", status, reason)
	}
	if reason == nil || *reason != "exit_code_0" {
		t.Fatalf("expected exit_reason=exit_code_0, got %v", reason)
	}

	// The terminal tx is supposed to commit MarkEventProcessed atomically
	// with UpdateInstanceTerminal (FR-006). After the agent_instance row is
	// succeeded, the newest event_outbox row must be processed.
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

// buildSupervisorBinary compiles cmd/supervisor into a test-scoped temp dir
// and returns the absolute path. Each test invocation builds once; the
// binary is cleaned up automatically when the test exits.
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

// mustFreePort asks the kernel for an ephemeral port by binding-then-closing
// a listener. There's a tiny race between close and the supervisor binding
// the same port, but in practice the integration harness is not competing
// with itself for ports.
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

// waitForHealth polls /health until it returns 200 or the deadline elapses.
// Any other response (including 503 during the pre-first-poll window) is
// treated as "not ready yet".
func waitForHealth(port int, within time.Duration) error {
	deadline := time.Now().Add(within)
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			// Port is open; now check status.
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
