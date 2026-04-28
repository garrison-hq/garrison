package claudeproto

import (
	"context"
	"testing"
)

// captureRouter records which method was called and with what payload.
// Zero-valued Router before Route returns.
type captureRouter struct {
	calls       []string
	initAction  RouterAction
	init        *InitEvent
	assistant   *AssistantEvent
	user        *UserEvent
	rateLimit   *RateLimitEvent
	result      *ResultEvent
	streamEvent *StreamEvent
	taskStarted *TaskStartedEvent
	unknown     *UnknownEvent
}

func (r *captureRouter) OnInit(_ context.Context, e InitEvent) RouterAction {
	r.calls = append(r.calls, "init")
	r.init = &e
	return r.initAction
}
func (r *captureRouter) OnAssistant(_ context.Context, e AssistantEvent) {
	r.calls = append(r.calls, "assistant")
	r.assistant = &e
}
func (r *captureRouter) OnUser(_ context.Context, e UserEvent) {
	r.calls = append(r.calls, "user")
	r.user = &e
}
func (r *captureRouter) OnRateLimit(_ context.Context, e RateLimitEvent) {
	r.calls = append(r.calls, "rate_limit")
	r.rateLimit = &e
}
func (r *captureRouter) OnResult(_ context.Context, e ResultEvent) {
	r.calls = append(r.calls, "result")
	r.result = &e
}
func (r *captureRouter) OnStreamEvent(_ context.Context, e StreamEvent) {
	r.calls = append(r.calls, "stream_event")
	r.streamEvent = &e
}
func (r *captureRouter) OnTaskStarted(_ context.Context, e TaskStartedEvent) {
	r.calls = append(r.calls, "task_started")
	r.taskStarted = &e
}
func (r *captureRouter) OnUnknown(_ context.Context, e UnknownEvent) {
	r.calls = append(r.calls, "unknown")
	r.unknown = &e
}

// TestRouteInitEvent parses a real 2.1.117 init line (minimized to the
// fields the supervisor acts on) and asserts OnInit fires with populated
// SessionID, CWD, Tools, and MCPServers.
func TestRouteInitEvent(t *testing.T) {
	raw := []byte(`{"type":"system","subtype":"init","cwd":"/work","session_id":"sid-1","model":"claude-haiku-4-5-20251001","tools":["Bash","Read","mcp__postgres__query"],"mcp_servers":[{"name":"postgres","status":"connected"}]}`)
	r := &captureRouter{initAction: RouterActionContinue}
	act, err := Route(context.Background(), raw, r)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}
	if act != RouterActionContinue {
		t.Fatalf("expected Continue, got %v", act)
	}
	if got := r.calls; len(got) != 1 || got[0] != "init" {
		t.Fatalf("expected one init call, got %v", got)
	}
	if r.init.SessionID != "sid-1" {
		t.Errorf("SessionID: got %q, want sid-1", r.init.SessionID)
	}
	if r.init.CWD != "/work" {
		t.Errorf("CWD: got %q, want /work", r.init.CWD)
	}
	if len(r.init.Tools) != 3 || r.init.Tools[2] != "mcp__postgres__query" {
		t.Errorf("Tools: got %v", r.init.Tools)
	}
	if len(r.init.MCPServers) != 1 || r.init.MCPServers[0].Name != "postgres" || r.init.MCPServers[0].Status != "connected" {
		t.Errorf("MCPServers: got %+v", r.init.MCPServers)
	}
	if len(r.init.Raw) == 0 {
		t.Errorf("Raw should be populated")
	}
}

// TestRouteAssistantEvent parses a line with three content-block types
// (thinking, text, tool_use) and verifies the summary fields. Shape
// matches real 2.1.117 output: content is nested under message.
func TestRouteAssistantEvent(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","id":"msg_1","role":"assistant","content":[{"type":"thinking","thinking":"…"},{"type":"text","text":"OK"},{"type":"tool_use","id":"t1","name":"mcp__postgres__query","input":{}}]}}`)
	r := &captureRouter{}
	act, err := Route(context.Background(), raw, r)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}
	if act != RouterActionContinue {
		t.Fatalf("expected Continue, got %v", act)
	}
	if r.assistant == nil {
		t.Fatalf("expected OnAssistant to fire")
	}
	if r.assistant.ContentBlockCount != 3 {
		t.Errorf("ContentBlockCount: got %d, want 3", r.assistant.ContentBlockCount)
	}
	// ContentTypes is deduplicated; order matches first-seen.
	want := []string{"thinking", "text", "tool_use"}
	if len(r.assistant.ContentTypes) != 3 {
		t.Fatalf("ContentTypes: got %v, want %v", r.assistant.ContentTypes, want)
	}
	for i, ty := range want {
		if r.assistant.ContentTypes[i] != ty {
			t.Errorf("ContentTypes[%d]: got %q, want %q", i, r.assistant.ContentTypes[i], ty)
		}
	}
	if r.assistant.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model: got %q", r.assistant.Model)
	}
}

// TestRouteUserEvent parses a user-turn line carrying a tool_result with
// is_error=true and verifies the summary captures both the flag and a
// short Detail excerpt.
func TestRouteUserEvent(t *testing.T) {
	raw := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":[{"type":"text","text":"ERROR: permission denied for table tickets"}]}]}}`)
	r := &captureRouter{}
	act, err := Route(context.Background(), raw, r)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}
	if act != RouterActionContinue {
		t.Fatalf("expected Continue, got %v", act)
	}
	if r.user == nil || len(r.user.ToolResults) != 1 {
		t.Fatalf("expected one tool_result, got %+v", r.user)
	}
	tr := r.user.ToolResults[0]
	if !tr.IsError {
		t.Errorf("IsError: got false, want true")
	}
	if tr.ToolUseID != "t1" {
		t.Errorf("ToolUseID: got %q, want t1", tr.ToolUseID)
	}
	if tr.Detail != "ERROR: permission denied for table tickets" {
		t.Errorf("Detail: got %q", tr.Detail)
	}
}

// TestRouteRateLimitEvent verifies all six rate_limit_info fields plus
// UUID and SessionID are populated. Shape matches real 2.1.117 output
// (the spike's prose listed the fields at the top level; they are in
// fact nested under rate_limit_info).
func TestRouteRateLimitEvent(t *testing.T) {
	raw := []byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1776970800,"rateLimitType":"seven_day","utilization":0.84,"isUsingOverage":false,"surpassedThreshold":0.75},"uuid":"u-1","session_id":"s-1"}`)
	r := &captureRouter{}
	act, err := Route(context.Background(), raw, r)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}
	if act != RouterActionContinue {
		t.Fatalf("expected Continue, got %v", act)
	}
	if r.rateLimit == nil {
		t.Fatalf("expected OnRateLimit to fire")
	}
	info := r.rateLimit.Info
	if info.Status != "allowed_warning" {
		t.Errorf("Status: got %q", info.Status)
	}
	if info.ResetsAt != 1776970800 {
		t.Errorf("ResetsAt: got %d", info.ResetsAt)
	}
	if info.RateLimitType != "seven_day" {
		t.Errorf("RateLimitType: got %q", info.RateLimitType)
	}
	if info.Utilization != 0.84 {
		t.Errorf("Utilization: got %v", info.Utilization)
	}
	if info.IsUsingOverage {
		t.Errorf("IsUsingOverage: got true, want false")
	}
	if info.SurpassedThreshold != 0.75 {
		t.Errorf("SurpassedThreshold: got %v", info.SurpassedThreshold)
	}
	if r.rateLimit.UUID != "u-1" {
		t.Errorf("UUID: got %q", r.rateLimit.UUID)
	}
	if r.rateLimit.SessionID != "s-1" {
		t.Errorf("SessionID: got %q", r.rateLimit.SessionID)
	}
}

// TestRouteResultEvent pins the terminal-event decoding: IsError, duration,
// total_cost_usd (as json.Number for precision), terminal text, subtype,
// and PermissionDenials. The cost value 0.00965475 must round-trip
// byte-exact — proving the UseNumber path avoids float64 precision loss
// (NFR-108 requirement).
func TestRouteResultEvent(t *testing.T) {
	raw := []byte(`{"type":"result","subtype":"success","is_error":false,"duration_ms":1742,"duration_api_ms":2742,"total_cost_usd":0.00965475,"stop_reason":"end_turn","result":"OK","permission_denials":[],"session_id":"s-1"}`)
	r := &captureRouter{}
	act, err := Route(context.Background(), raw, r)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}
	if act != RouterActionContinue {
		t.Fatalf("expected Continue, got %v", act)
	}
	if r.result == nil {
		t.Fatalf("expected OnResult to fire")
	}
	if r.result.IsError {
		t.Errorf("IsError: got true")
	}
	if r.result.DurationMS != 1742 {
		t.Errorf("DurationMS: got %d", r.result.DurationMS)
	}
	if r.result.TotalCostUSD.String() != "0.00965475" {
		t.Errorf("TotalCostUSD: got %q, want 0.00965475 (float64 parse would corrupt this)", r.result.TotalCostUSD.String())
	}
	if r.result.Subtype != "success" {
		t.Errorf("Subtype: got %q", r.result.Subtype)
	}
	if r.result.TerminalReason != "success" {
		t.Errorf("TerminalReason: got %q, want 'success' (derived from subtype)", r.result.TerminalReason)
	}
	if r.result.Result != "OK" {
		t.Errorf("Result text: got %q", r.result.Result)
	}
	if r.result.StopReason != "end_turn" {
		t.Errorf("StopReason: got %q", r.result.StopReason)
	}
}

// TestRouteTaskStartedEvent dispatches Claude's internal task-started
// notification to OnTaskStarted. The supervisor logs at debug and no-ops.
func TestRouteTaskStartedEvent(t *testing.T) {
	raw := []byte(`{"type":"system","subtype":"task_started","task_id":"t-1"}`)
	r := &captureRouter{}
	act, err := Route(context.Background(), raw, r)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}
	if act != RouterActionContinue {
		t.Fatalf("expected Continue, got %v", act)
	}
	if r.taskStarted == nil {
		t.Fatalf("expected OnTaskStarted")
	}
	if len(r.calls) != 1 || r.calls[0] != "task_started" {
		t.Fatalf("unexpected calls: %v", r.calls)
	}
}

// TestRouteUnknownEventType verifies FR-107: an unrecognized type lands
// on OnUnknown with Raw populated and no Bail.
func TestRouteUnknownEventType(t *testing.T) {
	raw := []byte(`{"type":"quantum_telemetry","payload":{"qubits":42}}`)
	r := &captureRouter{}
	act, err := Route(context.Background(), raw, r)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}
	if act != RouterActionContinue {
		t.Fatalf("expected Continue, got %v", act)
	}
	if r.unknown == nil {
		t.Fatalf("expected OnUnknown")
	}
	if r.unknown.Type != "quantum_telemetry" {
		t.Errorf("Type: got %q", r.unknown.Type)
	}
	if len(r.unknown.Raw) == 0 {
		t.Errorf("Raw should carry the line for logging")
	}
}

// TestRouteUnknownSystemSubtype verifies a system event with an
// unrecognized subtype routes to OnUnknown (not silently treated as
// init). Forward-compat for hypothetical future system subtypes.
func TestRouteUnknownSystemSubtype(t *testing.T) {
	raw := []byte(`{"type":"system","subtype":"compaction","session_id":"sid"}`)
	r := &captureRouter{}
	act, err := Route(context.Background(), raw, r)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}
	if act != RouterActionContinue {
		t.Fatalf("expected Continue, got %v", act)
	}
	if r.unknown == nil {
		t.Fatalf("expected OnUnknown")
	}
	if r.unknown.Type != "system" || r.unknown.Subtype != "compaction" {
		t.Errorf("Type/Subtype: got %q/%q", r.unknown.Type, r.unknown.Subtype)
	}
	// Init should NOT have fired.
	if r.init != nil {
		t.Errorf("OnInit fired unexpectedly for unknown subtype")
	}
}

// TestRouteMalformedJSON verifies a non-JSON line returns
// RouterActionBail + error without invoking any On* method. FR-106:
// parse errors terminate the subprocess with exit_reason='parse_error'.
func TestRouteMalformedJSON(t *testing.T) {
	raw := []byte(`{{not json`)
	r := &captureRouter{}
	act, err := Route(context.Background(), raw, r)
	if err == nil {
		t.Fatalf("expected error for malformed JSON")
	}
	if act != RouterActionBail {
		t.Fatalf("expected Bail, got %v", act)
	}
	if len(r.calls) != 0 {
		t.Errorf("no router method should fire on parse error; got %v", r.calls)
	}
}

// TestRouteInitBailsOnMCPFailure verifies the OnInit return value
// propagates through Route. This is the load-bearing path for
// acceptance criterion 11 (broken MCP config bail).
func TestRouteInitBailsOnMCPFailure(t *testing.T) {
	raw := []byte(`{"type":"system","subtype":"init","cwd":"/","session_id":"s","model":"m","tools":[],"mcp_servers":[{"name":"postgres","status":"failed"}]}`)
	r := &captureRouter{initAction: RouterActionBail}
	act, err := Route(context.Background(), raw, r)
	if err != nil {
		t.Fatalf("Route returned error: %v", err)
	}
	if act != RouterActionBail {
		t.Fatalf("expected Route to propagate OnInit's Bail action")
	}
}

// TestAssistantEventToolUses verifies the M2.2 tool_use extraction. A
// real Claude assistant line can carry multiple content blocks with
// mixed types (thinking / text / tool_use). The router should populate
// ToolUses with only the tool_use blocks, carrying name/id/input.
func TestAssistantEventToolUses(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"model":"claude-haiku","content":[` +
		`{"type":"thinking","signature":"x"},` +
		`{"type":"text","text":"calling two tools"},` +
		`{"type":"tool_use","id":"toolu_01","name":"mempalace_add_drawer","input":{"wing":"wing_x","room":"hall_events","content":"..."}},` +
		`{"type":"tool_use","id":"toolu_02","name":"mempalace_kg_add","input":{"subject":"a","predicate":"p","object":"b"}}` +
		`]}}`)
	r := &captureRouter{}
	if _, err := Route(context.Background(), raw, r); err != nil {
		t.Fatalf("Route err: %v", err)
	}
	if r.assistant == nil {
		t.Fatalf("expected OnAssistant to fire")
	}
	a := *r.assistant
	if len(a.ToolUses) != 2 {
		t.Fatalf("expected 2 tool_uses, got %d: %+v", len(a.ToolUses), a.ToolUses)
	}
	if a.ToolUses[0].Name != "mempalace_add_drawer" {
		t.Errorf("ToolUses[0].Name=%q", a.ToolUses[0].Name)
	}
	if a.ToolUses[0].ToolUseID != "toolu_01" {
		t.Errorf("ToolUses[0].ToolUseID=%q", a.ToolUses[0].ToolUseID)
	}
	if a.ToolUses[1].Name != "mempalace_kg_add" {
		t.Errorf("ToolUses[1].Name=%q", a.ToolUses[1].Name)
	}
	// InputRaw preserved as json.RawMessage for downstream processing.
	if len(a.ToolUses[0].InputRaw) == 0 {
		t.Errorf("ToolUses[0].InputRaw is empty")
	}
}

// TestRouter_OnStreamEvent_TextDelta feeds a captured content_block_delta
// stream_event line and asserts OnStreamEvent fires with the expected
// inner type, delta type, and text payload populated. Pre-M5.1 the line
// would have routed to OnUnknown.
func TestRouter_OnStreamEvent_TextDelta(t *testing.T) {
	raw := []byte(`{"type":"stream_event","session_id":"sid-1","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Purple."}},"uuid":"u-1"}`)
	r := &captureRouter{initAction: RouterActionContinue}
	act, err := Route(context.Background(), raw, r)
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if act != RouterActionContinue {
		t.Errorf("act = %v, want Continue", act)
	}
	if r.streamEvent == nil {
		t.Fatal("OnStreamEvent did not fire")
	}
	e := *r.streamEvent
	if e.SessionID != "sid-1" {
		t.Errorf("SessionID=%q", e.SessionID)
	}
	if e.InnerType != "content_block_delta" {
		t.Errorf("InnerType=%q", e.InnerType)
	}
	if e.Inner.DeltaType != "text_delta" {
		t.Errorf("DeltaType=%q", e.Inner.DeltaType)
	}
	if e.Inner.DeltaText != "Purple." {
		t.Errorf("DeltaText=%q", e.Inner.DeltaText)
	}
}

// TestRouter_OnStreamEvent_MessageStartCacheTokens covers the SC-002
// cache-token signal: cache_creation_input_tokens + cache_read_input_
// tokens populate from event.message.usage.
func TestRouter_OnStreamEvent_MessageStartCacheTokens(t *testing.T) {
	raw := []byte(`{"type":"stream_event","session_id":"sid-2","event":{"type":"message_start","message":{"id":"msg_x","model":"claude-sonnet-4-6","role":"assistant","content":[],"usage":{"input_tokens":3,"cache_creation_input_tokens":774,"cache_read_input_tokens":14249,"output_tokens":1}}},"uuid":"u-2"}`)
	r := &captureRouter{initAction: RouterActionContinue}
	if _, err := Route(context.Background(), raw, r); err != nil {
		t.Fatalf("Route: %v", err)
	}
	if r.streamEvent == nil {
		t.Fatal("OnStreamEvent did not fire")
	}
	e := *r.streamEvent
	if e.InnerType != "message_start" {
		t.Errorf("InnerType=%q", e.InnerType)
	}
	if e.Inner.InputTokens != 3 {
		t.Errorf("InputTokens=%d", e.Inner.InputTokens)
	}
	if e.Inner.OutputTokens != 1 {
		t.Errorf("OutputTokens=%d", e.Inner.OutputTokens)
	}
	if e.Inner.CacheReadInput != 14249 {
		t.Errorf("CacheReadInput=%d", e.Inner.CacheReadInput)
	}
	if e.Inner.CacheCreationInput != 774 {
		t.Errorf("CacheCreationInput=%d", e.Inner.CacheCreationInput)
	}
}

// TestRouter_OnStreamEvent_MalformedReturnsBail covers the parse-error
// path: a malformed stream_event line returns (RouterActionBail, error)
// rather than routing through to OnStreamEvent.
func TestRouter_OnStreamEvent_MalformedReturnsBail(t *testing.T) {
	raw := []byte(`{"type":"stream_event","event":{not valid json}}`)
	r := &captureRouter{initAction: RouterActionContinue}
	act, err := Route(context.Background(), raw, r)
	if err == nil {
		t.Fatal("Route: want error from malformed JSON, got nil")
	}
	if act != RouterActionBail {
		t.Errorf("act = %v, want Bail", act)
	}
	if r.streamEvent != nil {
		t.Errorf("OnStreamEvent should NOT fire on malformed lines")
	}
}
