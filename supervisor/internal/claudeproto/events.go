// Package claudeproto types the stream-json event vocabulary Claude Code
// emits in `--output-format stream-json --verbose` mode, plus the dispatch
// surface the supervisor uses to consume those events. The package is pure
// data plus dispatch — no I/O, no goroutines, no logging. That makes the
// routing rules exhaustively unit-testable with captured JSON fixtures.
//
// Field selection is pragmatic: only the fields the supervisor acts on are
// enumerated. Every event type also carries Raw (the original NDJSON line)
// so consumers can log the full envelope without re-marshalling. This lets
// downstream code stay honest when Claude adds fields in future versions —
// the structured portion covers what we depend on, and Raw covers every-
// thing else.
//
// Empirical shape reference: the samples captured during T006 against
// claude 2.1.117 (the M2.1-pinned version) in /tmp/claudeproto-fixtures.
// The spike doc `docs/research/m2-spike.md` §2.1 gives the inventory; this
// file refines a few shapes the spike's prose didn't capture precisely
// (assistant.message wrapping, rate_limit_event's rate_limit_info sub-
// object, result.subtype being populated with "success").
package claudeproto

import (
	"encoding/json"
)

// MCPServer is one entry of the init event's mcp_servers array. Per
// spike §A the enum of observed Status values is {connected, failed,
// needs-auth}; CheckMCPHealth treats anything outside "connected" as a
// failure (fail-closed; FR-108).
type MCPServer struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// InitEvent corresponds to the `system`/`init` line. FR-108 and FR-109
// require the supervisor to verify MCPServers and capture SessionID, Tools,
// and CWD. Other fields (model, permissionMode, slash_commands, skills,
// plugins, memory_paths, apiKeySource, claude_code_version) are present on
// the wire but unused; Raw carries them if a future log needs them.
type InitEvent struct {
	SessionID  string      `json:"session_id"`
	Model      string      `json:"model"`
	CWD        string      `json:"cwd"`
	Tools      []string    `json:"tools"`
	MCPServers []MCPServer `json:"mcp_servers"`
	Raw        []byte      `json:"-"`
}

// TaskStartedEvent corresponds to `system`/`task_started`. Claude's own
// task-system notifications; the supervisor logs at debug and takes no
// action (plan §pipeline.Run / OnTaskStarted).
type TaskStartedEvent struct {
	Raw []byte `json:"-"`
}

// ToolUseBlock is one tool_use item extracted from an assistant event's
// content array. M2.2 uses this for FR-218 mempalace_* tool-call logging
// (see internal/spawn/pipeline.go). Name is the tool's id ("mempalace_
// add_drawer", "mcp__mempalace__mempalace_add_drawer", etc.); ToolUseID
// is the correlation handle the subsequent user/tool_result event
// carries; InputRaw is the caller-provided arguments as received (kept
// as json.RawMessage so assistants' free-form input doesn't require
// pre-commit to a schema).
type ToolUseBlock struct {
	Name      string          `json:"name"`
	ToolUseID string          `json:"id"`
	InputRaw  json.RawMessage `json:"input"`
}

// AssistantEvent corresponds to the `assistant` line. The `message` sub-
// object carries content blocks (thinking / text / tool_use). The
// supervisor logs observationally (plan §OnAssistant); no dispatch change.
// ContentTypes is the deduplicated list of content-block types in this
// message — useful structured context for the slog line. ToolUses is
// populated with any tool_use blocks found in content (M2.2 / FR-218):
// best-effort parsing; nil if the shape drifts, which the caller treats
// as "no mempalace tool_use observed in this line" rather than an error.
type AssistantEvent struct {
	Model             string         `json:"-"`
	ContentBlockCount int            `json:"-"`
	ContentTypes      []string       `json:"-"`
	ToolUses          []ToolUseBlock `json:"-"`
	Raw               []byte         `json:"-"`
}

// ToolResultSummary is one tool_result item from the user event's
// message.content array. IsError is what the supervisor actually cares
// about for warn-level logging (plan §OnUser); Detail carries a short
// excerpt for the log line.
type ToolResultSummary struct {
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
	Detail    string `json:"-"`
}

// UserEvent corresponds to the `user` line — a turn where Claude passes
// tool_result values back to its own model. Per FR-111 the supervisor
// does not bail on tool errors; it only logs them.
type UserEvent struct {
	ToolResults []ToolResultSummary `json:"-"`
	Raw         []byte              `json:"-"`
}

// RateLimitInfo is the nested object real Claude 2.1.117 emits under
// rate_limit_event.rate_limit_info (the spike prose listed these fields
// at the top level; empirically they are nested). All six fields are
// logged by NFR-109 / SC-109.
type RateLimitInfo struct {
	Status             string  `json:"status"`
	ResetsAt           int64   `json:"resetsAt"`
	RateLimitType      string  `json:"rateLimitType"`
	Utilization        float64 `json:"utilization"`
	IsUsingOverage     bool    `json:"isUsingOverage"`
	SurpassedThreshold float64 `json:"surpassedThreshold"`
}

// RateLimitEvent corresponds to the `rate_limit_event` line. Logged at
// warn level per the routing table; M2.1 does not act on it (M6 will).
type RateLimitEvent struct {
	Info      RateLimitInfo `json:"rate_limit_info"`
	UUID      string        `json:"uuid"`
	SessionID string        `json:"session_id"`
	Raw       []byte        `json:"-"`
}

// ResultEvent corresponds to the terminal `result` line. TotalCostUSD is
// json.Number (a string) so Claude's full decimal precision (e.g.
// "0.00965475") is preserved across unmarshal; a float64 would lose the
// tail. The spawn package converts to pgtype.Numeric via its Scan path.
//
// Subtype is populated with "success" on happy-path runs (empirically
// confirmed on 2.1.117); kept as a separate field so the pipeline can
// distinguish "completed" from any future subtype Anthropic introduces.
type ResultEvent struct {
	Subtype           string      `json:"subtype"`
	IsError           bool        `json:"is_error"`
	DurationMS        int         `json:"duration_ms"`
	DurationAPIMs     int         `json:"duration_api_ms"`
	TotalCostUSD      json.Number `json:"total_cost_usd"`
	TerminalReason    string      `json:"-"` // derived from Subtype + StopReason below
	StopReason        string      `json:"stop_reason"`
	Result            string      `json:"result"`
	PermissionDenials []string    `json:"permission_denials"`
	SessionID         string      `json:"session_id"`
	Raw               []byte      `json:"-"`
}

// UnknownEvent carries any event whose `type` (or `type`/`subtype` pair)
// is not in the routing table. FR-107 requires this path to log and
// continue, not crash or bail. Future Claude versions may add event
// types; the supervisor stays forward-compatible.
type UnknownEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Raw     []byte `json:"-"`
}
