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
// (M2.2.1 T007). Handler's sole responsibilities:
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

// ToolResult is the finalize_ticket response body per FR-254/FR-255
// plus the M2.2.2 rich-error extensions per FR-301. `Ok` indicates
// whether validation succeeded; on false, the error fields are
// populated. `Attempt` is the handler-local counter value at the time
// of the response (1-based).
//
// JSON-tag policy per plan §"`internal/finalize/handler.go`":
//   - M2.2.1 fields (ErrorType, Field, Message) keep `,omitempty` so
//     OK responses stay compact (backward compat with M2.2.1 test
//     fixtures that parse the sparse shape).
//   - Failure keeps `,omitempty` because OK responses don't carry a
//     failure class — the JSON reads cleaner without it on success.
//   - The remaining new fields (Line, Column, Excerpt, Constraint,
//     Expected, Actual, Hint) drop `omitempty` so empty-string /
//     zero-value fields serialize as `""` / `0` at the JSON layer
//     per Clarification 2026-04-23 Q1's wire-shape-stability
//     requirement. Agents parsing the error object find the same
//     field set regardless of which Failure branch fired.
type ToolResult struct {
	Ok        bool      `json:"ok"`
	Attempt   int       `json:"attempt"`
	ErrorType ErrorType `json:"error_type,omitempty"`
	Field     string    `json:"field,omitempty"`
	Message   string    `json:"message,omitempty"`

	Failure    Failure    `json:"failure,omitempty"`
	Line       int        `json:"line"`
	Column     int        `json:"column"`
	Excerpt    string     `json:"excerpt"`
	Constraint Constraint `json:"constraint"`
	Expected   string     `json:"expected"`
	Actual     string     `json:"actual"`
	Hint       string     `json:"hint"`
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
		// Database read failures surface as a FailureState rejection
		// with a generic hint — the agent can't do anything about a
		// Postgres issue, but we don't leak internals. M2.2.2: wire
		// shape matches the other state path per Clarification Q3.
		h.Logger.Error("finalize: already-committed check failed", "err", err,
			"agent_instance_id", uuidString(h.AgentInstanceID))
		verr := &ValidationError{
			ErrorType: ErrorTypeSchema,
			Field:     "",
			Message:   "internal error checking finalize state",
			Failure:   FailureState,
			Hint:      "internal error checking finalize state; please retry",
		}
		return h.errorResult(verr), nil
	}
	if committed {
		return h.stateRejectionResult(), nil
	}

	// Step 2: schema validation.
	payload, verr := Validate(rawArgs)
	if verr != nil {
		h.Logger.Info("finalize: schema rejection",
			"agent_instance_id", uuidString(h.AgentInstanceID),
			"attempt", h.attempts,
			"error_type", verr.ErrorType,
			"field", verr.Field,
			"failure", verr.Failure,
			"constraint", verr.Constraint)
		return h.errorResult(verr), nil
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

// stateRejectionResult builds the already-committed (FR-260) response
// per Clarification 2026-04-23 Q3. `failure="state"` distinguishes
// this lifecycle objection from schema-shape validation and decode
// errors; `constraint`, `expected`, `actual`, `line`, `column`,
// `excerpt` are all empty/zero. The M2.2.1 `message` field carries
// the same string as `hint` for Q9 backward compat.
func (h *Handler) stateRejectionResult() json.RawMessage {
	const msg = "finalize_ticket already succeeded for this agent_instance"
	body := ToolResult{
		Ok:        false,
		Attempt:   h.attempts,
		ErrorType: ErrorTypeSchema,
		Field:     "",
		Message:   msg,
		Failure:   FailureState,
		Hint:      msg,
	}
	raw, _ := json.Marshal(body)
	return mcpContentEnvelope(raw)
}

// errorResult builds a tool_result envelope from a populated
// ValidationError. Copies all 11 fields into the ToolResult;
// truncates Actual to ActualTruncateMax (100) chars per FR-303.
func (h *Handler) errorResult(verr *ValidationError) json.RawMessage {
	actual := verr.Actual
	if len(actual) > ActualTruncateMax {
		actual = actual[:ActualTruncateMax]
	}
	body := ToolResult{
		Ok:         false,
		Attempt:    h.attempts,
		ErrorType:  verr.ErrorType,
		Field:      verr.Field,
		Message:    verr.Message,
		Failure:    verr.Failure,
		Line:       verr.Line,
		Column:     verr.Column,
		Excerpt:    verr.Excerpt,
		Constraint: verr.Constraint,
		Expected:   verr.Expected,
		Actual:     actual,
		Hint:       verr.Hint,
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
