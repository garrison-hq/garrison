//go:build integration

package pgmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
)

// runAgainst spins pgmcp.Serve in a goroutine, reads its responses line-
// by-line, and returns a send/receive pair for the test.
func runAgainst(t *testing.T, dsn string) (send func([]byte), recv func() jsonRPCResponse, stop func()) {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = Serve(ctx, stdinR, stdoutW, dsn)
		_ = stdoutW.Close()
	}()

	outCh := make(chan []byte, 8)
	go func() {
		buf := bytes.NewBuffer(nil)
		tmp := make([]byte, 4096)
		for {
			n, err := stdoutR.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
				for {
					line, err := buf.ReadBytes('\n')
					if err != nil {
						buf.Write(line)
						break
					}
					outCh <- line
				}
			}
			if err != nil {
				close(outCh)
				return
			}
		}
	}()

	send = func(b []byte) {
		if _, err := stdinW.Write(append(b, '\n')); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	recv = func() jsonRPCResponse {
		select {
		case line, ok := <-outCh:
			if !ok {
				t.Fatalf("pgmcp closed stdout before response")
			}
			var r jsonRPCResponse
			if err := json.Unmarshal(line, &r); err != nil {
				t.Fatalf("unmarshal response %q: %v", line, err)
			}
			return r
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for response")
			return jsonRPCResponse{}
		}
	}
	stop = func() {
		_ = stdinW.Close()
		cancel()
		wg.Wait()
	}
	return
}

// TestPgmcpQueryHappyPath runs an initialize + tools/list + tools/call
// sequence against a real Postgres container and verifies the query tool
// returns rows.
func TestPgmcpQueryHappyPath(t *testing.T) {
	pool := testdb.Start(t)
	dsn := pool.Config().ConnString()

	send, recv, stop := runAgainst(t, dsn)
	defer stop()

	send([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	r := recv()
	if r.Error != nil {
		t.Fatalf("initialize: %+v", r.Error)
	}

	send([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	r = recv()
	if r.Error != nil {
		t.Fatalf("tools/list: %+v", r.Error)
	}
	resultMap, _ := r.Result.(map[string]any)
	tools, _ := resultMap["tools"].([]any)
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}

	send([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"query","arguments":{"sql":"SELECT 1 AS one"}}}`))
	r = recv()
	if r.Error != nil {
		t.Fatalf("tools/call query: %+v", r.Error)
	}
	rm, _ := r.Result.(map[string]any)
	rows, _ := rm["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row, _ := rows[0].(map[string]any)
	if row["one"] == nil {
		t.Errorf("expected 'one' column; got %v", row)
	}
}

// TestPgmcpRejectsDML verifies the protocol-layer filter returns a
// read-only-violation error for INSERT, without the query ever reaching
// Postgres. (Even if the filter were bypassed, the role's lack of INSERT
// privilege would also reject — two-layer defense per NFR-104.)
func TestPgmcpRejectsDML(t *testing.T) {
	pool := testdb.Start(t)
	send, recv, stop := runAgainst(t, pool.Config().ConnString())
	defer stop()

	send([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	_ = recv()

	send([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"query","arguments":{"sql":"INSERT INTO tickets (department_id, objective) VALUES ('00000000-0000-0000-0000-000000000000', 'mischief')"}}}`))
	r := recv()
	if r.Error == nil {
		t.Fatalf("expected read-only-violation error; got result=%+v", r.Result)
	}
	if r.Error.Code != errCodeReadOnlyViolation {
		t.Errorf("expected code %d, got %d (message %q)", errCodeReadOnlyViolation, r.Error.Code, r.Error.Message)
	}
	if !strings.Contains(r.Error.Message, "read-only violation") {
		t.Errorf("error message: got %q", r.Error.Message)
	}
}

// TestPgmcpRejectsDDL same idea for DDL.
func TestPgmcpRejectsDDL(t *testing.T) {
	pool := testdb.Start(t)
	send, recv, stop := runAgainst(t, pool.Config().ConnString())
	defer stop()

	send([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	_ = recv()

	send([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"query","arguments":{"sql":"DROP TABLE tickets"}}}`))
	r := recv()
	if r.Error == nil || r.Error.Code != errCodeReadOnlyViolation {
		t.Fatalf("expected read-only-violation; got %+v", r)
	}
}

// TestPgmcpExplainRunsAgainstRealTable verifies the explain tool composes
// EXPLAIN and runs it, returning a plan.
func TestPgmcpExplainRunsAgainstRealTable(t *testing.T) {
	pool := testdb.Start(t)
	send, recv, stop := runAgainst(t, pool.Config().ConnString())
	defer stop()

	send([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	_ = recv()

	send([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"explain","arguments":{"sql":"SELECT id FROM departments"}}}`))
	r := recv()
	if r.Error != nil {
		t.Fatalf("explain: %+v", r.Error)
	}
	rm, _ := r.Result.(map[string]any)
	plan, _ := rm["plan"].([]any)
	if len(plan) == 0 {
		t.Errorf("expected non-empty plan, got %+v", r.Result)
	}
}

// TestPgmcpUnknownMethodReturnsMethodNotFound pins JSON-RPC -32601
// behaviour for anything other than initialize / tools/list / tools/call
// / notifications/initialized.
func TestPgmcpUnknownMethodReturnsMethodNotFound(t *testing.T) {
	pool := testdb.Start(t)
	send, recv, stop := runAgainst(t, pool.Config().ConnString())
	defer stop()

	send([]byte(`{"jsonrpc":"2.0","id":1,"method":"quantum/teleport","params":{}}`))
	r := recv()
	if r.Error == nil || r.Error.Code != errCodeMethodNotFound {
		t.Fatalf("expected method-not-found; got %+v", r)
	}
}
