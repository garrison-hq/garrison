package finalize

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "garrison-finalize"
	serverVersion   = "1.0.0"
)

// JSON-RPC 2.0 error codes; subset we emit.
const (
	errCodeParse          = -32700
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
)

// Server modes (M9). The mode selects which single tool the server
// exposes: ticket mode serves finalize_ticket exactly as M2.2.1
// shipped it (byte-for-byte, FR-302); oneshot mode serves
// finalize_oneshot. tools/list returns exactly one descriptor per
// mode, so an oneshot agent structurally cannot see or call
// finalize_ticket and vice versa (FR-304).
const (
	ModeTicket  = "ticket"
	ModeOneshot = "oneshot"
)

// Deps bundles the server's construction inputs so Serve's signature
// stays small. Callers populate Pool + AgentInstanceID from env; Logger
// is optional (stderr default).
//
// M9: Mode defaults to ModeTicket when empty (GARRISON_FINALIZE_MODE);
// ScheduledRunID is required in oneshot mode (GARRISON_SCHEDULED_RUN_ID)
// and anchors the oneshot double-commit guard
// (SelectScheduledTaskRunFinalizedState, FR-260 analog).
type Deps struct {
	Pool            *pgxpool.Pool
	AgentInstanceID pgtype.UUID
	Logger          *slog.Logger

	Mode           string
	ScheduledRunID pgtype.UUID
}

// Serve runs the JSON-RPC server against stdin/stdout. Returns nil on
// clean EOF (agent closed stdin or ctx cancelled), an error on fatal
// protocol or I/O issues. Exits when ctx is cancelled.
func Serve(ctx context.Context, stdin io.Reader, stdout io.Writer, deps Deps) error {
	logger := deps.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).
			With("stream", "finalize")
	}
	if deps.Pool == nil {
		return errors.New("finalize: Pool is required")
	}
	if !deps.AgentInstanceID.Valid {
		return errors.New("finalize: AgentInstanceID is required (GARRISON_AGENT_INSTANCE_ID)")
	}
	mode := deps.Mode
	if mode == "" {
		mode = ModeTicket
	}
	switch mode {
	case ModeTicket, ModeOneshot:
	default:
		return fmt.Errorf("finalize: unknown mode %q (want %q or %q)", deps.Mode, ModeTicket, ModeOneshot)
	}
	if mode == ModeOneshot && !deps.ScheduledRunID.Valid {
		return errors.New("finalize: ScheduledRunID is required in oneshot mode (GARRISON_SCHEDULED_RUN_ID)")
	}
	logger.Info("finalize: starting",
		"agent_instance_id", uuidString(deps.AgentInstanceID),
		"mode", mode)

	handler := NewHandler(deps.Pool, deps.AgentInstanceID, logger)
	handler.Mode = mode
	handler.ScheduledRunID = deps.ScheduledRunID
	srv := &server{handler: handler, logger: logger, mode: mode}
	return srv.loop(ctx, stdin, stdout)
}

type server struct {
	handler *Handler
	logger  *slog.Logger

	// mode is ModeTicket or ModeOneshot; the zero value behaves as
	// ModeTicket so M2.2.1-era construction paths are unchanged.
	mode string
}

// toolName is the single tool this server exposes for its mode.
func (s *server) toolName() string {
	if s.mode == ModeOneshot {
		return "finalize_oneshot"
	}
	return "finalize_ticket"
}

// toolDescriptor is the mode-matched tools/list entry (FR-304:
// exactly one tool per mode).
func (s *server) toolDescriptor() map[string]any {
	if s.mode == ModeOneshot {
		return OneshotToolDescriptor()
	}
	return ToolDescriptor()
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

func (s *server) loop(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	scanner := bufio.NewScanner(stdin)
	// Allow long payloads (up to 4000-char rationale + 100 triples).
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
			_ = enc.Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				Error:   &jsonRPCError{Code: errCodeParse, Message: err.Error()},
			})
			continue
		}

		resp := s.dispatch(ctx, req)
		if req.ID == nil {
			continue
		}
		resp.JSONRPC = "2.0"
		resp.ID = req.ID
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("finalize: write response: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("finalize: stdin scan: %w", err)
	}
	return nil
}

func (s *server) dispatch(ctx context.Context, req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return jsonRPCResponse{Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
		}}
	case "tools/list":
		return jsonRPCResponse{Result: map[string]any{
			"tools": []any{s.toolDescriptor()},
		}}
	case "tools/call":
		return s.handleToolsCall(ctx, req.Params)
	case "notifications/initialized":
		return jsonRPCResponse{}
	default:
		return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeMethodNotFound, Message: "method not found: " + req.Method}}
	}
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *server) handleToolsCall(ctx context.Context, params json.RawMessage) jsonRPCResponse {
	var p toolsCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeInvalidParams, Message: err.Error()}}
	}
	if p.Name != s.toolName() {
		return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeMethodNotFound, Message: "unknown tool: " + p.Name}}
	}
	body, err := s.handler.Handle(ctx, p.Arguments)
	if err != nil {
		return jsonRPCResponse{Error: &jsonRPCError{Code: errCodeInvalidParams, Message: err.Error()}}
	}
	// body is already the {content:[{type:text,text:"..."}]} envelope.
	var wrapped map[string]any
	_ = json.Unmarshal(body, &wrapped)
	return jsonRPCResponse{Result: wrapped}
}
