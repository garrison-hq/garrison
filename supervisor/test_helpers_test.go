//go:build integration || chaos

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
	"github.com/jackc/pgx/v5/pgxpool"
)

// supervisorOpts bundles the common env overrides tests need. Any field
// left blank gets the supervisor's default.
type supervisorOpts struct {
	FakeAgentCmd      string
	PollInterval      string
	SubprocessTimeout string
	ShutdownGrace     string
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
	if opts.ShutdownGrace != "" {
		env = append(env, "ORG_OS_SHUTDOWN_GRACE="+opts.ShutdownGrace)
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
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return
		}
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

// waitForRunningCount polls until COUNT(agent_instances WHERE status='running')
// reaches want, or the deadline elapses. Used by chaos tests that need a
// running subprocess to hook into before injecting a fault.
func waitForRunningCount(t *testing.T, pool *pgxpool.Pool, want int64, within time.Duration) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(within)
	var got int64
	for time.Now().Before(deadline) {
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM agent_instances WHERE status='running'`,
		).Scan(&got); err == nil && got >= want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d running rows; got %d", want, got)
}
