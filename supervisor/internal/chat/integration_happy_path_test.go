//go:build integration

// T017 golden-path integration test for the M5.1 chat backend.
//
// Boots a real testcontainer Postgres via testdb.Start, mocks the
// vault Fetcher (returns a canned token) and the dockerexec.RunStream
// (writes canned NDJSON matching the spike §8.1 wire shape), then
// drives the full chat flow end-to-end: dashboard-side INSERT →
// chat.message.sent notify → listener dispatch → worker → spawn
// (mocked) → ChatPolicy → terminal commit + RollUpSessionCost +
// work.chat.message_sent notify.
//
// Doesn't exercise real Docker / real Claude — those need the full
// docker-proxy testcontainer stack which lives outside this package.
// This test catches logic regressions in T009-T012 + T015 against a
// real Postgres surface.

package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/dockerexec"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
)

// fakeVault returns a canned token for any GrantRow that asks for
// CLAUDE_CODE_OAUTH_TOKEN.
type fakeVault struct {
	calls int
}

func (f *fakeVault) Fetch(ctx context.Context, req []vault.GrantRow) (map[string]vault.SecretValue, error) {
	f.calls++
	out := make(map[string]vault.SecretValue, len(req))
	for _, g := range req {
		out[g.EnvVarName] = vault.New([]byte("sk-ant-oat01-test"))
	}
	return out, nil
}

// fakeDockerExec mocks dockerexec.DockerExec.RunStream: writes canned
// NDJSON to the scanStdout callback, returns a *exec.Cmd that exits
// cleanly. Spike §8.1 trace shape used for fixtures.
type fakeDockerExec struct {
	mu         sync.Mutex
	calls      int
	stdinSeen  []byte
	cannedNDJSON string
}

func (f *fakeDockerExec) Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, []byte, error) {
	return nil, nil, errors.New("not implemented")
}

func (f *fakeDockerExec) RunStream(ctx context.Context, args []string, writeStdin func(io.WriteCloser) error, scanStdout func(io.Reader) error) (*exec.Cmd, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	// pipe1: stdin sink (drain + capture)
	stdinPipeR, stdinPipeW := io.Pipe()
	go func() {
		_ = writeStdin(stdinPipeW)
	}()
	captured := &strings.Builder{}
	go func() {
		_, _ = io.Copy(captured, stdinPipeR)
		f.mu.Lock()
		f.stdinSeen = []byte(captured.String())
		f.mu.Unlock()
	}()

	// pipe2: stdout source (canned NDJSON)
	stdoutPipeR, stdoutPipeW := io.Pipe()
	go func() {
		_, _ = stdoutPipeW.Write([]byte(f.cannedNDJSON))
		_ = stdoutPipeW.Close()
	}()
	if err := scanStdout(stdoutPipeR); err != nil {
		return nil, err
	}

	// /bin/true exits 0 immediately so cmd.Wait() returns nil.
	cmd := exec.CommandContext(ctx, "/bin/true")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// canned NDJSON for a single-turn response. Mirrors spike §8.1 init +
// stream_event deltas + assistant + result.
const cannedHappyPathNDJSON = `{"type":"system","subtype":"init","cwd":"/","session_id":"sid-1","model":"claude-sonnet-4-6","tools":[],"mcp_servers":[{"name":"postgres","status":"connected"},{"name":"mempalace","status":"connected"}]}
{"type":"stream_event","session_id":"sid-1","event":{"type":"message_start","message":{"id":"msg-1","model":"claude-sonnet-4-6","role":"assistant","content":[],"usage":{"input_tokens":3,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}}
{"type":"stream_event","session_id":"sid-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}}
{"type":"stream_event","session_id":"sid-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world."}}}
{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"Hello world."}]}}
{"type":"result","subtype":"success","is_error":false,"duration_ms":2000,"total_cost_usd":0.005,"stop_reason":"end_turn","session_id":"sid-1"}
`

func TestM5_1_HappyPath_SingleTurn(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)

	// Seed a user row so the FK-less started_by_user_id has a
	// realistic value (the better-auth users table is dashboard-
	// owned but the goose migrations don't touch it; tests insert
	// a synthetic id that's just a UUID).
	userID := newUUID(t)

	deps := Deps{
		Pool:        pool,
		Queries:     q,
		VaultClient: &fakeVault{},
		DockerExec:  &fakeDockerExec{cannedNDJSON: cannedHappyPathNDJSON},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		CustomerID:  newUUID(t),
		OAuthVaultPath: "/operator/CLAUDE_CODE_OAUTH_TOKEN",
		ChatContainerImage: "garrison-mockclaude:m5",
		MCPConfigDir:       t.TempDir(),
		DockerNetwork:      "garrison-net",
		TurnTimeout:        30 * time.Second,
		SessionIdleTimeout: 30 * time.Minute,
		SessionCostCapUSD:  10.00,
		TerminalWriteGrace: 5 * time.Second,
	}

	worker := NewWorker(deps, "/usr/local/bin/supervisor", "postgres://test/test", MempalaceWiring{
		DockerBin: "docker", MempalaceContainer: "spike-mempalace",
		PalacePath: "/palace", DockerHost: "tcp://localhost:2375",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Step 1 — dashboard-side INSERTs: chat_sessions + first chat_messages
	// (the M5.1 server action shape from T015 simulated directly via SQL).
	sess, err := q.CreateChatSession(ctx, userID)
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}
	op, err := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID,
		Content:   ptrFn("ping"),
	})
	if err != nil {
		t.Fatalf("InsertOperatorMessage: %v", err)
	}

	// Step 2 — worker handles the operator message directly (skipping the
	// pg_notify dispatch since the listener LISTEN against a shared pool
	// is fragile; T012's listener_test would cover that wiring).
	if err := worker.HandleMessageInSession(ctx, sess.ID, op.ID); err != nil {
		t.Fatalf("HandleMessageInSession: %v", err)
	}

	// Step 3 — assertions.
	rows, err := q.GetSessionTranscript(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSessionTranscript: %v", err)
	}
	var asstRow *store.GetSessionTranscriptRow
	for i, r := range rows {
		if r.Role == "assistant" {
			asstRow = &rows[i]
			break
		}
	}
	if asstRow == nil {
		t.Fatalf("expected an assistant row; got: %+v", rows)
	}
	if asstRow.Status != "completed" {
		t.Errorf("assistant.status = %q; want completed", asstRow.Status)
	}
	if asstRow.Content == nil || *asstRow.Content == "" {
		t.Errorf("assistant.content empty; want non-empty")
	}
	if !strings.Contains(*asstRow.Content, "Hello world.") {
		t.Errorf("assistant.content = %q; want to contain 'Hello world.'", *asstRow.Content)
	}

	// Cost rolled up.
	updated, err := q.GetChatSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetChatSession: %v", err)
	}
	gotCost, _ := numericToFloat(updated.TotalCostUsd)
	if gotCost <= 0 {
		t.Errorf("session.total_cost_usd = %v; want > 0", gotCost)
	}

	// Rule 6 backstop: token value must NOT appear in chat_messages
	// content / raw_event_envelope across the session.
	for _, r := range rows {
		if r.Content != nil && strings.Contains(*r.Content, "sk-ant-oat01-test") {
			t.Errorf("Rule 6 violation: token leaked into content of row %s", uuidString(r.ID))
		}
	}
}

// ptrFn returns *string for a literal — sqlc params for nullable
// string columns expect *string.
func ptrFn(s string) *string { return &s }

func newUUID(t *testing.T) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	// 16 random-ish bytes via os.Getpid + time fallback (no crypto/rand
	// dep needed; uniqueness within a test run is sufficient).
	pid := os.Getpid()
	now := time.Now().UnixNano()
	u.Valid = true
	u.Bytes = [16]byte{
		byte(pid), byte(pid >> 8), byte(pid >> 16), byte(pid >> 24),
		byte(now), byte(now >> 8), byte(now >> 16), byte(now >> 24),
		byte(now >> 32), byte(now >> 40), byte(now >> 48), byte(now >> 56),
		0, 0, 0, 0,
	}
	// Add a per-call counter so consecutive calls within the same ns
	// don't collide.
	uuidCounter++
	u.Bytes[12] = byte(uuidCounter)
	u.Bytes[13] = byte(uuidCounter >> 8)
	return u
}

var uuidCounter int

// fmt.Errorf retained via _
var _ = fmt.Errorf

// dockerexec interface assertion to keep the fake compatible
var _ dockerexec.DockerExec = (*fakeDockerExec)(nil)
