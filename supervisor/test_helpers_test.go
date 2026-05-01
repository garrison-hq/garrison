//go:build integration || chaos || live_acceptance || experiment

package supervisor_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// supervisorOpts bundles the common env overrides tests need. Any field
// left blank gets the supervisor's default. M2.1 fields (RealClaude*,
// AgentROPassword, MCPConfigDir) are only relevant when FakeAgentCmd is
// empty — setting both selects the real-Claude codepath.
type supervisorOpts struct {
	FakeAgentCmd      string
	PollInterval      string
	SubprocessTimeout string
	ShutdownGrace     string
	LogLevel          string

	// Real-Claude mode — leave ClaudeBin and AgentROPassword unset to
	// stay in fake-agent mode (FakeAgentCmd above).
	ClaudeBin              string
	AgentROPassword        string
	AgentMempalacePassword string // M2.2: required in real-claude mode. Auto-defaults to "integration-test-mp" when ClaudeBin is set and this is left blank.
	MCPConfigDir           string
	MockClaudeScript       string // forwarded as GARRISON_MOCK_CLAUDE_SCRIPT
	SignalMarker           string // forwarded as GARRISON_MOCK_CLAUDE_SIGNAL_MARKER

	// HomeOverride sets HOME for the supervisor subprocess (used by the
	// session-persistence test to scope claude's writes under a tempdir).
	HomeOverride string

	// SupervisorBinOverride forwards GARRISON_SUPERVISOR_BIN_OVERRIDE
	// into the daemon env. T018 BrokenMCPConfig test points this at
	// /bin/does-not-exist so the MCP server command in the per-spawn
	// config is unrunnable, forcing Claude to report postgres.status
	// =failed at init.
	SupervisorBinOverride string

	// PgmcpPIDFile forwards GARRISON_PGMCP_PID_FILE so the pgmcp
	// subcommand writes its own PID to the file on startup. T018
	// ChaosPgmcpDiesMidRun reads the file to externally kill the
	// subprocess and observe Claude's response.
	PgmcpPIDFile string

	// LogSink, if non-nil, receives a copy of the supervisor's stdout
	// alongside os.Stdout so tests can assert on structured log lines.
	// Writes are serialised via safeBuffer because the integration test
	// suite may have multiple goroutines Read()ing the captured stream
	// while the subprocess Write()s new lines.
	LogSink *safeBuffer
}

// safeBuffer is a mutex-guarded bytes.Buffer. Used by LogSink so the
// supervisor's JSON-per-line stdout can be written by cmd and read
// by the test without racing.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
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
		"GARRISON_DATABASE_URL="+url,
		fmt.Sprintf("GARRISON_HEALTH_PORT=%d", port),
		// M5.4 retro: re-enable the M1/M2.1 back-compat dispatch on
		// `created.engineering.todo` so chaos+integration tests that
		// pin the original todo-spawn contract still run. Production
		// keeps this off — `todo` is the operator triage/backlog column.
		"GARRISON_M21_BACKCOMPAT_DISPATCH=1",
	)
	if opts.FakeAgentCmd != "" {
		env = append(env, "GARRISON_FAKE_AGENT_CMD="+opts.FakeAgentCmd)
	}
	if opts.PollInterval != "" {
		env = append(env, "GARRISON_POLL_INTERVAL="+opts.PollInterval)
	}
	if opts.SubprocessTimeout != "" {
		env = append(env, "GARRISON_SUBPROCESS_TIMEOUT="+opts.SubprocessTimeout)
	}
	if opts.ShutdownGrace != "" {
		env = append(env, "GARRISON_SHUTDOWN_GRACE="+opts.ShutdownGrace)
	}
	if opts.LogLevel != "" {
		env = append(env, "GARRISON_LOG_LEVEL="+opts.LogLevel)
	}
	if opts.ClaudeBin != "" {
		env = append(env, "GARRISON_CLAUDE_BIN="+opts.ClaudeBin)
	}
	if opts.AgentROPassword != "" {
		env = append(env, "GARRISON_AGENT_RO_PASSWORD="+opts.AgentROPassword)
	}
	// M2.2: real-claude mode requires GARRISON_AGENT_MEMPALACE_PASSWORD.
	// Auto-default when ClaudeBin is set but the opts field is left blank
	// so M2.1 integration tests (which pre-date the M2.2 env check)
	// continue to pass without every test having to set it explicitly.
	// Also disable the palace bootstrap + hygiene goroutines — M2.1 tests
	// use mock-claude fixtures that don't exercise MemPalace, and CI
	// doesn't stand up a mempalace sidecar for them. M2.2-specific tests
	// that do need MemPalace (integration_m2_2_*) live in their own files
	// and don't use startSupervisor.
	if opts.ClaudeBin != "" {
		mpw := opts.AgentMempalacePassword
		if mpw == "" {
			mpw = "integration-test-mp"
		}
		env = append(env,
			"GARRISON_AGENT_MEMPALACE_PASSWORD="+mpw,
			"GARRISON_DISABLE_PALACE_BOOTSTRAP=1",
		)
	}
	if opts.MCPConfigDir != "" {
		env = append(env, "GARRISON_MCP_CONFIG_DIR="+opts.MCPConfigDir)
	}
	if opts.MockClaudeScript != "" {
		env = append(env, "GARRISON_MOCK_CLAUDE_SCRIPT="+opts.MockClaudeScript)
	}
	if opts.SignalMarker != "" {
		env = append(env, "GARRISON_MOCK_CLAUDE_SIGNAL_MARKER="+opts.SignalMarker)
	}
	if opts.SupervisorBinOverride != "" {
		env = append(env, "GARRISON_SUPERVISOR_BIN_OVERRIDE="+opts.SupervisorBinOverride)
	}
	if opts.PgmcpPIDFile != "" {
		env = append(env, "GARRISON_PGMCP_PID_FILE="+opts.PgmcpPIDFile)
	}
	if opts.HomeOverride != "" {
		// Strip any existing HOME entry before appending so the override
		// wins regardless of the parent shell's HOME.
		filtered := env[:0]
		for _, kv := range env {
			if !startsWith(kv, "HOME=") {
				filtered = append(filtered, kv)
			}
		}
		env = append(filtered, "HOME="+opts.HomeOverride)
	}

	cmd := exec.Command(bin)
	cmd.Env = env
	if opts.LogSink != nil {
		cmd.Stdout = io.MultiWriter(os.Stdout, opts.LogSink)
	} else {
		cmd.Stdout = os.Stdout
	}
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

// buildMockClaudeBinary compiles the integration-test stand-in for the
// real claude binary (supervisor/internal/spawn/mockclaude). Returns the
// absolute path of the produced binary; the t.TempDir it writes into is
// cleaned up automatically on test end.
func buildMockClaudeBinary(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	supervisorDir := filepath.Dir(thisFile)
	out := filepath.Join(t.TempDir(), "mockclaude")
	cmd := exec.Command("go", "build", "-o", out, "./internal/spawn/mockclaude")
	cmd.Dir = supervisorDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build mockclaude: %v", err)
	}
	return out
}

// mockClaudeScriptPath returns the absolute path to a fixture NDJSON
// script under supervisor/internal/spawn/mockclaude/scripts/. Relative
// paths break when the test binary runs from an unusual cwd (go test -c
// then ./pkg.test, bazel-style sandboxes), so compose the canonical
// absolute path once.
func mockClaudeScriptPath(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	supervisorDir := filepath.Dir(thisFile)
	p := filepath.Join(supervisorDir, "internal", "spawn", "mockclaude", "scripts", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("mockclaude script %s: %v", p, err)
	}
	return p
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

// terminalRow is the shape waitForTerminalByTicket returns — the four
// fields failure-path assertions care about. Moved to test_helpers_test
// so both integration_test.go and chaos_test.go can reach it without
// duplicating the SELECT.
type terminalRow struct {
	Status     string
	ExitReason *string
	Pid        *int32
	Cost       *string
}

// waitForTerminalByTicket polls for any non-running agent_instance row
// against the supplied ticket_id and returns status/exit_reason/pid/
// cost. Fails the test if no terminal row lands within the budget.
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

// startsWith is strings.HasPrefix aliased for readability at call site.
func startsWith(s, prefix string) bool { return strings.HasPrefix(s, prefix) }

// newLogSink allocates a fresh safeBuffer for a test that wants to
// inspect supervisor log lines.
func newLogSink() *safeBuffer {
	return &safeBuffer{}
}

// waitForLogSubstring polls the supplied sink until every substring in
// subs appears on the same log line, or the deadline elapses. Returns
// the first matching line. Fails the test on timeout.
func waitForLogSubstring(t *testing.T, sink *safeBuffer, within time.Duration, subs ...string) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(sink.String(), "\n") {
			if line == "" {
				continue
			}
			ok := true
			for _, s := range subs {
				if !strings.Contains(line, s) {
					ok = false
					break
				}
			}
			if ok {
				return line
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for log line containing %v within %s", subs, within)
	return ""
}

// discardUnused pins the bytes import in tests that do not directly
// reference it, since bytes.Buffer is only referenced by the safeBuffer
// type field.
var _ = bytes.NewBuffer
