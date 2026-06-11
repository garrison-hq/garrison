//go:build chaos

package spawn

// M7.1 T015 — chaos suite against the LIVE dev stack: real docker
// behind the real socket-proxy, the real squid egress proxy, and the
// real claude binary inside the agent image. Where the T013 integration
// suite cans the daemon and the stream, these tests exercise the
// failure surfaces that only exist with real network + container
// semantics:
//
//   - the FR-016 timeout ladder under an egress blackhole (SC-006;
//     spike F3's hang-not-fail class is structurally prevented by the
//     in-container coreutils timeout),
//   - the fast-fail of a proxy-denied CONNECT (clarification
//     2026-06-10: squid 403 → terminal result frame with
//     is_error=true in ~1 s — it never rides the timeout),
//   - the Restart backstop returning the container to its idle
//     `sleep infinity` PID 1 while tearing down the in-flight exec.
//
// Gate: GARRISON_CHAOS_DOCKER must be set; without it every test skips
// cleanly (the M7 chaos-suite convention). With it set, the harness
// additionally needs GARRISON_DOCKER_SOCKET_PROXY_URL pointing at the
// live docker-proxy from wherever the test process runs — the proxy
// listens on the compose network only, so from the host use the proxy
// container's bridge IP (e.g. http://172.20.0.3:2375). A missing URL
// with the gate set is a hard failure, not a skip: the operator asked
// for a chaos run and a silent skip would let T017's acceptance walk
// pass vacuously.
//
// Each test creates one dedicated chaos container through the
// production SpecForAgent + Controller path (same shape family as the
// live fleet; fixed agent ID so repeat runs reuse a single root-owned
// workspace dir under the workspace FS) and force-removes it on
// cleanup. Live agent containers are never touched.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/agentcontainer"
)

const (
	// chaosAgentID keys the dedicated chaos container
	// (garrison-agent-c4a05000) and its workspace dir. Fixed, not
	// random: chaos runs are operator-serial, and a stable ID keeps
	// the docker-created root-owned workspace dir count at one.
	chaosAgentID = "c4a05000-7e57-4c05-8000-000000000000"

	// chaosDroppingProxy is the SC-006 blackhole stand-in: the name
	// resolves on the agents network (the squid container) but nothing
	// listens on this port, so the CONNECT attempt fails at transport
	// level with no HTTP response — the class claude retries past any
	// budget (spike F3; verified live 2026-06-11 as a 10-attempt
	// api_retry backoff that exit-124s exactly at the wrapper budget).
	// Distinct from a denied CONNECT, where squid answers 403 and
	// claude fails fast (clarification 2026-06-10).
	chaosDroppingProxy = "http://garrison-egress-proxy:9999"

	// chaosDeniedBaseURL points claude's API traffic at a host the
	// live squid allow-list (api.anthropic.com only, FR-009) denies.
	// example.com is RFC 2606 reserved — never allow-listed.
	chaosDeniedBaseURL = "https://chaos-denied.example.com"

	// chaosDummyAPIKey satisfies claude's "have credentials, attempt
	// the call" requirement. It never reaches Anthropic: the blackhole
	// drops the CONNECT and the denied path 403s before any auth
	// exchange. Deliberately a dummy — the chaos suite needs no real
	// secrets.
	chaosDummyAPIKey = "sk-ant-chaos-dummy"

	// chaosBlackholeBudget is the short in-container timeout budget
	// for the blackhole test (vs the production SubprocessTimeout
	// default of minutes).
	chaosBlackholeBudget = 20 * time.Second
)

func requireChaosDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("GARRISON_CHAOS_DOCKER") == "" {
		t.Skip("set GARRISON_CHAOS_DOCKER=1 to opt in to chaos tests that mutate live docker state")
	}
}

func chaosEnvOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// chaosController builds the production socket-proxy Controller against
// the live docker-proxy. The URL is mandatory once the chaos gate is
// set (see file header).
func chaosController(t *testing.T) agentcontainer.Controller {
	t.Helper()
	url := os.Getenv("GARRISON_DOCKER_SOCKET_PROXY_URL")
	if url == "" {
		t.Fatal("GARRISON_CHAOS_DOCKER is set but GARRISON_DOCKER_SOCKET_PROXY_URL is not; " +
			"point it at the live docker-proxy (compose-network address, e.g. http://172.20.0.3:2375 from the host)")
	}
	return agentcontainer.NewSocketProxyController(url, nil, nil)
}

// startChaosContainer creates + starts the dedicated chaos container
// through the production spec source and registers a force-remove
// cleanup. SupervisorBin stays empty: no chaos exec runs an in-container
// MCP server, and an empty field skips the bind rather than mounting a
// host path the test cannot guarantee.
func startChaosContainer(t *testing.T, ctrl agentcontainer.Controller) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	image := chaosEnvOr("GARRISON_AGENT_CONTAINER_IMAGE", "garrison-claude:m5")
	digest, err := ctrl.ImageDigest(ctx, image)
	if err != nil {
		t.Fatalf("ImageDigest(%s): %v", image, err)
	}
	spec := agentcontainer.SpecForAgent(agentcontainer.AgentSpecParams{
		AgentID:     chaosAgentID,
		RoleSlug:    "chaos-probe",
		ImageDigest: digest,
		HostUID:     1900, // inside the FR-206a range; collision with a live allocation is harmless for a probe container
		WorkspaceFS: chaosEnvOr("GARRISON_AGENT_WORKSPACE_FS", "/var/lib/garrison/workspaces"),
		SkillsFS:    chaosEnvOr("GARRISON_AGENT_SKILLS_FS", "/var/lib/garrison/skills"),
		NetworkName: chaosEnvOr("GARRISON_AGENTS_NETWORK", "garrison-agents"),
		Memory:      "512m",
		CPUs:        "1.0",
		PIDsLimit:   200,
	})

	// A leftover container from an aborted run blocks Create with a
	// name conflict; force-remove is a no-op error when absent.
	name := agentcontainer.ContainerName(chaosAgentID)
	_ = ctrl.Remove(ctx, name)

	id, err := ctrl.Create(ctx, spec)
	if err != nil {
		t.Fatalf("Create chaos container: %v", err)
	}
	t.Cleanup(func() {
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rmCancel()
		if rmErr := ctrl.Remove(rmCtx, id); rmErr != nil {
			t.Logf("cleanup: remove chaos container: %v", rmErr)
		}
	})
	if err := ctrl.Start(ctx, id); err != nil {
		t.Fatalf("Start chaos container: %v", err)
	}
	return name
}

// chaosClaudeEnv mirrors containerExecEnv's fixed set (plan D17) with a
// dummy API key instead of the supervisor's auth passthrough + vault
// values — the chaos failure classes trigger before any credential is
// validated.
func chaosClaudeEnv(httpsProxy string, extra ...string) []string {
	env := []string{
		"HOME=/home/node",
		"HTTPS_PROXY=" + httpsProxy,
		"DISABLE_TELEMETRY=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"ENABLE_TOOL_SEARCH=false",
		"ANTHROPIC_API_KEY=" + chaosDummyAPIKey,
	}
	return append(env, extra...)
}

// chaosClaudeCmd wraps a minimal claude -p invocation in the production
// FR-016 coreutils-timeout prefix (same shape m7.go builds). The full
// argv contract is pinned elsewhere (TestContainerArgvMatchesDirectExecFlagSet);
// chaos only needs claude to start and attempt egress.
func chaosClaudeCmd(budget time.Duration) []string {
	return []string{
		"/usr/bin/timeout",
		"--signal=TERM",
		fmt.Sprintf("--kill-after=%ds", int(timeoutKillGrace/time.Second)),
		strconv.Itoa(int(budget / time.Second)),
		containerClaudeBin,
		"-p", "Reply with the single word ok.",
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
	}
}

// chaosExec runs one exec to completion, capturing stdout (the demuxed
// stream 1) and discarding stderr on its own goroutine so an unbuffered
// demux pipe can never stall the stdout drain — the runHelperExec
// pattern with capture.
func chaosExec(ctx context.Context, ctrl agentcontainer.Controller, containerID string, spec agentcontainer.ExecSpec) (int, string, error) {
	sess, err := ctrl.Exec(ctx, containerID, spec)
	if err != nil {
		return -1, "", err
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
	var out bytes.Buffer
	_, _ = io.Copy(&out, sess.Stdout)
	<-stderrDone
	code, err := sess.ExitCode(ctx)
	return code, out.String(), err
}

// TestBlackholeEgressTerminatesWithinBudget — SC-006. With HTTPS_PROXY
// pointed at a dropping endpoint, claude retries forever (spike F3
// class); the in-container timeout wrapper must terminate it at the
// budget with exit 124, and the production mapping must classify the
// exit as the timeout class. The supervisor-side execCtx backstop never
// has to fire — the wrapper always goes first (FR-016 ladder).
func TestBlackholeEgressTerminatesWithinBudget(t *testing.T) {
	requireChaosDocker(t)
	ctrl := chaosController(t)
	name := startChaosContainer(t, ctrl)

	budget := chaosBlackholeBudget
	execCtx, cancel := context.WithTimeout(context.Background(), budget+timeoutKillGrace+containerCtxSlack)
	defer cancel()

	start := time.Now()
	code, _, err := chaosExec(execCtx, ctrl, name, agentcontainer.ExecSpec{
		Cmd:        chaosClaudeCmd(budget),
		Env:        chaosClaudeEnv(chaosDroppingProxy),
		WorkingDir: "/workspace",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("blackhole exec: %v (after %v)", err, elapsed)
	}
	if code != 124 {
		t.Fatalf("exit code = %d after %v; want 124 (coreutils timeout TERM at budget)", code, elapsed)
	}
	if elapsed < budget {
		t.Errorf("claude exited 124 after only %v, before the %v budget — wrapper timing premise broken", elapsed, budget)
	}
	if max := budget + timeoutKillGrace + 15*time.Second; elapsed > max {
		t.Errorf("termination took %v; want within %v (budget + kill grace + slack) — SC-006 bound", elapsed, max)
	}

	// The production classification of that observation: exec-inspect
	// 124 → the timeout-budget WaitDetail → exit_reason=timeout.
	wait := containerWaitDetail(code, nil, nil)
	status, reason := Adjudicate(Result{}, wait, true, FinalizeState{})
	if status != "timeout" || reason != ExitTimeout {
		t.Errorf("Adjudicate(exit 124) = (%s, %s); want (timeout, %s)", status, reason, ExitTimeout)
	}
}

// TestDeniedConnectFailsFast — clarification 2026-06-10. A CONNECT the
// live squid denies is NOT a blackhole: squid answers 403, claude
// surfaces a terminal result frame with is_error=true within seconds
// and exits without riding the timeout. The exit adjudicates in the
// claude-error class through the production mapping.
func TestDeniedConnectFailsFast(t *testing.T) {
	requireChaosDocker(t)
	ctrl := chaosController(t)
	name := startChaosContainer(t, ctrl)

	egress := chaosEnvOr("GARRISON_EGRESS_PROXY_URL", "http://garrison-egress-proxy:3128")
	wrapperBudget := 60 * time.Second
	execCtx, cancel := context.WithTimeout(context.Background(), wrapperBudget+timeoutKillGrace+containerCtxSlack)
	defer cancel()

	start := time.Now()
	code, stdout, err := chaosExec(execCtx, ctrl, name, agentcontainer.ExecSpec{
		Cmd:        chaosClaudeCmd(wrapperBudget),
		Env:        chaosClaudeEnv(egress, "ANTHROPIC_BASE_URL="+chaosDeniedBaseURL),
		WorkingDir: "/workspace",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("denied-CONNECT exec: %v (after %v)", err, elapsed)
	}
	if code == 124 || code == 137 {
		t.Fatalf("denied CONNECT rode the timeout (exit %d after %v); want a fast claude-error exit", code, elapsed)
	}
	if elapsed > 30*time.Second {
		t.Errorf("denied CONNECT took %v to exit; want within seconds (probed ~1 s live)", elapsed)
	}

	// The terminal result frame must be present with is_error=true —
	// the evidence the pipeline's adjudication runs on.
	var resultSeen, resultIsError bool
	for _, line := range strings.Split(stdout, "\n") {
		if line == "" {
			continue
		}
		var frame struct {
			Type    string `json:"type"`
			IsError bool   `json:"is_error"`
		}
		if json.Unmarshal([]byte(line), &frame) != nil {
			continue
		}
		if frame.Type == "result" {
			resultSeen = true
			resultIsError = frame.IsError
		}
	}
	if !resultSeen || !resultIsError {
		t.Fatalf("no terminal result frame with is_error=true on stdout (result seen=%v is_error=%v); stdout:\n%s",
			resultSeen, resultIsError, stdout)
	}

	wait := containerWaitDetail(code, nil, nil)
	status, reason := Adjudicate(Result{ResultSeen: true, IsError: true}, wait, true, FinalizeState{})
	if status != "failed" || reason != ExitClaudeError {
		t.Errorf("Adjudicate(denied CONNECT) = (%s, %s); want (failed, %s)", status, reason, ExitClaudeError)
	}
}

// TestRestartBackstopRestoresIdleSleep — FR-016's SIGKILL analog. A
// Restart issued while an exec is in flight must (a) tear down the
// exec's stream and (b) bring the container back up with the idle
// `sleep infinity` as PID 1, ready for the next spawn.
func TestRestartBackstopRestoresIdleSleep(t *testing.T) {
	requireChaosDocker(t)
	ctrl := chaosController(t)
	name := startChaosContainer(t, ctrl)

	execCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// The mid-flight exec: a sleep long enough that only the restart
	// can end it inside the test window.
	sess, err := ctrl.Exec(execCtx, name, agentcontainer.ExecSpec{
		Cmd: []string{"/bin/sleep", "300"},
	})
	if err != nil {
		t.Fatalf("in-flight exec: %v", err)
	}
	defer func() {
		_ = sess.Stdout.Close()
		_ = sess.Stderr.Close()
	}()
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		stderrDone := make(chan struct{})
		go func() {
			defer close(stderrDone)
			_, _ = io.Copy(io.Discard, sess.Stderr)
		}()
		_, _ = io.Copy(io.Discard, sess.Stdout)
		<-stderrDone
	}()

	restartCtx, restartCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer restartCancel()
	if err := ctrl.Restart(restartCtx, name); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	// (a) the in-flight exec's stream EOFs — the restart killed it.
	select {
	case <-streamDone:
	case <-time.After(30 * time.Second):
		t.Fatal("in-flight exec stream did not EOF within 30s of Restart")
	}

	// (b) PID 1 is the idle sleep again. The container may take a
	// moment to come back; exec-create 409s against a not-yet-running
	// container, so poll.
	deadline := time.Now().Add(45 * time.Second)
	for {
		pollCtx, pollCancel := context.WithTimeout(context.Background(), 15*time.Second)
		code, out, err := chaosExec(pollCtx, ctrl, name, agentcontainer.ExecSpec{
			Cmd: []string{"/bin/cat", "/proc/1/cmdline"},
		})
		pollCancel()
		// /proc/1/cmdline is NUL-separated: "/bin/sleep\x00infinity\x00".
		if err == nil && code == 0 && strings.Contains(out, "/bin/sleep\x00infinity") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("PID 1 not back to idle sleep within 45s of Restart (last: code=%d out=%q err=%v)", code, out, err)
		}
		time.Sleep(time.Second)
	}
}
