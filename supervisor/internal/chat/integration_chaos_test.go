//go:build integration

// T019 chaos tests for the M5.1 chat backend.
//
// Two scenarios:
//
//   1. Container-crash mid-stream — the dockerexec.RunStream's
//      scanStdout callback returns mid-NDJSON (simulates container
//      death between text_delta events). ChatPolicy's commit path
//      must NOT fire (no result event seen); the worker's deferred
//      'container_crashed' write should land instead.
//
//   2. Context-cancel mid-spawn — the supplied context cancels while
//      the chat container is still streaming. ChatPolicy.OnTerminate
//      must fire with the appropriate reason and terminal-write the
//      assistant row via WithoutCancel + grace per AGENTS.md rule 6.
//
// Real docker SIGTERM cascade testing (the FR-101 'kill chat container
// mid-stream' from outside the supervisor) needs the docker-proxy
// testcontainer harness which has known environment fragility. That
// test lives in supervisor/chaos_m5_1_test.go (next-session work) and
// targets supervisor + docker-proxy + mockclaude:m5 actually wired
// together end-to-end.

package chat

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
)

// truncatedDockerExec emits a system/init line + a few stream_event
// deltas, then returns mid-stream without ever sending a 'result'
// event. Simulates a container that crashed before completing.
type truncatedDockerExec struct {
	calls int
}

func (t *truncatedDockerExec) Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, []byte, error) {
	return nil, nil, errors.New("not implemented")
}

func (t *truncatedDockerExec) RunStream(ctx context.Context, args []string, writeStdin func(io.WriteCloser) error, scanStdout func(io.Reader) error) (*exec.Cmd, error) {
	t.calls++

	stdinPipeR, stdinPipeW := io.Pipe()
	go func() { _ = writeStdin(stdinPipeW); _ = stdinPipeR.Close() }()

	stdoutPipeR, stdoutPipeW := io.Pipe()
	go func() {
		// init + 1 delta, then close mid-message (no assistant, no
		// result event).
		_, _ = stdoutPipeW.Write([]byte(
			`{"type":"system","subtype":"init","cwd":"/","session_id":"sid-x","model":"claude-sonnet-4-6","tools":[],"mcp_servers":[{"name":"postgres","status":"connected"},{"name":"mempalace","status":"connected"}]}` + "\n" +
				`{"type":"stream_event","session_id":"sid-x","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}}` + "\n",
		))
		_ = stdoutPipeW.Close() // truncated — no result event
	}()
	if err := scanStdout(stdoutPipeR); err != nil {
		return nil, err
	}

	// /bin/false exits non-zero → cmd.Wait returns a *ExitError.
	cmd := exec.CommandContext(ctx, "/bin/false")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// TestM5_1_Chaos_ContainerCrashedMidStream: container produces init +
// one delta then closes stdout without a result event. The worker's
// deferred fallback path marks the assistant row 'failed' with
// error_kind='container_crashed'.
func TestM5_1_Chaos_ContainerCrashedMidStream(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)

	// Real Infisical so the vault-fetch path isn't a coverage hole.
	// Container bootup runs OUTSIDE the test's 90s ctx so a slow CI
	// runner doesn't consume the budget reserved for the actual
	// HandleMessageInSession call. Pre-M7 this was inside the ctx
	// and worked at 90s; M7's heavier integration suite pushed the
	// timing past the threshold (see M7 retro flake-fix note).
	vaultClient, customerID := chatVaultStack(t, true /* seed token */)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	exec := &truncatedDockerExec{}
	deps := minimalEdgeDeps(t, pool, q, vaultClient, nil) // exec set below
	deps.DockerExec = exec
	deps.CustomerID = customerID

	sess, _ := q.CreateChatSession(ctx, newUUID(t))
	op, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("ping"),
	})

	// Minimal mempalace wiring — values aren't dialed against a real
	// docker daemon by the truncated mock, but they must be non-empty
	// so BuildChatConfig doesn't reject the config.
	w := NewWorker(deps, "/bin/true", "postgres://test/test", MempalaceWiring{
		DockerBin: "docker", MempalaceContainer: "test-mempalace",
		PalacePath: "/palace", DockerHost: "tcp://localhost:2375",
	})
	_ = w.HandleMessageInSession(ctx, sess.ID, op.ID)

	if exec.calls != 1 {
		t.Errorf("exec.calls = %d; want 1 (one spawn attempt)", exec.calls)
	}
	st, ek, found := findAssistantRow(t, ctx, pool, sess.ID)
	if !found {
		t.Fatal("no assistant row written")
	}
	if st != "failed" {
		t.Errorf("status = %q; want failed", st)
	}
	if ek != ErrorContainerCrashed {
		t.Errorf("error_kind = %q; want %q (container truncated, no result event)",
			ek, ErrorContainerCrashed)
	}

	// Session should stay 'active' — the session is recoverable, only
	// this turn failed.
	updated, _ := q.GetChatSession(ctx, sess.ID)
	if updated.Status != "active" {
		t.Errorf("session.status = %q; want active (only the turn failed)",
			updated.Status)
	}
}

// minimalEdgeDeps allows nil DockerExec (caller sets after); the
// helper signature accepts a *fakeDockerExec but other fakes share
// the same DockerExec interface.
func init() {
	// no-op — keep this file separate from edge_cases_test.go
}
