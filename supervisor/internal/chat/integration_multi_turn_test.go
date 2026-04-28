//go:build integration

// T018 multi-turn context-fidelity test (the SC-002 cache-read-input-
// tokens assertion). Uses the chat-mode mockclaude binary (T013
// extension) to run the supervisor's full chat path end-to-end with
// turn-aware response selection ("favorite color" → "Purple.") and
// cache_read emission on turn ≥ 2.

package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

// realMockclaudeExec runs the actual mockclaude Go binary as a
// subprocess, piping the supervisor's stdin → mockclaude → stdout
// back to scanStdout. Unlike the in-process fakeDockerExec, this
// exercises the chat-mode mockclaude entrypoint end-to-end and lets
// us assert against the real cache_read_input_tokens emission.
type realMockclaudeExec struct {
	binPath string
	calls   int
}

func (r *realMockclaudeExec) Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, []byte, error) {
	return nil, nil, errors.New("not implemented")
}

func (r *realMockclaudeExec) RunStream(ctx context.Context, args []string, writeStdin func(io.WriteCloser) error, scanStdout func(io.Reader) error) (*exec.Cmd, error) {
	r.calls++
	// The chat path constructs:
	//   docker run --rm -i -e ... -v ... --network <name> <image> <claude flags>
	// First positional after `--network <name>` is the image; flags
	// after that are what mockclaude consumes.
	imageIdx := -1
	for i, a := range args {
		_ = a
		if i > 0 && args[i-1] == "--network" {
			imageIdx = i
			break
		}
	}
	if imageIdx < 0 || imageIdx >= len(args)-1 {
		return nil, errors.New("realMockclaudeExec: image arg not found")
	}
	claudeFlags := args[imageIdx+1:]

	cmd := exec.CommandContext(ctx, r.binPath, claudeFlags...)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return nil, err
	}
	cmd.Stderr = os.Stderr // surface mockclaude warnings during test debugging
	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		return nil, err
	}
	if err := writeStdin(stdinPipe); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	if err := scanStdout(stdoutPipe); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	return cmd, nil
}

// TestM5_1_MultiTurn_ContextFidelity drives a 2-turn conversation
// using the real mockclaude binary in chat mode. Asserts:
//   - turn 2's content contains "purple" (favorite-color fixture)
//   - turn 2's raw_event_envelope contains a message_start event with
//     cache_read_input_tokens > 0 (SC-002 — proves the supervisor
//     replayed the prior turn into stdin)
//   - chat_sessions.total_cost_usd > 0 (SC-003)
func TestM5_1_MultiTurn_ContextFidelity(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Build the mockclaude binary once for this test.
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "mockclaude")
	build := exec.CommandContext(ctx, "go", "build", "-o", binPath,
		"github.com/garrison-hq/garrison/supervisor/internal/spawn/mockclaude")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build mockclaude: %v", err)
	}

	vaultClient, customerID := chatVaultStack(t, true)
	deps := minimalEdgeDeps(t, pool, q, vaultClient, nil)
	deps.DockerExec = &realMockclaudeExec{binPath: binPath}
	deps.CustomerID = customerID

	w := NewWorker(deps, "/bin/true", "postgres://test/test", MempalaceWiring{
		DockerBin: "docker", MempalaceContainer: "test-mempalace",
		PalacePath: "/palace", DockerHost: "tcp://localhost:2375",
	})

	sess, _ := q.CreateChatSession(ctx, newUUID(t))

	// Turn 1: state the favorite color.
	op1, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("My favorite color is purple."),
	})
	if err := w.HandleMessageInSession(ctx, sess.ID, op1.ID); err != nil {
		t.Fatalf("turn 1: %v", err)
	}

	// Turn 2: ask about it.
	op2, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("What is my favorite color?"),
	})
	if err := w.HandleMessageInSession(ctx, sess.ID, op2.ID); err != nil {
		t.Fatalf("turn 2: %v", err)
	}

	// Read all messages directly (transcript filter excludes failed/aborted).
	rows, err := pool.Query(ctx,
		`SELECT id, role, status, content, raw_event_envelope, COALESCE(error_kind, '')
		   FROM chat_messages WHERE session_id = $1
		  ORDER BY turn_index ASC`, sess.ID)
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	defer rows.Close()

	type msgRow struct {
		role, status string
		content      *string
		envelope     []byte
		errorKind    string
	}
	var all []msgRow
	for rows.Next() {
		var m msgRow
		var id pgtype.UUID
		if err := rows.Scan(&id, &m.role, &m.status, &m.content, &m.envelope, &m.errorKind); err != nil {
			t.Fatalf("scan: %v", err)
		}
		all = append(all, m)
	}
	for i, m := range all {
		t.Logf("row %d: role=%s status=%s error_kind=%s content=%q", i, m.role, m.status, m.errorKind, ptrValMT(m.content))
	}
	if len(all) != 4 {
		t.Fatalf("got %d messages; want 4 (op/asst/op/asst): %+v", len(all), all)
	}

	turn2Asst := all[3]
	if turn2Asst.role != "assistant" || turn2Asst.status != "completed" {
		t.Fatalf("turn 2 assistant row: role=%q status=%q", turn2Asst.role, turn2Asst.status)
	}
	if turn2Asst.content == nil || !strings.Contains(strings.ToLower(*turn2Asst.content), "purple") {
		t.Errorf("turn 2 content = %q; want to contain 'purple'", ptrValMT(turn2Asst.content))
	}

	// SC-002: cache_read_input_tokens > 0 in the raw envelope.
	cacheRead := extractCacheRead(t, turn2Asst.envelope)
	if cacheRead <= 0 {
		t.Errorf("turn 2 cache_read_input_tokens = %d; want > 0 (proves prefix replay)", cacheRead)
	}

	// SC-003: cost rolled up.
	updated, _ := q.GetChatSession(ctx, sess.ID)
	gotCost, _ := numericToFloatMT(updated.TotalCostUsd)
	if gotCost <= 0 {
		t.Errorf("session total cost = %v; want > 0", gotCost)
	}
}

func extractCacheRead(t *testing.T, envelope []byte) int {
	t.Helper()
	if len(envelope) == 0 {
		return 0
	}
	var events []json.RawMessage
	if err := json.Unmarshal(envelope, &events); err != nil {
		t.Logf("envelope not array: %v; len=%d", err, len(envelope))
		return 0
	}
	for _, ev := range events {
		var typed struct {
			Type  string `json:"type"`
			Event struct {
				Type    string `json:"type"`
				Message struct {
					Usage struct {
						CacheRead int `json:"cache_read_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			} `json:"event"`
		}
		if err := json.Unmarshal(ev, &typed); err != nil {
			continue
		}
		if typed.Type == "stream_event" && typed.Event.Type == "message_start" {
			return typed.Event.Message.Usage.CacheRead
		}
	}
	return 0
}

func ptrValMT(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func numericToFloatMT(n pgtype.Numeric) (float64, error) {
	if !n.Valid {
		return 0, nil
	}
	f, err := n.Float64Value()
	if err != nil {
		return 0, err
	}
	if !f.Valid {
		return 0, nil
	}
	return f.Float64, nil
}
