package garrisonmutate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "garrison-mutate"
	serverVersion   = "1.0.0"
)

// JSON-RPC 2.0 error codes; subset we emit. Mirrors internal/finalize.
const (
	errCodeParse          = -32700
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
	errCodeInternal       = -32603
)

// Serve runs the JSON-RPC server against stdin/stdout. Returns nil on
// clean EOF (Claude closed stdin or ctx cancelled), an error on fatal
// protocol or I/O issues. Mirrors internal/finalize.Serve in shape.
func Serve(ctx context.Context, stdin io.Reader, stdout io.Writer, deps Deps) error {
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).
			With("stream", "garrison-mutate")
	}
	if deps.Pool == nil {
		return errors.New("garrisonmutate: Pool is required")
	}
	if !deps.ChatSessionID.Valid {
		return errors.New("garrisonmutate: ChatSessionID is required (GARRISON_CHAT_SESSION_ID)")
	}
	if !deps.ChatMessageID.Valid {
		return errors.New("garrisonmutate: ChatMessageID is required (GARRISON_CHAT_MESSAGE_ID)")
	}
	deps.Logger = logger
	logger.Info("garrison-mutate: starting",
		"chat_session_id", uuidString(deps.ChatSessionID),
		"chat_message_id", uuidString(deps.ChatMessageID),
		"verbs", len(Verbs),
	)
	srv := &server{deps: deps, logger: logger}
	return srv.loop(ctx, stdin, stdout)
}

type server struct {
	deps   Deps
	logger *slog.Logger
}

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
}

type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      serverInfo     `json:"serverInfo"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolsListResult struct {
	Tools []toolDescriptor `json:"tools"`
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// toolsCallResult mirrors MCP's tool-result envelope. Content is a
// single-element array carrying the JSON-encoded Result; isError flips
// true when the verb's Result.Success is false.
type toolsCallResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"` // always "text" in M5.3
	Text string `json:"text"`
}

func (s *server) loop(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	scanner := bufio.NewScanner(stdin)
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
		s.handleLine(ctx, line, enc)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("garrisonmutate: stdin scan: %w", err)
	}
	return nil
}

func (s *server) handleLine(ctx context.Context, line []byte, enc *json.Encoder) {
	var req jsonRPCRequest
	if err := json.Unmarshal(line, &req); err != nil {
		_ = enc.Encode(jsonRPCResponse{
			JSONRPC: "2.0",
			Error:   &jsonRPCError{Code: errCodeParse, Message: err.Error()},
		})
		return
	}
	resp := s.dispatch(ctx, req)
	_ = enc.Encode(resp)
}

func (s *server) dispatch(ctx context.Context, req jsonRPCRequest) jsonRPCResponse {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = initializeResult{
			ProtocolVersion: protocolVersion,
			Capabilities:    map[string]any{"tools": map[string]any{}},
			ServerInfo:      serverInfo{Name: serverName, Version: serverVersion},
		}
	case "tools/list":
		resp.Result = toolsListResult{Tools: listTools()}
	case "tools/call":
		var params toolsCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &jsonRPCError{Code: errCodeInvalidParams, Message: err.Error()}
			return resp
		}
		result, err := dispatch(ctx, s.deps, params.Name, params.Arguments)
		if err != nil {
			// Unknown verb or unhandled error — surface as JSON-RPC
			// method-not-found (MCP convention for tools/call to a
			// non-registered tool name).
			resp.Error = &jsonRPCError{Code: errCodeMethodNotFound, Message: err.Error()}
			return resp
		}
		body, mErr := json.Marshal(result)
		if mErr != nil {
			resp.Error = &jsonRPCError{Code: errCodeInternal, Message: mErr.Error()}
			return resp
		}
		resp.Result = toolsCallResult{
			Content: []toolContent{{Type: "text", Text: string(body)}},
			IsError: !result.Success,
		}
	default:
		resp.Error = &jsonRPCError{Code: errCodeMethodNotFound, Message: req.Method}
	}
	return resp
}
