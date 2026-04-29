package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/claudeproto"
	"github.com/garrison-hq/garrison/supervisor/internal/spawn"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChatPolicy implements spawn.Policy + claudeproto.Router for the
// M5.1 chat runtime. One ChatPolicy is constructed per chat-message
// spawn (worker → spawn.Run → ChatPolicy). It does NOT carry a
// spawn.Result — chat has its own terminal-write path via
// chat_messages row commit; Result() returns a zero value.
type ChatPolicy struct {
	Pool       *pgxpool.Pool
	Queries    *store.Queries
	Logger     *slog.Logger
	SessionID  pgtype.UUID
	MessageID  pgtype.UUID
	GraceWrite time.Duration

	// runtime state populated as the stream is consumed
	deltaSeq int
	// messageBlock increments on each claude message_start. The
	// dashboard uses this to reset its per-message_id partial buffer
	// so multi-message turns (text → tool_use → text) render only
	// the current message's deltas while it streams.
	messageBlock int
	contentBuf   strings.Builder
	rawEvents    []json.RawMessage
	bailReason   string // populated by OnInit on MCP-health bail

	// rate-limit observation
	rateLimitOverage bool

	// usage snapshot (populated by OnStreamEvent message_start +
	// message_delta; surfaced into the terminal commit)
	tokensInput        int
	tokensOutput       int
	cacheReadInput     int
	cacheCreationInput int

	// resultEvent records the OnResult terminal-event payload for
	// CommitAssistantTerminal.
	resultEvent *claudeproto.ResultEvent
}

// NewChatPolicy constructs a fresh policy bound to one chat-message
// turn. ctxPool/queries are shared from chat.Deps; sessionID/messageID
// identify the in-flight assistant row.
func NewChatPolicy(deps Deps, sessionID, messageID pgtype.UUID) *ChatPolicy {
	return &ChatPolicy{
		Pool:       deps.Pool,
		Queries:    deps.Queries,
		Logger:     deps.Logger,
		SessionID:  sessionID,
		MessageID:  messageID,
		GraceWrite: deps.TerminalWriteGrace,
	}
}

// var _ spawn.Policy = (*ChatPolicy)(nil) is the build-time conformance
// check. Imported into a discard variable so the assertion fires at
// build time without affecting the binary.
var _ spawn.Policy = (*ChatPolicy)(nil)

func (p *ChatPolicy) OnInit(ctx context.Context, e claudeproto.InitEvent) claudeproto.RouterAction {
	healthy, offender, status := claudeproto.CheckMCPHealth(e.MCPServers)
	if !healthy {
		p.bailReason = BuildMCPErrorKind(offender, status)
		p.Logger.Error("chat: MCP server not connected at init; bailing",
			"session_id", uuidString(p.SessionID),
			"message_id", uuidString(p.MessageID),
			"offender", offender, "status", status)
		return claudeproto.RouterActionBail
	}
	p.Logger.Info("chat: claude init",
		"session_id", uuidString(p.SessionID),
		"message_id", uuidString(p.MessageID),
		"claude_session_id", e.SessionID,
		"model", e.Model)
	// Transition to streaming. Best-effort: parser keeps draining even
	// if this UPDATE fails (it's a state-bookkeeping nicety, not a
	// correctness barrier).
	if err := p.Queries.TransitionMessageToStreaming(ctx, p.MessageID); err != nil {
		p.Logger.Warn("chat: TransitionMessageToStreaming failed", "err", err)
	}
	return claudeproto.RouterActionContinue
}

func (p *ChatPolicy) OnAssistant(_ context.Context, e claudeproto.AssistantEvent) {
	// observational; the actual content accumulation happens via
	// OnStreamEvent text_delta events. Keep the raw envelope.
	p.rawEvents = append(p.rawEvents, json.RawMessage(append([]byte(nil), e.Raw...)))
}

func (p *ChatPolicy) OnUser(_ context.Context, e claudeproto.UserEvent) {
	// chat has no MCP tool_uses today (read-only postgres + mempalace);
	// log error tool_results at warn so debugging surfaces.
	for _, tr := range e.ToolResults {
		if tr.IsError {
			p.Logger.Warn("chat: tool_result reported error",
				"session_id", uuidString(p.SessionID),
				"message_id", uuidString(p.MessageID),
				"tool_use_id", tr.ToolUseID,
				"detail", tr.Detail)
		}
	}
	p.rawEvents = append(p.rawEvents, json.RawMessage(append([]byte(nil), e.Raw...)))
}

func (p *ChatPolicy) OnRateLimit(_ context.Context, e claudeproto.RateLimitEvent) {
	if e.Info.Status == "rejected" || strings.EqualFold(e.Info.RateLimitType, "rejected") {
		p.rateLimitOverage = true
	}
	p.Logger.Warn("chat: rate_limit_event",
		"session_id", uuidString(p.SessionID),
		"message_id", uuidString(p.MessageID),
		"status", e.Info.Status, "rate_limit_type", e.Info.RateLimitType,
		"is_using_overage", e.Info.IsUsingOverage)
	p.rawEvents = append(p.rawEvents, json.RawMessage(append([]byte(nil), e.Raw...)))
}

func (p *ChatPolicy) OnStreamEvent(ctx context.Context, e claudeproto.StreamEvent) {
	p.rawEvents = append(p.rawEvents, json.RawMessage(append([]byte(nil), e.Raw...)))

	switch e.InnerType {
	case "content_block_start":
		// When claude opens a tool_use content block within an
		// assistant message, any text streamed earlier in the same
		// message was preamble (e.g. "Let me check ..."). The
		// committed terminal commits only the FINAL message's text
		// (per message_start contentBuf reset above), but the live
		// stream has already pushed the preamble into the dashboard's
		// partialDeltas. Emit a scrub directive so the dashboard
		// clears the visible buffer for this (messageId, block) the
		// moment we know the preamble is no longer the answer.
		//
		// We detect tool_use by raw-substring search instead of
		// extending claudeproto's wire shape — the Raw bytes are the
		// canonical NDJSON line and `"type":"tool_use"` is unambiguous
		// at the content_block.type position.
		if bytes.Contains(e.Raw, []byte(`"content_block":{"type":"tool_use"`)) ||
			bytes.Contains(e.Raw, []byte(`"content_block": {"type": "tool_use"`)) {
			seq := p.deltaSeq
			p.deltaSeq++
			if err := EmitScrub(ctx, p.Pool, p.MessageID, p.messageBlock, seq); err != nil {
				p.Logger.Warn("chat: EmitScrub failed", "block", p.messageBlock, "seq", seq, "err", err)
			}
			// Drop any preamble text already accumulated for this
			// message — only the final message's text should commit
			// regardless of whether the next message_start fires.
			p.contentBuf.Reset()
		}
	case "content_block_delta":
		if e.Inner.DeltaType != "text_delta" {
			return // tool_use input deltas, thinking deltas: observational only
		}
		// Accumulate for the terminal content; emit per-batch notify.
		p.contentBuf.WriteString(e.Inner.DeltaText)
		seq := p.deltaSeq
		p.deltaSeq++
		if err := EmitDelta(ctx, p.Pool, p.MessageID, p.messageBlock, seq, e.Inner.DeltaText); err != nil {
			p.Logger.Warn("chat: EmitDelta failed", "block", p.messageBlock, "seq", seq, "err", err)
		}
	case "message_start":
		// Claude emits ONE message_start per assistant message in the
		// response stream. When the turn involves tool calls (text →
		// tool_use → text → ...) the response can carry multiple
		// message_start blocks. The committed `content` should be the
		// LAST assistant message's text — the human-readable answer
		// after any tool round-trips — not the concatenation of every
		// intermediate text block. Reset contentBuf so subsequent
		// content_block_delta text_deltas accumulate only into the
		// current message.
		//
		// Token totals across messages are tracked separately:
		// tokensInput is overridden every time (claude reports the same
		// cumulative input tokens on each message_start); tokensOutput
		// is taken from message_delta which fires per-message.
		p.contentBuf.Reset()
		// Bump messageBlock so subsequent EmitDelta calls carry the
		// new value; the dashboard resets its per-messageId visible
		// buffer when it sees a higher block.
		p.messageBlock++
		// deltaSeq stays monotonic across blocks for dedupe purposes —
		// the dashboard's seenSeqs set uses (messageId, block, seq) so
		// reusing a seq inside a new block is a different key.
		p.tokensInput = e.Inner.InputTokens
		p.cacheReadInput = e.Inner.CacheReadInput
		p.cacheCreationInput = e.Inner.CacheCreationInput
	case "message_delta":
		if e.Inner.OutputTokens > 0 {
			p.tokensOutput = e.Inner.OutputTokens
		}
	}
}

func (p *ChatPolicy) OnResult(ctx context.Context, e claudeproto.ResultEvent) {
	cp := e
	p.resultEvent = &cp
	p.rawEvents = append(p.rawEvents, json.RawMessage(append([]byte(nil), e.Raw...)))

	// Commit terminal state. Use a fresh tx via the pool; if we're
	// already in a shutdown path the caller (worker) has wrapped ctx
	// in WithoutCancel + grace.
	if err := p.commitAssistantTerminal(ctx, e); err != nil {
		p.Logger.Error("chat: commit terminal failed",
			"session_id", uuidString(p.SessionID),
			"message_id", uuidString(p.MessageID),
			"err", err)
	}
}

func (p *ChatPolicy) OnTaskStarted(_ context.Context, _ claudeproto.TaskStartedEvent) {
	// no-op for chat; tasks are claude's internal feature
}

func (p *ChatPolicy) OnUnknown(_ context.Context, e claudeproto.UnknownEvent) {
	p.Logger.Warn("chat: unknown event",
		"type", e.Type, "subtype", e.Subtype)
	p.rawEvents = append(p.rawEvents, json.RawMessage(append([]byte(nil), e.Raw...)))
}

// OnTerminate is invoked by spawn.Run on parse error or RouterActionBail.
// For chat we map these into chat.ErrorKind values and terminal-write
// the assistant row via the WithoutCancel + grace pattern.
func (p *ChatPolicy) OnTerminate(ctx context.Context, reason string) {
	var ek ErrorKind
	switch reason {
	case "parse_error":
		ek = ErrorClaudeRuntimeError
	case "bail":
		if p.bailReason != "" {
			ek = p.bailReason // mcp_<server>_<status>
		} else {
			ek = ErrorClaudeRuntimeError
		}
	default:
		ek = ErrorClaudeRuntimeError
	}
	p.terminalWriteError(ctx, ek)
}

// Result returns a zero spawn.Result for chat. Run uses this only as
// a return-value passthrough; chat doesn't go through Adjudicate.
func (p *ChatPolicy) Result() spawn.Result {
	return spawn.Result{}
}

// commitAssistantTerminal writes the success path: status='completed'
// (or 'failed' if is_error), content, cost, tokens, raw_envelope, in
// one tx with RollUpSessionCost + work.chat.message_sent notify.
func (p *ChatPolicy) commitAssistantTerminal(ctx context.Context, e claudeproto.ResultEvent) error {
	status := "completed"
	var ek *string
	if e.IsError {
		status = "failed"
		k := ErrorClaudeRuntimeError
		ek = &k
	}
	if p.rateLimitOverage && !e.IsError {
		// Result arrived even though rate limit was rejected; surface as
		// failed with the rate-limit kind so operator can rotate.
		status = "failed"
		k := ErrorRateLimitExhausted
		ek = &k
	}

	tx, err := p.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("commit: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := p.Queries.WithTx(tx)

	content := p.contentBuf.String()
	costNumeric, err := numericFromString(string(e.TotalCostUSD))
	if err != nil {
		return fmt.Errorf("commit: parse cost: %w", err)
	}
	envelopeBytes, err := json.Marshal(p.rawEvents)
	if err != nil {
		return fmt.Errorf("commit: marshal envelope: %w", err)
	}

	tokensIn := int32(p.tokensInput)
	tokensOut := int32(p.tokensOutput)

	if err := q.CommitAssistantTerminal(ctx, store.CommitAssistantTerminalParams{
		ID:               p.MessageID,
		Status:           status,
		Content:          &content,
		TokensInput:      &tokensIn,
		TokensOutput:     &tokensOut,
		CostUsd:          costNumeric,
		ErrorKind:        ek,
		RawEventEnvelope: envelopeBytes,
	}); err != nil {
		return fmt.Errorf("commit: update message: %w", err)
	}

	if err := q.RollUpSessionCost(ctx, store.RollUpSessionCostParams{
		ID:       p.SessionID,
		DeltaUsd: costNumeric,
	}); err != nil {
		return fmt.Errorf("commit: roll up cost: %w", err)
	}

	if err := EmitMessageSent(ctx, tx, p.SessionID, p.MessageID); err != nil {
		return fmt.Errorf("commit: emit message_sent: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: tx commit: %w", err)
	}
	return nil
}

// terminalWriteError writes status='failed' + error_kind without
// touching content/cost/envelope. Uses WithoutCancel + grace timeout
// so shutdown paths still complete the write (AGENTS.md rule 6).
func (p *ChatPolicy) terminalWriteError(ctx context.Context, ek ErrorKind) {
	wctx := context.WithoutCancel(ctx)
	if p.GraceWrite > 0 {
		var cancel context.CancelFunc
		wctx, cancel = context.WithTimeout(wctx, p.GraceWrite)
		defer cancel()
	}
	ekVal := ek
	if err := p.Queries.TerminalWriteWithError(wctx, store.TerminalWriteWithErrorParams{
		ID:        p.MessageID,
		Status:    "failed",
		ErrorKind: &ekVal,
	}); err != nil {
		p.Logger.Error("chat: terminalWriteError failed", "err", err, "error_kind", ek)
	}
}

// numericFromString parses Claude's json.Number-shaped cost into
// pgtype.Numeric. Empty / "0" → valid zero; arbitrary precision
// preserved via the Numeric Scan path.
func numericFromString(s string) (pgtype.Numeric, error) {
	var n pgtype.Numeric
	if s == "" {
		return n, nil
	}
	if err := n.Scan(s); err != nil {
		return n, err
	}
	return n, nil
}
