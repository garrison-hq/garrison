package garrisonmutate

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// runOneRequest helper: drives the server's dispatch path for a single
// JSON-RPC request without needing a real *pgxpool.Pool. The Pool field
// in Deps is required by Serve's preflight, but no stub handler reaches
// it, so we leave it nil and exercise dispatch via the server struct
// directly.
func runOneRequest(t *testing.T, req jsonRPCRequest) jsonRPCResponse {
	t.Helper()
	deps := Deps{
		ChatSessionID: pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, Valid: true},
		ChatMessageID: pgtype.UUID{Bytes: [16]byte{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}, Valid: true},
	}
	s := &server{deps: deps}
	return s.dispatch(context.Background(), req)
}

// TestServerInitializeReturnsCorrectInfo verifies the JSON-RPC
// initialize response carries the expected protocol version and server
// info envelope (MCP convention).
func TestServerInitializeReturnsCorrectInfo(t *testing.T) {
	resp := runOneRequest(t, jsonRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize"})
	if resp.Error != nil {
		t.Fatalf("initialize returned error: %v", resp.Error)
	}
	body, _ := json.Marshal(resp.Result)
	var ir initializeResult
	if err := json.Unmarshal(body, &ir); err != nil {
		t.Fatalf("unmarshal initializeResult: %v", err)
	}
	if ir.ProtocolVersion != protocolVersion {
		t.Errorf("ProtocolVersion = %q; want %q", ir.ProtocolVersion, protocolVersion)
	}
	if ir.ServerInfo.Name != serverName {
		t.Errorf("ServerInfo.Name = %q; want %q", ir.ServerInfo.Name, serverName)
	}
	if _, ok := ir.Capabilities["tools"]; !ok {
		t.Errorf("Capabilities missing 'tools' key")
	}
}

// TestServerToolsListReturnsAllVerbs verifies tools/list returns one
// descriptor per registered verb, with the names matching the registry.
func TestServerToolsListReturnsAllVerbs(t *testing.T) {
	resp := runOneRequest(t, jsonRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list"})
	if resp.Error != nil {
		t.Fatalf("tools/list returned error: %v", resp.Error)
	}
	body, _ := json.Marshal(resp.Result)
	var tl toolsListResult
	if err := json.Unmarshal(body, &tl); err != nil {
		t.Fatalf("unmarshal toolsListResult: %v", err)
	}
	if len(tl.Tools) != len(Verbs) {
		t.Errorf("tools/list returned %d tools; want %d", len(tl.Tools), len(Verbs))
	}
	got := map[string]struct{}{}
	for _, td := range tl.Tools {
		got[td.Name] = struct{}{}
	}
	for _, v := range Verbs {
		if _, ok := got[v.Name]; !ok {
			t.Errorf("tools/list missing verb %q", v.Name)
		}
	}
}

// TestServerToolsCallRejectsUnknownVerb pins Rule 1: any unregistered
// verb name returns JSON-RPC method-not-found.
func TestServerToolsCallRejectsUnknownVerb(t *testing.T) {
	params, _ := json.Marshal(toolsCallParams{Name: "rotate_secret", Arguments: json.RawMessage(`{}`)})
	resp := runOneRequest(t, jsonRPCRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`3`),
		Method: "tools/call", Params: params,
	})
	if resp.Error == nil {
		t.Fatal("tools/call to unknown verb did not return error")
	}
	if resp.Error.Code != errCodeMethodNotFound {
		t.Errorf("error.code = %d; want %d (method-not-found)", resp.Error.Code, errCodeMethodNotFound)
	}
}

// TestServerToolsCallDispatchesToHandler verifies a registered verb's
// stub handler runs and returns its Result via the MCP toolsCallResult
// envelope.
func TestServerToolsCallDispatchesToHandler(t *testing.T) {
	params, _ := json.Marshal(toolsCallParams{Name: "create_ticket", Arguments: json.RawMessage(`{}`)})
	resp := runOneRequest(t, jsonRPCRequest{
		JSONRPC: "2.0", ID: json.RawMessage(`4`),
		Method: "tools/call", Params: params,
	})
	if resp.Error != nil {
		t.Fatalf("tools/call returned error: %v", resp.Error)
	}
	body, _ := json.Marshal(resp.Result)
	var tc toolsCallResult
	if err := json.Unmarshal(body, &tc); err != nil {
		t.Fatalf("unmarshal toolsCallResult: %v", err)
	}
	if !tc.IsError {
		t.Errorf("stub handler returned Success=true; want false (T004 stub)")
	}
	if len(tc.Content) != 1 || tc.Content[0].Type != "text" {
		t.Errorf("Content shape unexpected: %+v", tc.Content)
	}
	// The stub handler returns ErrValidationFailed; assert the result
	// JSON contains the typed error_kind.
	if !strings.Contains(tc.Content[0].Text, string(ErrValidationFailed)) {
		t.Errorf("Content text missing %q: %q", ErrValidationFailed, tc.Content[0].Text)
	}
}

// TestServerRejectsUnknownMethod pins JSON-RPC method dispatch: an
// unknown top-level method returns method-not-found.
func TestServerRejectsUnknownMethod(t *testing.T) {
	resp := runOneRequest(t, jsonRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`5`), Method: "wat"})
	if resp.Error == nil || resp.Error.Code != errCodeMethodNotFound {
		t.Errorf("wat: got error=%+v; want method-not-found", resp.Error)
	}
}

// TestServerLoopReturnsCleanlyOnEOF asserts the stdin loop exits with
// nil when the producer closes stdin (no requests exchanged).
func TestServerLoopReturnsCleanlyOnEOF(t *testing.T) {
	deps := Deps{
		Pool:          nil, // bypass for the loop-only path
		ChatSessionID: pgtype.UUID{Valid: true, Bytes: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}},
		ChatMessageID: pgtype.UUID{Valid: true, Bytes: [16]byte{2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17}},
	}
	srv := &server{deps: deps}
	var out bytes.Buffer
	if err := srv.loop(context.Background(), strings.NewReader(""), &out); err != nil {
		t.Errorf("loop on empty stdin returned error: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("empty stdin produced output: %q", out.String())
	}
}

// TestServerLoopProcessesRequest exercises the full bufio loop for one
// request line (initialize) and asserts the response shape.
func TestServerLoopProcessesRequest(t *testing.T) {
	deps := Deps{
		ChatSessionID: pgtype.UUID{Valid: true, Bytes: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}},
		ChatMessageID: pgtype.UUID{Valid: true, Bytes: [16]byte{2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17}},
	}
	srv := &server{deps: deps}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n")
	var out bytes.Buffer
	if err := srv.loop(context.Background(), in, &out); err != nil {
		t.Fatalf("loop: %v", err)
	}
	if !strings.Contains(out.String(), `"protocolVersion":"`+protocolVersion+`"`) {
		t.Errorf("response missing protocolVersion: %q", out.String())
	}
}
