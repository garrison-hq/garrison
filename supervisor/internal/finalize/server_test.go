package finalize

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newTestServerLoop returns a server wired with a handler whose
// DB-check always reports "not yet committed." That lets server-level
// tests focus on the JSON-RPC protocol without needing a testcontainer
// Postgres (handler-DB behaviour is covered in handler_test.go via a
// mock, not exercised here).
func newTestServerLoop() *server {
	h := &Handler{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// Queries is nil; checkAlreadyCommitted will error, which
		// the handler maps to a generic schema error. For the
		// server-protocol tests below that matters only when we
		// actually call tools/call; init, tools/list, and unknown
		// method tests don't touch it.
	}
	return &server{handler: h, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// runLoop is a small harness around server.loop: writes the given
// requests to a pipe, runs the loop in a goroutine, and returns the
// accumulated stdout. Closes stdin to signal EOF after all requests.
func runLoop(t *testing.T, srv *server, requests []string) string {
	t.Helper()
	stdin := bytes.NewBufferString(strings.Join(requests, "\n") + "\n")
	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.loop(ctx, stdin, &stdout); err != nil {
		t.Fatalf("loop returned err: %v", err)
	}
	return stdout.String()
}

// decodeResponses splits stdout on newlines and decodes each as a
// JSON-RPC response. Helper for the assertion-heavy server tests.
func decodeResponses(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var out []map[string]any
	s := bufio.NewScanner(strings.NewReader(stdout))
	for s.Scan() {
		line := s.Bytes()
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// TestServerInitResponse — initialize returns a valid MCP response with
// protocolVersion and serverInfo.name == garrison-finalize.
func TestServerInitResponse(t *testing.T) {
	srv := newTestServerLoop()
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	out := runLoop(t, srv, []string{req})
	responses := decodeResponses(t, out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses; want 1", len(responses))
	}
	result, _ := responses[0]["result"].(map[string]any)
	if result == nil {
		t.Fatalf("init response missing result: %s", out)
	}
	if result["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion = %v; want %q", result["protocolVersion"], protocolVersion)
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != serverName {
		t.Errorf("serverInfo.name = %v; want %q", info["name"], serverName)
	}
}

// TestServerToolsList — tools/list returns exactly one tool named
// finalize_ticket; description contains the schema version tag.
func TestServerToolsList(t *testing.T) {
	srv := newTestServerLoop()
	req := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	out := runLoop(t, srv, []string{req})
	responses := decodeResponses(t, out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses; want 1", len(responses))
	}
	result, _ := responses[0]["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools count = %d; want 1", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "finalize_ticket" {
		t.Errorf("tool name = %v; want finalize_ticket", tool["name"])
	}
	desc, _ := tool["description"].(string)
	if !strings.Contains(desc, SchemaVersion) {
		t.Errorf("tool description missing schema version %q: %s", SchemaVersion, desc)
	}
}

// TestServerRejectsUnknownMethod — methods other than
// initialize/tools/list/tools/call/notifications/initialized return
// a JSON-RPC -32601 Method not found error.
func TestServerRejectsUnknownMethod(t *testing.T) {
	srv := newTestServerLoop()
	req := `{"jsonrpc":"2.0","id":3,"method":"something_else"}`
	out := runLoop(t, srv, []string{req})
	responses := decodeResponses(t, out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses; want 1", len(responses))
	}
	errObj, _ := responses[0]["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("expected error response; got: %s", out)
	}
	if code, _ := errObj["code"].(float64); int(code) != errCodeMethodNotFound {
		t.Errorf("error code = %v; want %d", errObj["code"], errCodeMethodNotFound)
	}
}

// TestServerExitsOnStdinEOF — when stdin closes (no more bytes),
// server.loop returns cleanly without error. This is the lifecycle
// the MCP client (Claude) drives when wrapping up a turn.
func TestServerExitsOnStdinEOF(t *testing.T) {
	srv := newTestServerLoop()
	stdin := bytes.NewBufferString("") // empty → immediate EOF
	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := srv.loop(ctx, stdin, &stdout); err != nil {
		t.Fatalf("loop returned err on EOF: %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("EOF should produce no stdout; got %q", stdout.String())
	}
}

// ----------------------------------------------------------------------------
// Serve construction-path coverage (M9 T020 top-up): the mode/deps
// validation Serve performs before entering the JSON-RPC loop. The
// pgxpool below never connects — pgxpool.New is lazy (MinConns
// defaults to 0), so a parse-valid DSN suffices for the pre-loop
// branches these tests pin.
// ----------------------------------------------------------------------------

// serveTestPool builds a lazy, never-connected pool for Serve's
// nil-check. Closed via t.Cleanup.
func serveTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://garrison:pw@127.0.0.1:1/garrison")
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func serveTestInstanceID() pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
}

// TestServeRejectsNilPool — Pool is required before any stdio is read.
func TestServeRejectsNilPool(t *testing.T) {
	err := Serve(context.Background(), strings.NewReader(""), io.Discard, Deps{
		AgentInstanceID: serveTestInstanceID(),
	})
	if err == nil || !strings.Contains(err.Error(), "Pool is required") {
		t.Fatalf("err = %v; want Pool-required error", err)
	}
}

// TestServeRejectsInvalidAgentInstanceID — the env-derived instance
// anchor is required in every mode.
func TestServeRejectsInvalidAgentInstanceID(t *testing.T) {
	err := Serve(context.Background(), strings.NewReader(""), io.Discard, Deps{
		Pool: serveTestPool(t),
	})
	if err == nil || !strings.Contains(err.Error(), "AgentInstanceID is required") {
		t.Fatalf("err = %v; want AgentInstanceID-required error", err)
	}
}

// TestServeRejectsUnknownMode — GARRISON_FINALIZE_MODE outside the
// two-value enum fails fast (FR-304: no ambiguous tool surface).
func TestServeRejectsUnknownMode(t *testing.T) {
	err := Serve(context.Background(), strings.NewReader(""), io.Discard, Deps{
		Pool:            serveTestPool(t),
		AgentInstanceID: serveTestInstanceID(),
		Mode:            "batch",
	})
	if err == nil || !strings.Contains(err.Error(), `unknown mode "batch"`) {
		t.Fatalf("err = %v; want unknown-mode error", err)
	}
}

// TestServeOneshotRequiresScheduledRunID — oneshot mode without
// GARRISON_SCHEDULED_RUN_ID cannot anchor the double-commit guard.
func TestServeOneshotRequiresScheduledRunID(t *testing.T) {
	err := Serve(context.Background(), strings.NewReader(""), io.Discard, Deps{
		Pool:            serveTestPool(t),
		AgentInstanceID: serveTestInstanceID(),
		Mode:            ModeOneshot,
	})
	if err == nil || !strings.Contains(err.Error(), "ScheduledRunID is required") {
		t.Fatalf("err = %v; want ScheduledRunID-required error", err)
	}
}

// TestServeEmptyModeDefaultsToTicketCleanEOF — the zero-value mode
// behaves as ticket (FR-302) and an immediately-EOF stdin exits Serve
// nil without touching the database.
func TestServeEmptyModeDefaultsToTicketCleanEOF(t *testing.T) {
	var out bytes.Buffer
	err := Serve(context.Background(), strings.NewReader(""), &out, Deps{
		Pool:            serveTestPool(t),
		AgentInstanceID: serveTestInstanceID(),
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Serve = %v; want nil on clean EOF", err)
	}
	if out.Len() != 0 {
		t.Errorf("unsolicited output on empty stdin: %s", out.String())
	}
}

// TestServeOneshotModeCleanEOF — a fully-specified oneshot boot (pool +
// instance + run id) reaches the loop and exits nil on EOF; combined
// with the nil-Logger default this covers the whole oneshot
// construction path without a database.
func TestServeOneshotModeCleanEOF(t *testing.T) {
	err := Serve(context.Background(), strings.NewReader(""), io.Discard, Deps{
		Pool:            serveTestPool(t),
		AgentInstanceID: serveTestInstanceID(),
		Mode:            ModeOneshot,
		ScheduledRunID:  pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
	})
	if err != nil {
		t.Fatalf("Serve = %v; want nil on clean EOF", err)
	}
}
