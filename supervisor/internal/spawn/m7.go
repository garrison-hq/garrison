package spawn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/agentcontainer"
	"github.com/garrison-hq/garrison/supervisor/internal/agentpolicy"
	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/mcpconfig"
	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
)

// M7/M7.1 spawn-side wiring lives in this file so the legacy spawn.go
// pre-M7 surface stays self-contained and the diff for the soak window
// is easy to walk. runRealClaudeViaContainer is the M7.1 (T011) real
// container pipeline: the same sealed spawn contracts as direct-exec —
// shared argv builder, claudeproto consumer, FR-108 gate, finalize
// observer, Adjudicate, typed exit reasons — with only the transport
// swapped to docker exec through the socket proxy (FR-013).

// In-container path constants (plan §5). The agent image carries claude
// + coreutils timeout; the supervisor binary is bind-mounted read-only
// at create time (FR-014); the per-spawn MCP config lives on the /tmp
// tmpfs and dies with the container.
const (
	containerClaudeBin     = "/usr/local/bin/claude"
	containerSupervisorBin = "/usr/local/bin/garrison-supervisor"
	containerMCPDir        = "/tmp"

	// timeoutKillGrace is coreutils timeout's --kill-after window: the
	// in-container SIGTERM→SIGKILL escalation (FR-016, Q3). Exit 124 ⇒
	// TERM-killed on budget; 137 ⇒ KILL after this grace.
	timeoutKillGrace = 10 * time.Second

	// containerCtxSlack pads the supervisor-side execCtx past the
	// in-container timeout budget so the wrapper always fires first;
	// ctx expiry is the network-blackhole backstop only (clarified
	// 2026-06-10: a proxy 403 denial fails fast as a claude error and
	// never rides the timeout).
	containerCtxSlack = 30 * time.Second

	// helperExecTimeout bounds the MCP-config write/cleanup helper
	// execs and the Restart backstop calls — none of them stream agent
	// output, so seconds of budget is generous.
	helperExecTimeout = 10 * time.Second

	// exitPollGrace caps the post-drain exec-inspect poll loop
	// (ExecSession.ExitCode's 10×200 ms budget plus transport latency).
	exitPollGrace = 5 * time.Second

	// mcpConfigEnvVar carries the rendered MCP config bytes into the
	// config-write helper exec. Env transit, never argv, never stdin
	// (FR-002/FR-004 — the deliberate deviation from spike F6's
	// `cat >` sketch, Q6's recorded acceptance).
	mcpConfigEnvVar = "GARRISON_MCP_CONFIG_JSON"
)

// recordM7HashesForInstance updates the just-INSERTed agent_instances
// row with the M7 forensic hashes. Call site is spawn.runRealClaude
// step 3a, immediately after the agent is resolved + before the
// (possibly slow) wake-up RPC. Failures here are best-effort: the spawn
// continues even if the UPDATE fails — the row's image_digest /
// preamble_hash columns default to empty strings so a missing UPDATE
// leaves them empty rather than corrupt.
func recordM7HashesForInstance(
	ctx context.Context,
	deps Deps,
	instanceID pgtype.UUID,
	dept store.Department,
	agent agents.Agent,
) error {
	imageDigest := ""
	if agent.PalaceWing != nil {
		// Until T014's grandfathering migration runs, no agent has an
		// image_digest column populated. Once migrate7 runs, agents.id
		// → image_digest mapping is populated; until then, leave the
		// instance row's image_digest at its '' default.
	}
	claudeMD := computeClaudeMDHash(dept)
	return deps.Queries.UpdateInstanceM7Hashes(ctx, store.UpdateInstanceM7HashesParams{
		ID:           instanceID,
		PreambleHash: agentpolicy.Hash(),
		ClaudeMdHash: claudeMD,
		ImageDigest:  imageDigest,
	})
}

// computeClaudeMDHash returns the SHA-256 of the workspace's CLAUDE.md
// file when one exists at the expected path, NULL otherwise. Claude
// Code's auto-discovery looks at the cwd; we mirror that here so the
// recorded hash matches what Claude actually loaded at spawn time.
func computeClaudeMDHash(dept store.Department) *string {
	if dept.WorkspacePath == nil || *dept.WorkspacePath == "" {
		return nil
	}
	path := filepath.Join(*dept.WorkspacePath, "CLAUDE.md")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil
	}
	hex := hex.EncodeToString(h.Sum(nil))
	return &hex
}

// ErrContainerControllerMissing surfaces when UseDirectExec=false but
// AgentContainer is nil. Spawn writes ExitSpawnFailed and the operator
// gets a clear log line pointing at the misconfiguration.
var ErrContainerControllerMissing = errors.New("spawn: AgentContainer Controller is nil; cannot exec via container path")

// containerRunInputs is the parameter bundle for
// runRealClaudeViaContainer: the invocation identifiers plus everything
// the shared steps 1–3 already resolved (agent row, fetched vault
// values, wake-up capture). The caller (runRealClaude) keeps ownership
// of Fetched's Zero()ing — the values must stay live until the claude
// exec-create POST carries them.
type containerRunInputs struct {
	InstanceID   pgtype.UUID
	EventID      pgtype.UUID
	TicketUUID   pgtype.UUID
	Dept         store.Department
	Payload      spawnPayload
	RoleSlug     string
	Agent        agents.Agent
	Fetched      map[string]vault.SecretValue
	WakeUpStdout string
	WakeUpStatus mempalace.Status
	Logger       *slog.Logger
}

// runRealClaudeViaContainer is the M7.1 container spawn path (plan §5).
// Called when cfg.UseDirectExec is false and AgentContainer is wired,
// after the shared prepare/hashes/wake-up/vault steps. The container is
// addressed by ContainerName(agent.ID) — agent-ID keyed, never role
// keyed (FR-008, the acceptance-diary latent bug).
func runRealClaudeViaContainer(ctx context.Context, deps Deps, in containerRunInputs) error {
	logger := in.Logger.With("via", "agent_container")
	if deps.AgentContainer == nil {
		logger.Error("agent container controller not wired", "err", ErrContainerControllerMissing)
		return writeContainerFail(ctx, deps, in, ExitSpawnFailed, false)
	}
	agentID := formatUUID(in.Agent.ID)
	name := agentcontainer.ContainerName(agentID)
	instanceIDText := formatUUID(in.InstanceID)
	logger = logger.With("container", name)

	// Step 2: render the MCP config — postgres + finalize +
	// garrison-mutate, no mempalace (FR-014, Q1). Command paths
	// reference the bind-mounted supervisor binary; DSNs are identical
	// to direct-exec (FR-010: trust model unchanged). Rule 3 runs
	// inside Render regardless of mode.
	cfgBytes, renderFailReason := renderContainerMCPConfig(deps, in, instanceIDText, logger)
	if renderFailReason != "" {
		return writeContainerFail(ctx, deps, in, renderFailReason, false)
	}
	mcpPath := containerMCPDir + "/mcp-" + instanceIDText + ".json"

	// Step 3: config-write exec (plan D15, operator-approved).
	if ok, retryable := writeContainerMCPConfig(ctx, deps, name, mcpPath, cfgBytes, logger); !ok {
		return writeContainerFail(ctx, deps, in, ExitSpawnFailed, retryable)
	}

	// Step 4: deferred cleanup exec on every exit path — success, bail,
	// and error paths all converge here (mirrors the host-side
	// mcpconfig.Remove defer).
	defer cleanupContainerMCPConfig(ctx, deps, name, mcpPath, logger)

	// Step 5: shared argv (FR-013 — TestContainerArgvMatchesDirectExecFlagSet
	// pins parity with direct-exec; only the --mcp-config path differs)
	// wrapped in coreutils timeout so enforcement lives in-container
	// (FR-016, Q3).
	argvIn := claudeArgvInputs(deps, in.Agent, in.RoleSlug, in.Payload.TicketID, instanceIDText, in.WakeUpStdout)
	argvIn.MCPConfigPath = mcpPath
	argv := buildClaudeArgv(argvIn)
	cmd := make([]string, 0, len(argv)+5)
	cmd = append(cmd,
		"/usr/bin/timeout",
		"--signal=TERM",
		fmt.Sprintf("--kill-after=%ds", int(timeoutKillGrace/time.Second)),
		strconv.Itoa(int(deps.SubprocessTimeout/time.Second)),
		containerClaudeBin,
	)
	cmd = append(cmd, argv...)

	// Step 6: per-exec env (plan D17) — the only env transit (FR-002).
	env := containerExecEnv(deps, in.Fetched)

	// Step 7: supervisor-side backstop ctx. Detached from the
	// dispatcher ctx so shutdown sequencing (the runner's Terminate
	// ladder), not abrupt cancellation, drives session teardown.
	execCtx, execCancel := context.WithTimeout(context.WithoutCancel(ctx), deps.SubprocessTimeout+containerCtxSlack)
	defer execCancel()

	// Step 8: the claude exec. WorkingDir /workspace is the in-container
	// cwd verification surface (FR-006; init frame cwd, SC-001). An
	// exec-create failure leaves the event retryable — the boot
	// reconciler repairs the container and the next poll retries.
	sess, err := deps.AgentContainer.Exec(execCtx, name, agentcontainer.ExecSpec{
		Cmd:        cmd,
		Env:        env,
		WorkingDir: "/workspace",
	})
	if err != nil {
		logger.Error("claude exec-create failed; recording spawn_failed (event retryable)", "err", err)
		return writeContainerFail(ctx, deps, in, ExitSpawnFailed, true)
	}
	defer func() {
		// Post-drain hygiene: closing Stdout tears down the raw stream
		// and with it the demux goroutine (concurrency rule 1).
		_ = sess.Stdout.Close()
		_ = sess.Stderr.Close()
	}()
	logger = logger.With("exec_id", sess.ID)
	logger.Info("claude exec started in agent container")

	restart := func(reason string) {
		rCtx, rCancel := context.WithTimeout(context.WithoutCancel(ctx), helperExecTimeout)
		defer rCancel()
		if rErr := deps.AgentContainer.Restart(rCtx, name); rErr != nil {
			logger.Warn("container restart returned error", "reason", reason, "err", rErr)
		}
	}

	// Blackhole backstop (FR-016 ladder, t = budget + 30s): if the
	// in-container timeout never fires (proxy absent, packets dropped),
	// execCtx expiry restarts the container — the SIGKILL analog. The
	// idle `sleep` PID 1 returns, the stream EOFs, and adjudication
	// lands in timeout. The goroutine ends with the session either way.
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go watchExecDeadline(execCtx, sessionDone, restart, logger)

	// Bail hook (FR-108 MCP gate / parse error): where direct-exec
	// group-kills, the container path restarts — same latch semantics
	// so Adjudicate's Signaled suppression carries over.
	var bailed atomic.Bool
	onBail := func(reason string) {
		if !bailed.CompareAndSwap(false, true) {
			return
		}
		restart("bail:" + reason)
	}

	// Step 9: the container transport + the shared runner. wrapperFailed
	// is written by ExitDetail and read by the terminal-reason override —
	// both run sequentially on the runner's goroutine.
	var wrapperFailed bool
	containerTransport := transport{
		// Both streams ride drain-ahead relays: the demux copies frames
		// into unbuffered pipes sequentially, and pipeline.Run's M6
		// result-grace path can return BEFORE stdout EOF — with no
		// reader left, the demux would wedge mid-stream and never close
		// the stderr side, deadlocking the runner at its stderr drain.
		// Direct-exec tolerates the same early return via kernel pipe
		// buffering; the relays restore that property here (live-smoke
		// finding, 2026-06-11).
		Stdout: newDrainAheadReader(sess.Stdout),
		Stderr: newDrainAheadReader(sess.Stderr),
		Terminate: func(bool) error {
			// Restart is already the SIGKILL analog; the escalation
			// rung re-issues it (idempotent — the container lands back
			// at the idle sleep either way).
			rCtx, rCancel := context.WithTimeout(context.WithoutCancel(ctx), helperExecTimeout)
			defer rCancel()
			return deps.AgentContainer.Restart(rCtx, name)
		},
		ExitDetail: func(c context.Context) WaitDetail {
			wait, wf := inspectContainerExit(c, sess, execCtx, logger)
			wrapperFailed = wf
			return wait
		},
	}

	// UpdatePID is deliberately skipped on this path (pid stays NULL):
	// the exec's PID is host-namespace and not a supervisor child —
	// recording it would invite PID-level signaling the transport
	// cannot honor. The column is nullable; row shape is unchanged.

	// The terminal-reason override rides a per-spawn Deps copy: a
	// coreutils-timeout wrapper failure (exit 125–127) with no result
	// frame adjudicates as no_result, which this remaps into the
	// spawn_failed class (plan D21; FR-019 — no new vocabulary).
	sessDeps := deps
	sessDeps.terminalReasonOverride = func(status, exitReason string) (string, string) {
		if wrapperFailed && exitReason == ExitNoResult {
			logger.Warn("timeout wrapper failed inside the container; classifying spawn_failed")
			return "failed", ExitSpawnFailed
		}
		return status, exitReason
	}

	// Acceptance-gate workspace path is the agent-ID-keyed dir
	// <WorkspaceFS>/<agent-uuid> (FR-006; analyze U1) — the host side
	// of the container's /workspace bind.
	workspacePath := ""
	if deps.AgentWorkspaceFS != "" {
		workspacePath = filepath.Join(deps.AgentWorkspaceFS, agentID)
	}

	fromCol, toCol := transitionColumns(in.RoleSlug, in.Payload.ColumnSlug)
	return runClaudeSession(ctx, execCtx, sessDeps, containerTransport, sessionParams{
		InstanceID:       in.InstanceID,
		EventID:          in.EventID,
		TicketUUID:       in.TicketUUID,
		TicketIDText:     in.Payload.TicketID,
		RoleSlug:         in.RoleSlug,
		OriginColumn:     in.Payload.ColumnSlug,
		Agent:            in.Agent,
		Dept:             in.Dept,
		WakeUpStatus:     in.WakeUpStatus,
		FinalizeExpected: finalizeExpectedForRole(in.RoleSlug, in.Payload.ColumnSlug),
		FromCol:          fromCol,
		ToCol:            toCol,
		WorkspacePath:    workspacePath,
		Bailed:           &bailed,
		OnBail:           onBail,
		Logger:           logger,
	})
}

// renderContainerMCPConfig renders the in-container MCP config (step 2:
// postgres + finalize + garrison-mutate, no mempalace — FR-014, Q1) and
// classifies a render failure into the typed exit reason the caller
// hands writeContainerFail: ExitVaultMCPInConfig on a Rule 3 violation,
// ExitSpawnFailed otherwise. failReason == "" means success.
func renderContainerMCPConfig(deps Deps, in containerRunInputs, instanceIDText string, logger *slog.Logger) (cfgBytes []byte, failReason string) {
	cfgBytes, _, err := mcpconfig.Render(mcpconfig.WriteParams{
		InstanceID:    in.InstanceID,
		SupervisorBin: containerSupervisorBin,
		DSN:           deps.AgentRODSN,
		Finalize: mcpconfig.FinalizeParams{
			SupervisorBin:   containerSupervisorBin,
			AgentInstanceID: instanceIDText,
			DatabaseURL:     deps.AgentRODSN,
		},
		GarrisonMutate: mcpconfig.GarrisonMutateParams{
			SupervisorBin:   containerSupervisorBin,
			AgentInstanceID: instanceIDText,
			DatabaseURL:     deps.DatabaseURL,
		},
		ExtraServersJSON: in.Agent.McpConfig,
		OmitMempalace:    true,
	})
	if err != nil {
		if errors.Is(err, mcpconfig.ErrVaultMCPBanned) {
			logger.Error("mcpconfig.Render: Rule 3 violation — vault-pattern MCP server", "err", err)
			return nil, ExitVaultMCPInConfig
		}
		logger.Error("mcpconfig.Render failed; recording spawn_failed", "err", err)
		return nil, ExitSpawnFailed
	}
	return cfgBytes, ""
}

// writeContainerMCPConfig runs the config-write exec (plan D15,
// operator-approved). The rendered bytes transit the exec-create Env
// exactly like the secrets do; the shell writes them onto the /tmp
// tmpfs with 0600 perms. Non-zero exit or transport error → !ok, the
// same contract as the host-side mcpconfig.Write failure — except a
// missing/stopped container, which stays retryable (FR-019: the boot
// reconciler is the repair path).
func writeContainerMCPConfig(ctx context.Context, deps Deps, name, mcpPath string, cfgBytes []byte, logger *slog.Logger) (ok, eventRetryable bool) {
	writeCtx, writeCancel := context.WithTimeout(ctx, helperExecTimeout)
	defer writeCancel()
	code, werr := runHelperExec(writeCtx, deps.AgentContainer, name, agentcontainer.ExecSpec{
		Cmd: []string{"/bin/sh", "-c", fmt.Sprintf("umask 077; printf %%s \"$%s\" > %s", mcpConfigEnvVar, mcpPath)},
		Env: []string{mcpConfigEnvVar + "=" + string(cfgBytes)},
	})
	if werr != nil || code != 0 {
		logger.Error("container MCP-config write exec failed; recording spawn_failed",
			"exit_code", code, "err", werr)
		return false, errors.Is(werr, agentcontainer.ErrContainerNotFound)
	}
	return true, false
}

// cleanupContainerMCPConfig is the deferred cleanup exec every exit
// path converges on (mirrors the host-side mcpconfig.Remove defer).
// Best-effort: the file lives on tmpfs and dies with the container, so
// a failed rm only warns.
func cleanupContainerMCPConfig(ctx context.Context, deps Deps, name, mcpPath string, logger *slog.Logger) {
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), helperExecTimeout)
	defer cleanupCancel()
	if rmCode, rmErr := runHelperExec(cleanupCtx, deps.AgentContainer, name, agentcontainer.ExecSpec{
		Cmd: []string{"/bin/rm", "-f", mcpPath},
	}); rmErr != nil || rmCode != 0 {
		logger.Warn("container MCP-config cleanup exec failed; tmpfs file dies with the container",
			"exit_code", rmCode, "err", rmErr)
	}
}

// watchExecDeadline is the blackhole-backstop goroutine body (FR-016
// ladder, t = budget + 30s): execCtx deadline expiry restarts the
// container — the SIGKILL analog. The goroutine ends with the session
// either way (sessionDone closes when runRealClaudeViaContainer
// returns), so it never outlives the stream (concurrency rule 1).
func watchExecDeadline(execCtx context.Context, sessionDone <-chan struct{}, restart func(reason string), logger *slog.Logger) {
	select {
	case <-execCtx.Done():
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			logger.Warn("execCtx deadline elapsed past the in-container timeout; restarting container")
			restart("exec_ctx_deadline")
		}
	case <-sessionDone:
	}
}

// inspectContainerExit is the container transport's ExitDetail body:
// poll the exec inspect for the exit code (bounded by exitPollGrace),
// map it through containerWaitDetail with the execCtx deadline overlay,
// and report whether the coreutils-timeout wrapper itself failed
// (exit 125–127 — remapped to spawn_failed by the terminal-reason
// override). The runner calls ExitDetail only after both streams have
// drained (concurrency rule 8), so the inspect never races a read.
func inspectContainerExit(ctx context.Context, sess *agentcontainer.ExecSession, execCtx context.Context, logger *slog.Logger) (WaitDetail, bool) {
	pollCtx, pollCancel := context.WithTimeout(context.WithoutCancel(ctx), exitPollGrace)
	defer pollCancel()
	exitCode, inspectErr := sess.ExitCode(pollCtx)
	if inspectErr != nil {
		logger.Warn("exec exit-code inspect failed; result frames govern", "err", inspectErr)
	}
	wrapperFailed := inspectErr == nil && isWrapperFailureExit(exitCode)
	return containerWaitDetail(exitCode, inspectErr, execCtx.Err()), wrapperFailed
}

// containerExecEnv composes the claude exec's environment (plan D17) —
// the only env transit on the container path (FR-002): HOME on the
// tmpfs (spike F5), the egress proxy (FR-009/FR-012), the telemetry-off
// pair (FR-011, Q7 — the allow-list stays one host and the proxy log
// stays quiet), the 2.1.170 tool-search pin, auth passthrough from the
// supervisor's own env, then vault values via the shared appendSecretEnv
// helper (the sanctioned UnsafeBytes call-site count stays at two).
func containerExecEnv(deps Deps, fetched map[string]vault.SecretValue) []string {
	env := []string{
		"HOME=/home/node",
		"HTTPS_PROXY=" + deps.EgressProxyURL,
		"DISABLE_TELEMETRY=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"ENABLE_TOOL_SEARCH=false",
	}
	for _, key := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	return appendSecretEnv(env, fetched)
}

// containerWaitDetail maps the exec-inspect observation to the runner's
// WaitDetail (plan D21): 124 → the timeout-budget DeadlineExceeded row;
// 128+n → Signaled with the decoded signal (137 → signaled_SIGKILL via
// FormatSignalled); inspect failure → exit code −1, result frames
// govern. The execCtx deadline overlay keeps the blackhole backstop in
// the timeout class regardless of what the restarted exec reports.
// Wrapper failures (125–127) carry no flag here — they adjudicate as
// no_result and the terminal-reason override remaps them.
func containerWaitDetail(exitCode int, inspectErr, execCtxErr error) WaitDetail {
	wait := WaitDetail{ExitCode: exitCode}
	if inspectErr != nil {
		wait.ExitCode = -1
	} else {
		switch {
		case exitCode == 124:
			wait.ContextErr = context.DeadlineExceeded
		case exitCode >= 128:
			wait.Signaled = true
			wait.Signal = syscall.Signal(exitCode - 128)
		}
	}
	if errors.Is(execCtxErr, context.DeadlineExceeded) {
		wait.ContextErr = context.DeadlineExceeded
	}
	return wait
}

// isWrapperFailureExit reports whether the exit code belongs to
// coreutils timeout's own failure vocabulary: 125 (timeout itself
// failed), 126 (command found but not runnable), 127 (command not
// found). With no result frame these classify as spawn_failed —
// infrastructure, not agent behavior (plan D21, FR-019).
func isWrapperFailureExit(exitCode int) bool {
	return exitCode >= 125 && exitCode <= 127
}

// drainAheadReader continuously drains src on its own goroutine into an
// elastic in-memory buffer the consumer reads from. It guarantees src
// always reaches EOF even when the consumer stops reading early — the
// property the container transport needs so the sequential demux can
// never block on an unread pipe side. The goroutine ends when src
// returns an error/EOF; src is the demuxed stream, which is bounded by
// the exec request context and torn down by the deferred session close
// (concurrency rule 1). Buffered-but-unread bytes are bounded by what
// claude emits after the pipeline stops reading — the post-result tail.
type drainAheadReader struct {
	mu   sync.Mutex
	cond *sync.Cond
	buf  bytes.Buffer
	err  error // sticky terminal state once src is fully drained
}

func newDrainAheadReader(src io.Reader) *drainAheadReader {
	r := &drainAheadReader{}
	r.cond = sync.NewCond(&r.mu)
	go func() {
		chunk := make([]byte, 32*1024)
		for {
			n, err := src.Read(chunk)
			r.mu.Lock()
			if n > 0 {
				r.buf.Write(chunk[:n])
			}
			if err != nil {
				r.err = err
			}
			r.cond.Broadcast()
			done := err != nil
			r.mu.Unlock()
			if done {
				return
			}
		}
	}()
	return r
}

func (r *drainAheadReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for r.buf.Len() == 0 && r.err == nil {
		r.cond.Wait()
	}
	if r.buf.Len() > 0 {
		return r.buf.Read(p)
	}
	return 0, r.err
}

// runHelperExec runs one non-claude helper exec (MCP-config write,
// cleanup) through the same Exec surface (plan D5 — no separate API),
// drains both demuxed streams to EOF, and returns the exit code. Stderr
// drains on its own goroutine so an unbuffered demux pipe can never
// stall the stdout drain; both end when the raw stream closes.
func runHelperExec(ctx context.Context, c agentcontainer.Controller, containerID string, spec agentcontainer.ExecSpec) (int, error) {
	sess, err := c.Exec(ctx, containerID, spec)
	if err != nil {
		return -1, err
	}
	defer func() {
		_ = sess.Stdout.Close()
		_ = sess.Stderr.Close()
	}()
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(io.Discard, sess.Stderr)
	}()
	_, _ = io.Copy(io.Discard, sess.Stdout)
	<-stderrDone
	return sess.ExitCode(ctx)
}

// writeContainerFail records a pre-pipeline container-path failure.
// eventRetryable=true leaves the event_outbox row unprocessed (FR-019:
// container missing/stopped or exec-create rejection — the boot
// reconciler is the repair path and the next poll sweep retries);
// false marks it processed like the direct-exec spawn_failed contract.
func writeContainerFail(ctx context.Context, deps Deps, in containerRunInputs, exitReason string, eventRetryable bool) error {
	termCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TerminalWriteGrace)
	defer cancel()
	if err := writeTerminalCostAndWakeup(termCtx, deps, terminalWriteParams{
		InstanceID:        in.InstanceID,
		EventID:           in.EventID,
		TicketID:          in.TicketUUID,
		Status:            "failed",
		ExitReason:        exitReason,
		SkipMarkProcessed: eventRetryable,
	}); err != nil {
		return fmt.Errorf("agent_container: terminal write: %w", err)
	}
	return nil
}
