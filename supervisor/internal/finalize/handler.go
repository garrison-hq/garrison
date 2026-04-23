package finalize

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler receives validated tool calls. It does NOT perform the atomic
// write — that's the supervisor's job via internal/spawn.WriteFinalize
// (T007). Handler's sole responsibilities:
//
//  1. Validate the payload against the schema (via tool.go::Validate).
//  2. Query agent_instances to detect already-committed state (FR-260).
//  3. Return a structured tool_result that the agent can act on.
//
// attempts is a per-process counter starting at 1; it increments on
// every Handle call except rejections of already-committed state. The
// supervisor-side counter in internal/spawn/pipeline.go is the cap
// authority — this one is informational for the agent's retry UX.
type Handler struct {
	Pool            *pgxpool.Pool
	AgentInstanceID pgtype.UUID
	Logger          *slog.Logger
	Queries         *store.Queries

	attempts int
}

// NewHandler constructs a Handler and wires a store.Queries binding to
// the pool. Logger is optional; a discard-backed default is used when nil.
func NewHandler(pool *pgxpool.Pool, agentInstanceID pgtype.UUID, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		Pool:            pool,
		AgentInstanceID: agentInstanceID,
		Logger:          logger,
		Queries:         store.New(pool),
	}
}

// ToolResult is the finalize_ticket response body per FR-254/FR-255.
// `Ok` indicates whether the validation succeeded; on false, the error
// fields are populated. `Attempt` is the handler-local counter value
// at the time of the response (1-based).
type ToolResult struct {
	Ok        bool      `json:"ok"`
	Attempt   int       `json:"attempt"`
	ErrorType ErrorType `json:"error_type,omitempty"`
	Field     string    `json:"field,omitempty"`
	Message   string    `json:"message,omitempty"`
}

// Handle validates one tools/call payload and returns the MCP content
// array ready to be embedded in a tools/call response Result. The MCP
// convention is that tool returns are stringified JSON inside
// content[0].text (matches what MemPalace's own tools do — see M2.2
// spike §3.6).
func (h *Handler) Handle(ctx context.Context, rawArgs json.RawMessage) (json.RawMessage, error) {
	h.attempts++

	// Step 1: check already-committed state before validating. A post-
	// commit duplicate call should report the already-committed error
	// regardless of whether the repeated payload is schema-valid.
	committed, err := h.checkAlreadyCommitted(ctx)
	if err != nil {
		// Database read failures surface as schema errors with a
		// generic message — the agent can't do anything about a
		// Postgres issue, but we don't leak internals.
		h.Logger.Error("finalize: already-committed check failed", "err", err,
			"agent_instance_id", uuidString(h.AgentInstanceID))
		return h.errorResult(ErrorTypeSchema, "", "internal error checking finalize state"), nil
	}
	if committed {
		return h.errorResult(ErrorTypeSchema, "", "finalize_ticket already succeeded for this agent_instance"), nil
	}

	// Step 2: schema validation.
	payload, verr := Validate(rawArgs)
	if verr != nil {
		h.Logger.Info("finalize: schema rejection",
			"agent_instance_id", uuidString(h.AgentInstanceID),
			"attempt", h.attempts,
			"error_type", verr.ErrorType,
			"field", verr.Field)
		return h.errorResult(verr.ErrorType, verr.Field, verr.Message), nil
	}

	// Step 3: success. The supervisor-side event observer in
	// internal/spawn/pipeline.go is watching the stream-json parser
	// for this tool_result; on seeing ok=true it triggers WriteFinalize
	// per FR-259. Handler does NOT perform the write itself.
	h.Logger.Info("finalize: payload accepted",
		"agent_instance_id", uuidString(h.AgentInstanceID),
		"ticket_id", payload.TicketID,
		"attempt", h.attempts,
		"triple_count", len(payload.KGTriples))
	return h.okResult(), nil
}

// checkAlreadyCommitted runs SelectAgentInstanceFinalizedState. Returns
// true when the atomic writer has already committed (status=succeeded
// AND a ticket_transitions row exists for this agent_instance). Any
// error is surfaced to the caller so the handler can return a generic
// internal error — the DB query is on the hot path, so failures are
// expected to be transient.
func (h *Handler) checkAlreadyCommitted(ctx context.Context) (bool, error) {
	if h.Queries == nil {
		return false, fmt.Errorf("finalize: handler has no queries binding")
	}
	row, err := h.Queries.SelectAgentInstanceFinalizedState(ctx, h.AgentInstanceID)
	if err != nil {
		return false, err
	}
	return row.Status == "succeeded" && row.HasTransition, nil
}

func (h *Handler) okResult() json.RawMessage {
	body := ToolResult{Ok: true, Attempt: h.attempts}
	raw, _ := json.Marshal(body)
	return mcpContentEnvelope(raw)
}

func (h *Handler) errorResult(t ErrorType, field, message string) json.RawMessage {
	body := ToolResult{
		Ok:        false,
		Attempt:   h.attempts,
		ErrorType: t,
		Field:     field,
		Message:   message,
	}
	raw, _ := json.Marshal(body)
	return mcpContentEnvelope(raw)
}

// mcpContentEnvelope wraps the stringified JSON body in the standard
// MCP tool_result content shape: {"content":[{"type":"text","text":"<body>"}]}.
func mcpContentEnvelope(body []byte) json.RawMessage {
	envelope := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": string(body)},
		},
	}
	raw, _ := json.Marshal(envelope)
	return raw
}

// uuidString formats a pgtype.UUID for structured log context. Returns
// empty string for invalid UUIDs so log lines carry no bogus values.
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	const hex = "0123456789abcdef"
	out := make([]byte, 36)
	j := 0
	for i, x := range b {
		out[j] = hex[x>>4]
		j++
		out[j] = hex[x&0x0f]
		j++
		if i == 3 || i == 5 || i == 7 || i == 9 {
			out[j] = '-'
			j++
		}
	}
	return string(out)
}
