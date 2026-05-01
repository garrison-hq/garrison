package garrisonmutate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// stdinFromLines turns request lines into a stdin reader.
func stdinFromLines(lines ...string) io.Reader {
	return strings.NewReader(strings.Join(lines, "\n") + "\n")
}

// validDeps returns Deps with a non-nil pool placeholder + valid IDs so
// Serve's preflight checks pass. Tests use this to drive the loop
// without a real database when no verb is invoked.
func validDeps() Deps {
	return Deps{
		Pool:          &pgxpool.Pool{},
		ChatSessionID: pgtype.UUID{Bytes: [16]byte{1, 2, 3}, Valid: true},
		ChatMessageID: pgtype.UUID{Bytes: [16]byte{4, 5, 6}, Valid: true},
	}
}

func TestServe_RejectsMissingPool(t *testing.T) {
	deps := validDeps()
	deps.Pool = nil
	err := Serve(context.Background(), strings.NewReader(""), io.Discard, deps)
	if err == nil || !strings.Contains(err.Error(), "Pool is required") {
		t.Errorf("expected Pool error; got %v", err)
	}
}

func TestServe_RejectsMissingChatSessionID(t *testing.T) {
	deps := validDeps()
	deps.ChatSessionID = pgtype.UUID{Valid: false}
	err := Serve(context.Background(), strings.NewReader(""), io.Discard, deps)
	if err == nil || !strings.Contains(err.Error(), "ChatSessionID is required") {
		t.Errorf("expected ChatSessionID error; got %v", err)
	}
}

func TestServe_RejectsMissingChatMessageID(t *testing.T) {
	deps := validDeps()
	deps.ChatMessageID = pgtype.UUID{Valid: false}
	err := Serve(context.Background(), strings.NewReader(""), io.Discard, deps)
	if err == nil || !strings.Contains(err.Error(), "ChatMessageID is required") {
		t.Errorf("expected ChatMessageID error; got %v", err)
	}
}

func TestServe_HandlesInitializeRequest(t *testing.T) {
	deps := validDeps()
	in := `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(in), &out, deps); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resp jsonRPCResponse
	if err := json.NewDecoder(&out).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("got JSON-RPC error: %+v", resp.Error)
	}
}

func TestServe_HandlesToolsListRequest(t *testing.T) {
	deps := validDeps()
	in := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"
	var out bytes.Buffer
	if err := Serve(context.Background(), strings.NewReader(in), &out, deps); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if !strings.Contains(out.String(), `"tools"`) {
		t.Errorf("response should contain tools array; got %s", out.String())
	}
}

func TestServe_RoutesMultipleRequestsInOneStream(t *testing.T) {
	deps := validDeps()
	in := stdinFromLines(
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	var out bytes.Buffer
	if err := Serve(context.Background(), in, &out, deps); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	// Two responses on separate lines.
	dec := json.NewDecoder(&out)
	for i := 0; i < 2; i++ {
		var r jsonRPCResponse
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("decode response %d: %v", i, err)
		}
		if r.Error != nil {
			t.Errorf("response %d had error: %+v", i, r.Error)
		}
	}
}

func TestServe_SkipsBlankLines(t *testing.T) {
	deps := validDeps()
	// Two blank lines around a valid request must not cause a parse
	// error response — the loop's len(line)==0 short-circuit handles
	// keepalive newlines in some MCP transports.
	in := strings.NewReader("\n\n" + `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n\n")
	var out bytes.Buffer
	if err := Serve(context.Background(), in, &out, deps); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	dec := json.NewDecoder(&out)
	var r jsonRPCResponse
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Error != nil {
		t.Errorf("blank-line handling broken; got error %+v", r.Error)
	}
}

func TestServe_EmitsParseErrorOnMalformedJSON(t *testing.T) {
	deps := validDeps()
	in := strings.NewReader("not json at all\n")
	var out bytes.Buffer
	if err := Serve(context.Background(), in, &out, deps); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var r jsonRPCResponse
	if err := json.NewDecoder(&out).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Error == nil || r.Error.Code != errCodeParse {
		t.Errorf("expected parse-error response; got %+v", r)
	}
}

func TestServe_HandlesUnknownMethod(t *testing.T) {
	deps := validDeps()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}` + "\n")
	var out bytes.Buffer
	_ = Serve(context.Background(), in, &out, deps)
	var r jsonRPCResponse
	_ = json.NewDecoder(&out).Decode(&r)
	if r.Error == nil || r.Error.Code != errCodeMethodNotFound {
		t.Errorf("expected method-not-found; got %+v", r)
	}
}

func TestServe_HandlesContextCancel(t *testing.T) {
	deps := validDeps()
	ctx, cancel := context.WithCancel(context.Background())
	// We pre-cancel so the first ctx.Done check inside the loop trips
	// and the loop returns cleanly. The line still gets written before
	// the context is checked, so we accept either zero or one response.
	cancel()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n")
	if err := Serve(ctx, in, io.Discard, deps); err != nil {
		t.Errorf("Serve should return nil on cancelled ctx; got %v", err)
	}
}

func TestServe_ReturnsCleanlyOnEmptyStdin(t *testing.T) {
	deps := validDeps()
	// Empty stdin → scanner returns false immediately → loop exits.
	if err := Serve(context.Background(), strings.NewReader(""), io.Discard, deps); err != nil {
		t.Errorf("empty stdin should return nil; got %v", err)
	}
}

func TestServe_HandlesToolsCallInvalidParams(t *testing.T) {
	deps := validDeps()
	// Malformed params for tools/call → InvalidParams error code.
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"not an object"}` + "\n")
	var out bytes.Buffer
	_ = Serve(context.Background(), in, &out, deps)
	var r jsonRPCResponse
	_ = json.NewDecoder(&out).Decode(&r)
	if r.Error == nil || r.Error.Code != errCodeInvalidParams {
		t.Errorf("expected invalid-params; got %+v", r)
	}
}

func TestServe_HandlesToolsCallUnknownVerb(t *testing.T) {
	deps := validDeps()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"not_a_real_verb","arguments":{}}}` + "\n")
	var out bytes.Buffer
	_ = Serve(context.Background(), in, &out, deps)
	var r jsonRPCResponse
	_ = json.NewDecoder(&out).Decode(&r)
	if r.Error == nil || r.Error.Code != errCodeMethodNotFound {
		t.Errorf("expected method-not-found for unknown verb; got %+v", r)
	}
}

// errReader is an io.Reader that returns a non-EOF error on the first
// Read call. Used to exercise the scanner-error branch of loop.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) {
	return 0, errors.New("read failed mid-line")
}

func TestServe_PropagatesScannerError(t *testing.T) {
	deps := validDeps()
	err := Serve(context.Background(), errReader{}, io.Discard, deps)
	if err == nil || !strings.Contains(err.Error(), "stdin scan") {
		t.Errorf("expected stdin-scan error; got %v", err)
	}
}

// TestServe_TimeoutDoesntPanic guards against a regression where the
// scanner's blocking Read on stdin blocks ctx cancellation. We just
// exercise a short-lived context — mostly a smoke test.
func TestServe_TimeoutDoesntPanic(t *testing.T) {
	deps := validDeps()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Stream of slow keepalives — eventually the deadline trips.
	in := strings.NewReader("\n\n\n\n\n\n\n")
	_ = Serve(ctx, in, io.Discard, deps)
}
