package spawn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/agentcontainer"
	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// deptWithWorkspace builds a minimal store.Department with the given
// workspace path. Empty path → nil pointer (matches "no workspace
// configured" mode).
func deptWithWorkspace(path string) store.Department {
	if path == "" {
		return store.Department{}
	}
	return store.Department{WorkspacePath: &path}
}

func writeFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

// fakeController is a minimal zero-value-usable Controller stub. The
// inflight tests hand it to Deps purely so the container-path gate
// (!UseDirectExec && AgentContainer != nil) reads as selected; none of
// its methods are exercised there. The container-pipeline tests below
// use agentcontainer.FakeController instead — its scripted exec
// sessions carry real exit codes.
type fakeController struct{}

func (fakeController) Create(context.Context, agentcontainer.ContainerSpec) (string, error) {
	return "fake-id", nil
}
func (fakeController) Start(context.Context, string) error   { return nil }
func (fakeController) Stop(context.Context, string) error    { return nil }
func (fakeController) Restart(context.Context, string) error { return nil }
func (fakeController) Remove(context.Context, string) error  { return nil }
func (fakeController) Exec(context.Context, string, agentcontainer.ExecSpec) (*agentcontainer.ExecSession, error) {
	return nil, errors.New("fakeController: exec not scripted")
}
func (fakeController) ConnectNetwork(context.Context, string, string) error { return nil }
func (fakeController) Reconcile(context.Context, []agentcontainer.ExpectedContainer) (agentcontainer.ReconcileReport, error) {
	return agentcontainer.ReconcileReport{}, nil
}
func (fakeController) ReconcileShape(context.Context, []agentcontainer.ContainerSpec) (agentcontainer.ShapeReport, error) {
	return agentcontainer.ShapeReport{}, nil
}
func (fakeController) ImageDigest(context.Context, string) (string, error) {
	return "sha256:fake", nil
}

// -----------------------------------------------------------------------
// M7.1 T011 — container pipeline harness
// -----------------------------------------------------------------------

const (
	m71AgentUUID    = "aaaabbbb-cccc-dddd-eeee-ffff00001111"
	m71InstanceUUID = "22222222-2222-2222-2222-222222222222"
	m71EventUUID    = "33333333-3333-3333-3333-333333333333"
	m71TicketUUID   = "11111111-1111-1111-1111-111111111111"

	// m71ContainerName = "garrison-agent-" + shortID(m71AgentUUID):
	// agent-ID keyed, never role keyed (FR-008).
	m71ContainerName = "garrison-agent-aaaabbbb"
	m71MCPPath       = "/tmp/mcp-" + m71InstanceUUID + ".json"
)

const (
	m71InitFrame       = `{"type":"system","subtype":"init","session_id":"sess-m71","cwd":"/workspace","mcp_servers":[{"name":"postgres","status":"connected"},{"name":"finalize","status":"connected"},{"name":"garrison-mutate","status":"connected"}]}`
	m71ResultFrame     = `{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.014,"session_id":"sess-m71"}`
	m71FailedMCPFrame  = `{"type":"system","subtype":"init","session_id":"sess-m71","cwd":"/workspace","mcp_servers":[{"name":"finalize","status":"failed"}]}`
	m71HealthyStream   = m71InitFrame + "\n" + m71ResultFrame + "\n"
	m71FailedMCPStream = m71FailedMCPFrame + "\n"
)

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("uuid %q: %v", s, err)
	}
	return u
}

// containerHarness wires a FakeController-backed Deps + containerRunInputs
// pair and captures terminal writes through the terminalWriteFn seam so
// the container path's terminal contracts are assertable without a
// database.
type containerHarness struct {
	fc        *agentcontainer.FakeController
	deps      Deps
	in        containerRunInputs
	mu        sync.Mutex
	terminals []terminalWriteParams
}

func newContainerHarness(t *testing.T) *containerHarness {
	t.Helper()
	fc := agentcontainer.NewFakeController()
	fc.Containers[m71ContainerName] = &agentcontainer.FakeContainerState{State: "running"}

	h := &containerHarness{fc: fc}
	h.deps = Deps{
		Logger:            testLogger(),
		SubprocessTimeout: 300 * time.Second,
		UseDirectExec:     false,
		AgentContainer:    fc,
		ClaudeModel:       "claude-sonnet-4-5",
		ClaudeBudgetUSD:   0.10,
		AgentRODSN:        "postgres://garrison_agent_ro:pw@garrison-postgres:5432/garrison",
		DatabaseURL:       "postgres://garrison:pw@garrison-postgres:5432/garrison",
		EgressProxyURL:    "http://garrison-egress-proxy:3128",
		terminalWriteFn: func(_ context.Context, p terminalWriteParams) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.terminals = append(h.terminals, p)
			return nil
		},
	}
	h.in = containerRunInputs{
		InstanceID: mustUUID(t, m71InstanceUUID),
		EventID:    mustUUID(t, m71EventUUID),
		TicketUUID: mustUUID(t, m71TicketUUID),
		Payload:    spawnPayload{TicketID: m71TicketUUID},
		RoleSlug:   "engineer",
		Agent: agents.Agent{
			ID:      mustUUID(t, m71AgentUUID),
			Role:    "engineer",
			AgentMD: "# engineer agent.md",
		},
		Logger: testLogger(),
	}
	return h
}

// script queues the FakeController exec results for the agent container:
// index 0 is the MCP-config write exec, 1 the claude exec, 2 the rm
// cleanup exec (when reached).
func (h *containerHarness) script(results ...agentcontainer.FakeExecResult) {
	h.fc.ExecResults[m71ContainerName] = results
}

func (h *containerHarness) run(ctx context.Context) error {
	return runRealClaudeViaContainer(ctx, h.deps, h.in)
}

func (h *containerHarness) execCalls() []agentcontainer.FakeCall {
	var out []agentcontainer.FakeCall
	for _, c := range h.fc.Calls {
		if c.Method == "Exec" {
			out = append(out, c)
		}
	}
	return out
}

func (h *containerHarness) methodCount(method string) int {
	n := 0
	for _, c := range h.fc.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

func (h *containerHarness) lastTerminal(t *testing.T) terminalWriteParams {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.terminals) != 1 {
		t.Fatalf("terminal writes recorded = %d; want exactly 1 (%+v)", len(h.terminals), h.terminals)
	}
	return h.terminals[0]
}

// cleanupCall returns the rm -f exec call, failing the test if it is
// absent or duplicated.
func (h *containerHarness) cleanupCall(t *testing.T) agentcontainer.FakeCall {
	t.Helper()
	var found []agentcontainer.FakeCall
	for _, c := range h.execCalls() {
		if len(c.Exec.Cmd) > 0 && c.Exec.Cmd[0] == "/bin/rm" {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("rm cleanup execs = %d; want exactly 1", len(found))
	}
	return found[0]
}

// claudeExecPrefixLen is the coreutils-timeout wrapper length in the
// claude exec argv: timeout --signal=TERM --kill-after=10s <secs> claude.
const claudeExecPrefixLen = 5

// TestContainerArgvWrapsCoreutilsTimeout — FR-016/Q3: the claude exec is
// wrapped in coreutils timeout with SIGTERM, a 10s kill-after grace, and
// the per-spawn budget in seconds, and runs with cwd /workspace (FR-006).
func TestContainerArgvWrapsCoreutilsTimeout(t *testing.T) {
	h := newContainerHarness(t)
	h.script(
		agentcontainer.FakeExecResult{},
		agentcontainer.FakeExecResult{Stdout: m71HealthyStream},
		agentcontainer.FakeExecResult{},
	)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	calls := h.execCalls()
	if len(calls) != 3 {
		t.Fatalf("exec calls = %d; want 3 (config write, claude, rm)", len(calls))
	}
	claude := calls[1].Exec
	wantPrefix := []string{
		"/usr/bin/timeout",
		"--signal=TERM",
		"--kill-after=10s",
		"300",
		"/usr/local/bin/claude",
	}
	if len(claude.Cmd) < len(wantPrefix) {
		t.Fatalf("claude exec Cmd too short: %q", claude.Cmd)
	}
	for i, want := range wantPrefix {
		if claude.Cmd[i] != want {
			t.Errorf("Cmd[%d] = %q; want %q", i, claude.Cmd[i], want)
		}
	}
	if claude.WorkingDir != "/workspace" {
		t.Errorf("WorkingDir = %q; want /workspace", claude.WorkingDir)
	}
}

// TestContainerArgvMatchesDirectExecFlagSet — FR-013/US3 golden compare:
// past the timeout wrapper, the container path's claude argv is exactly
// buildClaudeArgv's output — the same flag set the direct-exec transport
// runs, with only the --mcp-config path differing (in-container /tmp).
func TestContainerArgvMatchesDirectExecFlagSet(t *testing.T) {
	h := newContainerHarness(t)
	h.script(
		agentcontainer.FakeExecResult{},
		agentcontainer.FakeExecResult{Stdout: m71HealthyStream},
		agentcontainer.FakeExecResult{},
	)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	claude := h.execCalls()[1].Exec

	want := claudeArgvInputs(h.deps, h.in.Agent, h.in.RoleSlug, h.in.Payload.TicketID, m71InstanceUUID, h.in.WakeUpStdout)
	want.MCPConfigPath = m71MCPPath
	wantArgv := buildClaudeArgv(want)

	got := claude.Cmd[claudeExecPrefixLen:]
	if len(got) != len(wantArgv) {
		t.Fatalf("container argv length = %d; want %d\ngot:  %q\nwant: %q", len(got), len(wantArgv), got, wantArgv)
	}
	for i := range wantArgv {
		if got[i] != wantArgv[i] {
			t.Errorf("argv[%d] = %q; want %q", i, got[i], wantArgv[i])
		}
	}
}

// TestContainerMCPConfigPathUsesInstanceID — the per-spawn MCP config is
// written to /tmp/mcp-<instance-uuid>.json via a sh -c exec whose bytes
// transit GARRISON_MCP_CONFIG_JSON env (never argv, never stdin —
// FR-002/FR-004), and the claude argv's --mcp-config references the same
// in-container path.
func TestContainerMCPConfigPathUsesInstanceID(t *testing.T) {
	h := newContainerHarness(t)
	h.script(
		agentcontainer.FakeExecResult{},
		agentcontainer.FakeExecResult{Stdout: m71HealthyStream},
		agentcontainer.FakeExecResult{},
	)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	calls := h.execCalls()

	cfgWrite := calls[0].Exec
	wantCmd := []string{"/bin/sh", "-c", `umask 077; printf %s "$GARRISON_MCP_CONFIG_JSON" > ` + m71MCPPath}
	if len(cfgWrite.Cmd) != len(wantCmd) {
		t.Fatalf("config-write Cmd = %q; want %q", cfgWrite.Cmd, wantCmd)
	}
	for i := range wantCmd {
		if cfgWrite.Cmd[i] != wantCmd[i] {
			t.Errorf("config-write Cmd[%d] = %q; want %q", i, cfgWrite.Cmd[i], wantCmd[i])
		}
	}
	if len(cfgWrite.Env) != 1 || !strings.HasPrefix(cfgWrite.Env[0], "GARRISON_MCP_CONFIG_JSON={") {
		t.Errorf("config-write Env = %q; want single GARRISON_MCP_CONFIG_JSON entry carrying the rendered JSON", cfgWrite.Env)
	}
	// The rendered config must reference the bind-mounted supervisor
	// binary and omit mempalace (FR-014, Q1).
	if !strings.Contains(cfgWrite.Env[0], "/usr/local/bin/garrison-supervisor") {
		t.Errorf("rendered config does not reference the in-container supervisor binary")
	}
	if strings.Contains(cfgWrite.Env[0], "mempalace") {
		t.Errorf("rendered config carries a mempalace entry; container path must omit it")
	}

	claude := calls[1].Exec.Cmd
	idx := -1
	for i, arg := range claude {
		if arg == "--mcp-config" {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(claude) || claude[idx+1] != m71MCPPath {
		t.Errorf("claude argv --mcp-config = %q; want %q", claude, m71MCPPath)
	}
}

// TestContainerExecEnvComposition — plan D17 / FR-011: the claude exec
// env is exactly HOME (tmpfs), HTTPS_PROXY (egress proxy), the
// telemetry-off pair, the tool-search pin, auth passthrough from the
// supervisor's env, then the vault values via the shared appendSecretEnv
// helper.
func TestContainerExecEnvComposition(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-sentinel")
	t.Setenv("ANTHROPIC_API_KEY", "api-key-sentinel")

	h := newContainerHarness(t)
	h.in.Fetched = map[string]vault.SecretValue{
		"EXTRA_SECRET": vault.New([]byte("vault-sentinel")),
	}
	h.script(
		agentcontainer.FakeExecResult{},
		agentcontainer.FakeExecResult{Stdout: m71HealthyStream},
		agentcontainer.FakeExecResult{},
	)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	claude := h.execCalls()[1].Exec
	want := []string{
		"HOME=/home/node",
		"HTTPS_PROXY=http://garrison-egress-proxy:3128",
		"DISABLE_TELEMETRY=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"ENABLE_TOOL_SEARCH=false",
		"CLAUDE_CODE_OAUTH_TOKEN=oauth-sentinel",
		"ANTHROPIC_API_KEY=api-key-sentinel",
		"EXTRA_SECRET=vault-sentinel",
	}
	if len(claude.Env) != len(want) {
		t.Fatalf("claude exec Env = %q; want %q", claude.Env, want)
	}
	for i := range want {
		if claude.Env[i] != want[i] {
			t.Errorf("Env[%d] = %q; want %q", i, claude.Env[i], want[i])
		}
	}
}

// TestSecretsNeverInArgvOrCreateBody — SC-003 at the unit level: vault
// values transit per-exec Env only. No container create happens at spawn
// time at all, and no exec's argv (nor the rendered MCP config) carries
// secret bytes.
func TestSecretsNeverInArgvOrCreateBody(t *testing.T) {
	const secret = "vault-secret-sentinel"
	h := newContainerHarness(t)
	h.in.Fetched = map[string]vault.SecretValue{
		"EXTRA_SECRET": vault.New([]byte(secret)),
	}
	h.script(
		agentcontainer.FakeExecResult{},
		agentcontainer.FakeExecResult{Stdout: m71HealthyStream},
		agentcontainer.FakeExecResult{},
	)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := h.methodCount("Create"); n != 0 {
		t.Errorf("Create calls during spawn = %d; want 0 (secrets can never reach a create body)", n)
	}
	calls := h.execCalls()
	for i, c := range calls {
		for _, arg := range c.Exec.Cmd {
			if strings.Contains(arg, secret) {
				t.Errorf("exec %d argv carries the secret: %q", i, arg)
			}
		}
	}
	// The rendered MCP config (config-write Env) must not carry it.
	for _, e := range calls[0].Exec.Env {
		if strings.Contains(e, secret) {
			t.Errorf("rendered MCP config carries the secret")
		}
	}
	// The claude exec env is the single sanctioned transit.
	foundInClaudeEnv := false
	for _, e := range calls[1].Exec.Env {
		if e == "EXTRA_SECRET="+secret {
			foundInClaudeEnv = true
		}
	}
	if !foundInClaudeEnv {
		t.Errorf("claude exec Env missing the vault value; appendSecretEnv not applied")
	}
}

// TestExit124AdjudicatesTimeout — plan D21: coreutils timeout's exit 124
// (TERM-killed on budget) maps to the DeadlineExceeded wait row and
// adjudicates as exit_reason=timeout (FR-016/FR-019 — no new vocabulary).
func TestExit124AdjudicatesTimeout(t *testing.T) {
	h := newContainerHarness(t)
	h.script(
		agentcontainer.FakeExecResult{},
		agentcontainer.FakeExecResult{Stdout: m71InitFrame + "\n", ExitCode: 124},
		agentcontainer.FakeExecResult{},
	)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	term := h.lastTerminal(t)
	if term.Status != "timeout" || term.ExitReason != ExitTimeout {
		t.Errorf("terminal = (%s, %s); want (timeout, %s)", term.Status, term.ExitReason, ExitTimeout)
	}
	if term.SkipMarkProcessed {
		t.Errorf("timeout terminal must mark the event processed")
	}
}

// TestExit125To127AdjudicateSpawnFailed — plan D21: coreutils timeout's
// own failure vocabulary (125 wrapper failed, 126 not runnable, 127 not
// found) with no result frame is infrastructure, not agent behavior —
// the no_result adjudication is remapped into the spawn_failed class.
func TestExit125To127AdjudicateSpawnFailed(t *testing.T) {
	for _, code := range []int{125, 126, 127} {
		t.Run(fmt.Sprintf("exit_%d", code), func(t *testing.T) {
			h := newContainerHarness(t)
			h.script(
				agentcontainer.FakeExecResult{},
				agentcontainer.FakeExecResult{ExitCode: code},
				agentcontainer.FakeExecResult{},
			)
			if err := h.run(context.Background()); err != nil {
				t.Fatalf("run: %v", err)
			}
			term := h.lastTerminal(t)
			if term.Status != "failed" || term.ExitReason != ExitSpawnFailed {
				t.Errorf("exit %d terminal = (%s, %s); want (failed, %s)", code, term.Status, term.ExitReason, ExitSpawnFailed)
			}
		})
	}
}

// TestExit137AdjudicatesSignaledSigkill — plan D21: 128+n decodes the
// terminating signal; 137 (timeout -k's KILL after the TERM grace) lands
// in signaled_SIGKILL via FormatSignalled.
func TestExit137AdjudicatesSignaledSigkill(t *testing.T) {
	h := newContainerHarness(t)
	h.script(
		agentcontainer.FakeExecResult{},
		agentcontainer.FakeExecResult{ExitCode: 137},
		agentcontainer.FakeExecResult{},
	)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	term := h.lastTerminal(t)
	if term.Status != "failed" || term.ExitReason != "signaled_SIGKILL" {
		t.Errorf("terminal = (%s, %s); want (failed, signaled_SIGKILL)", term.Status, term.ExitReason)
	}
}

// TestExecCreateFailureWritesSpawnFailedEventRetryable — FR-019: the
// claude exec-create being rejected (container missing/stopped, proxy
// refusal) writes the spawn_failed terminal but leaves the event
// unprocessed so the next poll sweep retries after the boot reconciler
// repairs the container.
func TestExecCreateFailureWritesSpawnFailedEventRetryable(t *testing.T) {
	h := newContainerHarness(t)
	h.script(
		agentcontainer.FakeExecResult{},
		agentcontainer.FakeExecResult{Err: errors.New("simulated exec-create rejection")},
		agentcontainer.FakeExecResult{},
	)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	term := h.lastTerminal(t)
	if term.Status != "failed" || term.ExitReason != ExitSpawnFailed {
		t.Errorf("terminal = (%s, %s); want (failed, %s)", term.Status, term.ExitReason, ExitSpawnFailed)
	}
	if !term.SkipMarkProcessed {
		t.Errorf("exec-create failure must leave the event unprocessed (retryable)")
	}
	// The cleanup exec still runs: the config file was already written.
	h.cleanupCall(t)
}

// TestConfigWriteExecFailureWritesSpawnFailed — the MCP-config write
// exec exiting non-zero follows the host-side mcpconfig.Write failure
// contract: spawn_failed, event processed, dispatcher continues. The
// claude exec is never attempted.
func TestConfigWriteExecFailureWritesSpawnFailed(t *testing.T) {
	h := newContainerHarness(t)
	h.script(
		agentcontainer.FakeExecResult{ExitCode: 1},
	)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	term := h.lastTerminal(t)
	if term.Status != "failed" || term.ExitReason != ExitSpawnFailed {
		t.Errorf("terminal = (%s, %s); want (failed, %s)", term.Status, term.ExitReason, ExitSpawnFailed)
	}
	if term.SkipMarkProcessed {
		t.Errorf("config-write failure marks the event processed (mcpconfig.Write contract)")
	}
	if calls := h.execCalls(); len(calls) != 1 {
		t.Errorf("exec calls = %d; want 1 (no claude exec, no cleanup for an unwritten config)", len(calls))
	}
}

// TestConfigCleanupExecRunsOnEveryExitPath — the deferred rm -f exec
// fires on the success, MCP-gate-bail, and exec-error paths alike
// (mirrors the host-side mcpconfig.Remove defer).
func TestConfigCleanupExecRunsOnEveryExitPath(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		h := newContainerHarness(t)
		h.script(
			agentcontainer.FakeExecResult{},
			agentcontainer.FakeExecResult{Stdout: m71HealthyStream},
			agentcontainer.FakeExecResult{},
		)
		if err := h.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		rm := h.cleanupCall(t)
		wantCmd := []string{"/bin/rm", "-f", m71MCPPath}
		for i, want := range wantCmd {
			if rm.Exec.Cmd[i] != want {
				t.Errorf("rm Cmd[%d] = %q; want %q", i, rm.Exec.Cmd[i], want)
			}
		}
	})
	t.Run("mcp_gate_bail", func(t *testing.T) {
		h := newContainerHarness(t)
		h.script(
			agentcontainer.FakeExecResult{},
			agentcontainer.FakeExecResult{Stdout: m71FailedMCPStream, ExitCode: 137},
			agentcontainer.FakeExecResult{},
		)
		if err := h.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		h.cleanupCall(t)
		// FR-108 carried: the bail hook restarts the container (the
		// SIGKILL analog) and adjudication keeps the mcp_<server>_<status>
		// vocabulary.
		if n := h.methodCount("Restart"); n == 0 {
			t.Errorf("MCP-gate bail did not Restart the container")
		}
		term := h.lastTerminal(t)
		if term.Status != "failed" || term.ExitReason != "mcp_finalize_failed" {
			t.Errorf("terminal = (%s, %s); want (failed, mcp_finalize_failed)", term.Status, term.ExitReason)
		}
	})
	t.Run("exec_error", func(t *testing.T) {
		h := newContainerHarness(t)
		h.script(
			agentcontainer.FakeExecResult{},
			agentcontainer.FakeExecResult{Err: errors.New("simulated exec-create rejection")},
			agentcontainer.FakeExecResult{},
		)
		if err := h.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		h.cleanupCall(t)
	})
}

// TestContainerLookupKeyedByAgentIDNotRole — FR-008 regression pin for
// the acceptance-diary latent bug: every exec addresses the container by
// garrison-agent-<short-agent-id>, never by role slug.
func TestContainerLookupKeyedByAgentIDNotRole(t *testing.T) {
	h := newContainerHarness(t)
	h.script(
		agentcontainer.FakeExecResult{},
		agentcontainer.FakeExecResult{Stdout: m71HealthyStream},
		agentcontainer.FakeExecResult{},
	)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	calls := h.execCalls()
	if len(calls) == 0 {
		t.Fatal("no exec calls recorded")
	}
	for i, c := range calls {
		if c.ID != m71ContainerName {
			t.Errorf("exec %d container = %q; want %q", i, c.ID, m71ContainerName)
		}
		if c.ID == "garrison-agent-engineer" {
			t.Errorf("exec %d addressed the role-keyed container name (the FR-008 latent bug)", i)
		}
	}
}

// shutdownScriptController wraps the FakeController so the claude exec's
// stdout blocks until Restart is invoked — the shape of a live exec
// being torn down by the container-restart SIGKILL analog.
type shutdownScriptController struct {
	*agentcontainer.FakeController
	claudeStarted chan struct{}
	unblock       chan struct{}
	startOnce     sync.Once
	unblockOnce   sync.Once
	restarts      atomic.Int32
}

type blockUntilClosed struct{ ch chan struct{} }

func (b *blockUntilClosed) Read([]byte) (int, error) {
	<-b.ch
	return 0, io.EOF
}

func (c *shutdownScriptController) Exec(ctx context.Context, id string, spec agentcontainer.ExecSpec) (*agentcontainer.ExecSession, error) {
	sess, err := c.FakeController.Exec(ctx, id, spec)
	if err != nil || len(spec.Cmd) == 0 || spec.Cmd[0] != "/usr/bin/timeout" {
		return sess, err
	}
	sess.Stdout = io.NopCloser(&blockUntilClosed{ch: c.unblock})
	c.startOnce.Do(func() { close(c.claudeStarted) })
	return sess, nil
}

func (c *shutdownScriptController) Restart(ctx context.Context, id string) error {
	c.restarts.Add(1)
	c.unblockOnce.Do(func() { close(c.unblock) })
	return c.FakeController.Restart(ctx, id)
}

// TestShutdownMidExecRestartsContainer — FR-016: dispatcher-context
// cancellation (supervisor shutdown) drives the runner's Terminate
// ladder, which restarts the container, and the terminal row keeps the
// existing supervisor_shutdown precedence.
func TestShutdownMidExecRestartsContainer(t *testing.T) {
	h := newContainerHarness(t)
	sc := &shutdownScriptController{
		FakeController: h.fc,
		claudeStarted:  make(chan struct{}),
		unblock:        make(chan struct{}),
	}
	h.deps.AgentContainer = sc
	h.script(
		agentcontainer.FakeExecResult{},
		agentcontainer.FakeExecResult{ExitCode: 137},
		agentcontainer.FakeExecResult{},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- h.run(ctx) }()

	select {
	case <-sc.claudeStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("claude exec never started")
	}
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after shutdown")
	}

	if sc.restarts.Load() == 0 {
		t.Errorf("Restart was never called on shutdown")
	}
	term := h.lastTerminal(t)
	if term.Status != "failed" || term.ExitReason != ExitSupervisorShutdown {
		t.Errorf("terminal = (%s, %s); want (failed, %s)", term.Status, term.ExitReason, ExitSupervisorShutdown)
	}
}

// TestUpdatePIDSkippedOnContainerPath — the exec's PID is host-namespace
// and never recorded: pid stays NULL. Deps.Queries is nil here, so any
// UpdatePID (or other store call) on this path would panic — completing
// the run proves the column is never touched. The only side effects are
// the three execs and the seam-captured terminal write.
func TestUpdatePIDSkippedOnContainerPath(t *testing.T) {
	h := newContainerHarness(t)
	if h.deps.Queries != nil {
		t.Fatal("harness must leave Queries nil for this test")
	}
	h.script(
		agentcontainer.FakeExecResult{},
		agentcontainer.FakeExecResult{Stdout: m71HealthyStream},
		agentcontainer.FakeExecResult{},
	)
	if err := h.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, c := range h.fc.Calls {
		if c.Method != "Exec" {
			t.Errorf("unexpected controller call %q; container spawn issues execs only", c.Method)
		}
	}
	h.lastTerminal(t)
}

// TestContainerStdoutDrainAheadPreventsDemuxStall — live-smoke finding
// (2026-06-11): pipeline.Run's M6 result-grace path can return before
// stdout EOF; the sequential demux would then block writing the next
// frame into an unread io.Pipe and never close the stderr side. The
// drain-ahead relay must let the source complete to EOF with no
// consumer attached, while preserving every byte for a late reader.
func TestContainerStdoutDrainAheadPreventsDemuxStall(t *testing.T) {
	pr, pw := io.Pipe()
	relay := newDrainAheadReader(pr)

	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		// Without the relay these writes block forever: io.Pipe has no
		// buffer and no reader is consuming yet.
		for i := 0; i < 3; i++ {
			if _, err := pw.Write([]byte("post-grace frame\n")); err != nil {
				t.Errorf("source write %d: %v", i, err)
				return
			}
		}
		_ = pw.Close()
	}()

	select {
	case <-wrote:
		// Source reached EOF with zero consumer reads — the demux-side
		// stall is structurally impossible.
	case <-time.After(5 * time.Second):
		t.Fatal("source writes blocked; drain-ahead relay is not draining")
	}

	got, err := io.ReadAll(relay)
	if err != nil && err != io.EOF {
		t.Fatalf("relay read: %v", err)
	}
	want := strings.Repeat("post-grace frame\n", 3)
	if string(got) != want {
		t.Errorf("relay bytes = %q; want %q", got, want)
	}
}

// -----------------------------------------------------------------------
// M7 forensic-hash helpers (unchanged surface)
// -----------------------------------------------------------------------

// TestComputeClaudeMDHashEmptyDept returns nil for the no-workspace
// path and a stable hash for the workspace-with-CLAUDE.md case.
func TestComputeClaudeMDHashEmptyDept(t *testing.T) {
	if got := computeClaudeMDHash(deptWithWorkspace("")); got != nil {
		t.Errorf("empty workspace path should yield nil; got %v", got)
	}
}

func TestComputeClaudeMDHashFromWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir, "CLAUDE.md", "# project context"); err != nil {
		t.Fatalf("seed CLAUDE.md: %v", err)
	}
	got := computeClaudeMDHash(deptWithWorkspace(dir))
	if got == nil {
		t.Fatal("expected non-nil hash for workspace with CLAUDE.md")
	}
	// SHA-256 hex is 64 chars.
	if len(*got) != 64 {
		t.Errorf("hash length = %d; want 64", len(*got))
	}
}

func TestComputeClaudeMDHashMissingFile(t *testing.T) {
	dir := t.TempDir()
	got := computeClaudeMDHash(deptWithWorkspace(dir))
	if got != nil {
		t.Errorf("missing CLAUDE.md should yield nil; got %v", got)
	}
}
