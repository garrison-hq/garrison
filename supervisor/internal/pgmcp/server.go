// Package pgmcp implements the in-tree Postgres MCP server that Claude Code
// invokes per spawn (one stdio subprocess per invocation). Protocol is
// JSON-RPC 2.0 over stdio, restricted to the three MCP methods Claude
// calls during its lifetime: initialize, tools/list, tools/call.
//
// Exactly two tools are exposed: query and explain. Both enforce a
// SELECT/EXPLAIN-only statement filter as defense-in-depth on top of
// the garrison_agent_ro Postgres role (FR-115, NFR-104). The primary
// guarantee against DML is the role's GRANT SELECT only; the protocol
// filter is the second layer.
//
// Structured slog output goes to stderr with stream="pgmcp" so the
// supervisor's per-stream log aggregation can distinguish these lines
// from the supervisor's own stderr.
package pgmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "garrison-pgmcp"
	serverVersion   = "1.0.0"

	// MaxQueryRows caps how many rows a single query tool_call returns.
	// Past this point the result is truncated and `truncated: true` is
	// set on the response so the caller knows to narrow the query.
	MaxQueryRows = 100
)

// JSON-RPC 2.0 error codes; subset we actually emit.
const (
	errCodeParse          = -32700
	errCodeInvalidRequest = -32600
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
	errCodeInternal       = -32603
	// MCP custom code for our read-only protocol filter. The JSON-RPC
	// spec reserves -32000 to -32099 for server-defined errors.
	errCodeReadOnlyViolation = -32001
)

// Serve runs the JSON-RPC server against stdin/stdout and the given DSN.
// Returns nil on clean stdin EOF, an error on fatal protocol or I/O
// issues. Exits when ctx is cancelled.
func Serve(ctx context.Context, stdin io.Reader, stdout io.Writer, dsn string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("stream", "pgmcp")

	if dsn == "" {
		return errors.New("pgmcp: DSN is empty (set GARRISON_PGMCP_DSN)")
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("pgmcp: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	logger.Info("pgmcp connected", "server", serverName, "version", serverVersion)

	srv := &server{
		conn:   conn,
		logger: logger,
	}
	return srv.loop(ctx, stdin, stdout)
}

type server struct {
	conn   *pgx.Conn
	logger *slog.Logger
}

// jsonRPCRequest and jsonRPCResponse cover just the subset we need.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *server) loop(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	scanner := bufio.NewScanner(stdin)
	// Support long payloads (EXPLAIN output, wide rows): 1 MiB buffer.
	scanner.Buffer(make([]byte, 64*1024), 1<<20)

	enc := json.NewEncoder(stdout)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			// Notifications (no id) cannot produce a response; send an
			// error response with null id for request-shaped parses.
			if err := enc.Encode(jsonRPCResponse{JSONRPC: "2.0", Error: &jsonRPCError{Code: errCodeParse, Message: err.Error()}}); err != nil {
				return fmt.Errorf("pgmcp: write parse-error response: %w", err)
			}
			continue
		}

		resp := s.dispatch(ctx, req)
		// Notifications (no id) produce no response per JSON-RPC 2.0.
		if req.ID == nil {
			continue
		}
		resp.JSONRPC = "2.0"
		resp.ID = req.ID
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("pgmcp: write response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("pgmcp: stdin scan: %w", err)
	}
	return nil
}

// dispatch routes one request to its handler. Returns a response with
// either Result or Error populated (never both).
func (s *server) dispatch(ctx context.Context, req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize()
	case "tools/list":
		return s.handleToolsList()
	case "tools/call":
		return s.handleToolsCall(ctx, req.Params)
	case "notifications/initialized":
		// MCP clients send this notification after initialize; no-op.
		return jsonRPCResponse{}
	default:
		return jsonRPCResponse{Error: &jsonRPCError{
			Code:    errCodeMethodNotFound,
			Message: "method not found: " + req.Method,
		}}
	}
}

func (s *server) handleInitialize() jsonRPCResponse {
	return jsonRPCResponse{
		Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    serverName,
				"version": serverVersion,
			},
		},
	}
}

func (s *server) handleToolsList() jsonRPCResponse {
	return jsonRPCResponse{
		Result: map[string]any{
			"tools": []any{
				map[string]any{
					"name":        "query",
					"description": "Run a read-only SQL SELECT or EXPLAIN and return up to 100 rows.",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"sql": map[string]any{"type": "string", "description": "SELECT or EXPLAIN statement."},
						},
						"required": []string{"sql"},
					},
				},
				map[string]any{
					"name":        "explain",
					"description": "Run EXPLAIN against a SELECT statement without executing it.",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"sql": map[string]any{"type": "string", "description": "SELECT statement to explain."},
						},
						"required": []string{"sql"},
					},
				},
			},
		},
	}
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type sqlArgs struct {
	SQL string `json:"sql"`
}

func (s *server) handleToolsCall(ctx context.Context, params json.RawMessage) jsonRPCResponse {
	var p toolsCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeInvalidParams, Message: err.Error()}}
	}
	var args sqlArgs
	if len(p.Arguments) > 0 {
		if err := json.Unmarshal(p.Arguments, &args); err != nil {
			return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeInvalidParams, Message: err.Error()}}
		}
	}

	switch p.Name {
	case "query":
		return s.runQuery(ctx, args.SQL)
	case "explain":
		return s.runExplain(ctx, args.SQL)
	default:
		return jsonRPCResponse{Error: &jsonRPCError{
			Code:    errCodeMethodNotFound,
			Message: "tool not found: " + p.Name,
		}}
	}
}

func (s *server) runQuery(ctx context.Context, sql string) jsonRPCResponse {
	if err := allowReadOnly(sql); err != nil {
		s.logger.Warn("pgmcp rejected non-read-only SQL", "sql_prefix", firstWord(sql))
		return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeReadOnlyViolation, Message: err.Error()}}
	}
	rows, err := s.conn.Query(ctx, sql)
	if err != nil {
		return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeInternal, Message: err.Error()}}
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	colNames := make([]string, len(fields))
	for i, f := range fields {
		colNames[i] = string(f.Name)
	}

	out := make([]map[string]any, 0, 16)
	truncated := false
	for rows.Next() {
		if len(out) >= MaxQueryRows {
			truncated = true
			break
		}
		vals, err := rows.Values()
		if err != nil {
			return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeInternal, Message: err.Error()}}
		}
		row := make(map[string]any, len(colNames))
		for i, v := range vals {
			row[colNames[i]] = normalizeValue(v)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeInternal, Message: err.Error()}}
	}
	return jsonRPCResponse{Result: map[string]any{
		"rows":      out,
		"truncated": truncated,
	}}
}

func (s *server) runExplain(ctx context.Context, sql string) jsonRPCResponse {
	if err := allowSelectOnly(sql); err != nil {
		s.logger.Warn("pgmcp explain rejected", "sql_prefix", firstWord(sql))
		return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeReadOnlyViolation, Message: err.Error()}}
	}
	// Safe to prepend EXPLAIN because the filter confirmed the statement
	// is a bare SELECT; we do not wrap existing EXPLAINs.
	rows, err := s.conn.Query(ctx, "EXPLAIN "+sql)
	if err != nil {
		return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeInternal, Message: err.Error()}}
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeInternal, Message: err.Error()}}
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeInternal, Message: err.Error()}}
	}
	return jsonRPCResponse{Result: map[string]any{"plan": plan}}
}

// normalizeValue flattens pgx-returned values into JSON-friendly forms.
// []byte is encoded as a string; time values are handled by the default
// json marshaller.
func normalizeValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	default:
		return v
	}
}

func firstWord(sql string) string {
	s := strings.TrimSpace(sql)
	if i := strings.IndexAny(s, " \t\n\r("); i > 0 {
		return s[:i]
	}
	return s
}
