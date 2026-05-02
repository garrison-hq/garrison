// QueryClient is the read-only operator-facing surface over MemPalace
// for the M5.4 knows-pane (T007). Mirrors the M2.2 Client transport
// (docker exec + stdio JSON-RPC) but exposes the recent-drawers and
// recent-KG-triples surface the dashboard needs.
//
// Stateless; one fresh `python -m mempalace.mcp_server` process per
// call. The MCP tool surface is verified against `docs/research/m2-spike.md`
// §3.6: `mempalace_list_drawers` (no args, palace-wide enumerate) and
// `mempalace_kg_timeline` (no args, chronological triples). No
// cross-wing aggregation is needed — both tools return palace-wide
// data; recency ordering and limit truncation happen client-side.
//
// Design deviation flagged at /garrison-implement T007 verification
// step: the M5.4 plan (specs/013-m5-4-knows-pane/plan.md §3) described
// this client as HTTP-sidecar-based, but verification against the
// m2-spike + Dockerfile.mempalace + the existing M2.2 Client confirmed
// MemPalace is stdio-only. The data contract is unaffected; only the
// transport differs from the plan's literal wording. The plan's open
// question §"Exact MemPalace sidecar API" anticipated this.

package mempalace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/garrison-hq/garrison/supervisor/internal/dockerexec"
)

// BodyPreviewMaxRunes is the supervisor-side body-preview cap for drawer
// entries returned to the dashboard. UTF-8-safe truncation: never splits
// a multi-byte rune. Per spec FR-685 / plan §3.
const BodyPreviewMaxRunes = 200

// QueryClient is the read-only knows-pane surface. Fields mirror the
// M2.2 Client: same DockerExec seam, same MCP server invocation shape.
type QueryClient struct {
	DockerBin          string
	MempalaceContainer string
	PalacePath         string
	DockerHost         string
	Timeout            time.Duration
	Exec               dockerexec.DockerExec
	Logger             *slog.Logger
}

// NewQueryClient is a convenience constructor mirroring the codebase's
// preference for explicit wire-up over reflection / DI containers.
func NewQueryClient(exec dockerexec.DockerExec, container, palacePath string, logger *slog.Logger) *QueryClient {
	return &QueryClient{
		Exec:               exec,
		MempalaceContainer: container,
		PalacePath:         palacePath,
		Logger:             logger,
	}
}

// DrawerEntry is the read-side projection for a recent palace write.
// BodyPreview is supervisor-side-truncated to ≤BodyPreviewMaxRunes (200)
// runes, UTF-8-safe.
type DrawerEntry struct {
	ID                  string    `json:"id"`
	DrawerName          string    `json:"drawer_name"`
	RoomName            string    `json:"room_name"`
	WingName            string    `json:"wing_name"`
	SourceAgentRoleSlug string    `json:"source_agent_role_slug,omitempty"`
	WrittenAt           time.Time `json:"written_at"`
	BodyPreview         string    `json:"body_preview"`
}

// KGTriple is the read-side projection for a recent KG fact. The optional
// pointer fields are nil when MemPalace does not supply the corresponding
// JSON key (e.g. a triple written without `source_closet`).
type KGTriple struct {
	ID                  string    `json:"id"`
	Subject             string    `json:"subject"`
	Predicate           string    `json:"predicate"`
	Object              string    `json:"object"`
	SourceTicketID      *string   `json:"source_ticket_id,omitempty"`
	SourceAgentRoleSlug *string   `json:"source_agent_role_slug,omitempty"`
	WrittenAt           time.Time `json:"written_at"`
}

// ErrSidecarUnreachable wraps any failure reaching MemPalace via docker
// exec (timeout, non-zero exit, malformed JSON-RPC, parse error). The
// dashboardapi handler (T010) maps this to HTTP 503 MempalaceUnreachable.
var ErrSidecarUnreachable = errors.New("mempalace: sidecar unreachable")

// RecentDrawers returns the most recent `limit` drawer entries across
// the entire palace, ordered by WrittenAt DESC. BodyPreview is truncated
// to BodyPreviewMaxRunes (200 runes, UTF-8-safe).
//
// limit ≤ 0 returns no entries (defensive — the dashboardapi handler
// clamps incoming requests, but this method is also reachable from
// other callers).
func (c *QueryClient) RecentDrawers(ctx context.Context, limit int) ([]DrawerEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	if c.MempalaceContainer == "" || c.PalacePath == "" {
		return nil, fmt.Errorf(errFmtMissingPalacePath, ErrSidecarUnreachable)
	}
	if c.Exec == nil {
		return nil, fmt.Errorf(errFmtNoDockerExec, ErrSidecarUnreachable)
	}

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout, stderr, err := c.Exec.Run(runCtx, c.execArgs(), bytes.NewReader(buildListDrawersRequest()))
	if err != nil {
		return nil, fmt.Errorf(errFmtDockerExecFailed, ErrSidecarUnreachable, err, stderr)
	}

	items, err := parseListDrawersResponse(stdout)
	if err != nil {
		return nil, fmt.Errorf(errFmtParse, ErrSidecarUnreachable, err)
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].WrittenAt.After(items[j].WrittenAt)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	for i := range items {
		items[i].BodyPreview = truncateRunes(items[i].BodyPreview, BodyPreviewMaxRunes)
	}
	return items, nil
}

// RecentKGTriples returns the most recent `limit` KG triples across the
// palace, ordered by WrittenAt DESC.
func (c *QueryClient) RecentKGTriples(ctx context.Context, limit int) ([]KGTriple, error) {
	if limit <= 0 {
		return nil, nil
	}
	if c.MempalaceContainer == "" || c.PalacePath == "" {
		return nil, fmt.Errorf(errFmtMissingPalacePath, ErrSidecarUnreachable)
	}
	if c.Exec == nil {
		return nil, fmt.Errorf(errFmtNoDockerExec, ErrSidecarUnreachable)
	}

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout, stderr, err := c.Exec.Run(runCtx, c.execArgs(), bytes.NewReader(buildKGTimelineRequest()))
	if err != nil {
		return nil, fmt.Errorf(errFmtDockerExecFailed, ErrSidecarUnreachable, err, stderr)
	}

	items, err := parseKGTimelineResponse(stdout)
	if err != nil {
		return nil, fmt.Errorf(errFmtParse, ErrSidecarUnreachable, err)
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].WrittenAt.After(items[j].WrittenAt)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// Error-format constants shared across the three sidecar-call methods
// (RecentDrawers, RecentKGTriples, KgQueryByTicketID). Pulled into
// constants per SonarCloud duplicate-string check; behaviour-preserving.
const (
	errFmtMissingPalacePath = "%w: missing container or palace path"
	errFmtNoDockerExec      = "%w: no DockerExec"
	errFmtDockerExecFailed  = "%w: docker exec: %v: stderr=%s"
	errFmtParse             = "%w: parse: %v"
)

// KgQueryByTicketID returns every KG triple that mentions the supplied
// ticket id (as subject or object). Unlike RecentKGTriples (timeline-
// style, palace-wide) this calls mempalace_kg_query with a ticket-keyed
// filter so the M6 hygiene evaluator can decide missing_kg_facts on a
// per-ticket basis. Returns ErrSidecarUnreachable on docker exec or
// parse failure (the evaluator skips the predicate on that error per
// the soft-gates posture).
func (c *QueryClient) KgQueryByTicketID(ctx context.Context, ticketIDText string) ([]KGTriple, error) {
	if ticketIDText == "" {
		return nil, nil
	}
	if c.MempalaceContainer == "" || c.PalacePath == "" {
		return nil, fmt.Errorf(errFmtMissingPalacePath, ErrSidecarUnreachable)
	}
	if c.Exec == nil {
		return nil, fmt.Errorf(errFmtNoDockerExec, ErrSidecarUnreachable)
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr, err := c.Exec.Run(runCtx, c.execArgs(), bytes.NewReader(buildKGQueryByTicketRequest(ticketIDText)))
	if err != nil {
		return nil, fmt.Errorf(errFmtDockerExecFailed, ErrSidecarUnreachable, err, stderr)
	}
	items, err := parseKGTimelineResponse(stdout)
	if err != nil {
		return nil, fmt.Errorf(errFmtParse, ErrSidecarUnreachable, err)
	}
	// Filter client-side as a defensive shim: mempalace_kg_query's
	// subject-filter semantics vary by version. Keep only triples that
	// actually mention the ticket id (subject or object).
	out := make([]KGTriple, 0, len(items))
	for _, it := range items {
		if it.Subject == ticketIDText || it.Object == ticketIDText {
			out = append(out, it)
		}
	}
	return out, nil
}

// buildKGQueryByTicketRequest composes initialize (id=1) + tools/call
// mempalace_kg_query (id=2) with the ticket id as the subject filter.
// The arguments shape follows the M5.4 RecentKGTriples pattern; the
// palace's parser tolerates `subject` and the matching `object` filter
// shape per the M2.2 live-run finding 2026-04-23.
func buildKGQueryByTicketRequest(ticketIDText string) []byte {
	init := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": mcpClientName, "version": "0"},
		},
	}
	kgQuery := map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": rpcMethodToolsCall,
		"params": map[string]any{
			"name": "mempalace_kg_query",
			"arguments": map[string]any{
				"subject": ticketIDText,
			},
		},
	}
	var buf bytes.Buffer
	for _, msg := range []any{init, kgQuery} {
		_ = json.NewEncoder(&buf).Encode(msg)
	}
	return buf.Bytes()
}

func (c *QueryClient) execArgs() []string {
	return []string{
		"exec", "-i", c.MempalaceContainer,
		"python", "-m", mcpServerModule,
		mcpPalaceFlag, c.PalacePath,
	}
}

// buildListDrawersRequest composes initialize (id=1) + tools/call
// mempalace_list_drawers (id=2). Mirrors the M2.2 Client request-builder
// shape exactly.
func buildListDrawersRequest() []byte {
	init := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": mcpClientName, "version": "0"},
		},
	}
	listDrawers := map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": rpcMethodToolsCall,
		"params": map[string]any{
			"name":      "mempalace_list_drawers",
			"arguments": map[string]any{},
		},
	}
	var buf bytes.Buffer
	for _, msg := range []any{init, listDrawers} {
		_ = json.NewEncoder(&buf).Encode(msg)
	}
	return buf.Bytes()
}

// buildKGTimelineRequest composes initialize (id=1) + tools/call
// mempalace_kg_timeline (id=2).
func buildKGTimelineRequest() []byte {
	init := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": mcpClientName, "version": "0"},
		},
	}
	kgTimeline := map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": rpcMethodToolsCall,
		"params": map[string]any{
			"name":      "mempalace_kg_timeline",
			"arguments": map[string]any{},
		},
	}
	var buf bytes.Buffer
	for _, msg := range []any{init, kgTimeline} {
		_ = json.NewEncoder(&buf).Encode(msg)
	}
	return buf.Bytes()
}

// listDrawersItem mirrors the JSON shape MemPalace embeds in
// content[0].text for the list_drawers tool. Tolerant of MemPalace
// version drift: accepts `id` or `drawer_id`, body via `body` or
// `content` or `body_preview` or `content_preview` (3.3.2 uses the
// last), `wing` or `wing_name`, `room` or `room_name`, `name` or
// `drawer_name`, `created_at` or `written_at`.
//
// Note on timestamps: MemPalace 3.3.2's mempalace_list_drawers does
// NOT return a created/written timestamp at all. WrittenAt remains
// zero-time for those rows; the dashboard renders nothing or a
// placeholder. Reaching for a per-drawer timestamp would require a
// follow-up mempalace_get_drawer call per id (O(N) round-trips) —
// out of scope for the M5.4 list view; tracked-not-fixed.
type listDrawersItem struct {
	ID             string    `json:"id"`
	DrawerID       string    `json:"drawer_id"`
	Wing           string    `json:"wing"`
	WingName       string    `json:"wing_name"`
	Room           string    `json:"room"`
	RoomName       string    `json:"room_name"`
	Name           string    `json:"name"`
	DrawerName     string    `json:"drawer_name"`
	Body           string    `json:"body"`
	Content        string    `json:"content"`
	Preview        string    `json:"body_preview"`
	ContentPreview string    `json:"content_preview"`
	CreatedAt      time.Time `json:"created_at"`
	WrittenAt      time.Time `json:"written_at"`
	// Optional: M2.2 convention encodes the source agent in the wing
	// name (`wing_<role>`); when MemPalace surfaces the role explicitly
	// we honor it here.
	SourceAgentRoleSlug string `json:"source_agent_role_slug,omitempty"`
}

// kgTimelineItem mirrors the kg_timeline JSON shape. Tolerant of
// `source_ticket_id` / `source_closet` field-name variants.
type kgTimelineItem struct {
	ID                  string    `json:"id"`
	Subject             string    `json:"subject"`
	Predicate           string    `json:"predicate"`
	Object              string    `json:"object"`
	ValidFrom           time.Time `json:"valid_from"`
	WrittenAt           time.Time `json:"written_at"`
	SourceTicketID      *string   `json:"source_ticket_id,omitempty"`
	SourceAgentRoleSlug *string   `json:"source_agent_role_slug,omitempty"`
}

// parseListDrawersResponse consumes the newline-delimited JSON-RPC
// responses and extracts the drawer list. Tolerant of wrapper-object
// `{drawers: [...]}` or flat-array text payloads.
func parseListDrawersResponse(stdout []byte) ([]DrawerEntry, error) {
	text, err := extractToolCallPayload(stdout)
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, nil
	}

	var wrapper struct {
		Drawers []listDrawersItem `json:"drawers"`
		Items   []listDrawersItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(text), &wrapper); err == nil {
		if wrapper.Drawers != nil {
			return drawersToEntries(wrapper.Drawers), nil
		}
		if wrapper.Items != nil {
			return drawersToEntries(wrapper.Items), nil
		}
	}
	var flat []listDrawersItem
	if err := json.Unmarshal([]byte(text), &flat); err != nil {
		return nil, fmt.Errorf("decode drawers payload: %w", err)
	}
	return drawersToEntries(flat), nil
}

func drawersToEntries(items []listDrawersItem) []DrawerEntry {
	out := make([]DrawerEntry, 0, len(items))
	for _, it := range items {
		out = append(out, drawerItemToEntry(it))
	}
	return out
}

// drawerItemToEntry collapses the multi-name fallback chains
// (id/drawer_id, wing/wing_name, …, content/preview/body) into a single
// DrawerEntry. MemPalace 3.x has shipped several payload-shape variants;
// the fallback order pins the most-recent canonical name first.
func drawerItemToEntry(it listDrawersItem) DrawerEntry {
	written := it.WrittenAt
	if written.IsZero() {
		written = it.CreatedAt
	}
	return DrawerEntry{
		ID:                  firstNonEmpty(it.ID, it.DrawerID),
		DrawerName:          firstNonEmpty(it.DrawerName, it.Name),
		RoomName:            firstNonEmpty(it.RoomName, it.Room),
		WingName:            firstNonEmpty(it.WingName, it.Wing),
		SourceAgentRoleSlug: it.SourceAgentRoleSlug,
		WrittenAt:           written,
		BodyPreview:         firstNonEmpty(it.Preview, it.ContentPreview, it.Content, it.Body),
	}
}

func firstNonEmpty(candidates ...string) string {
	for _, c := range candidates {
		if c != "" {
			return c
		}
	}
	return ""
}

// parseKGTimelineResponse consumes the newline-delimited JSON-RPC
// responses and extracts the KG triple list. Tolerant of wrapper-object
// `{timeline: [...]}` (MemPalace 3.3.2 — verified live), `{triples: [...]}`,
// or `{facts: [...]}` (M2.2 live-run finding 2026-04-23 against kg_query),
// or flat-array text payloads.
func parseKGTimelineResponse(stdout []byte) ([]KGTriple, error) {
	text, err := extractToolCallPayload(stdout)
	if err != nil {
		return nil, err
	}
	if text == "" {
		return nil, nil
	}

	var wrapper struct {
		Timeline []kgTimelineItem `json:"timeline"`
		Triples  []kgTimelineItem `json:"triples"`
		Facts    []kgTimelineItem `json:"facts"`
	}
	if err := json.Unmarshal([]byte(text), &wrapper); err == nil {
		if wrapper.Timeline != nil {
			return timelineToTriples(wrapper.Timeline), nil
		}
		if wrapper.Triples != nil {
			return timelineToTriples(wrapper.Triples), nil
		}
		if wrapper.Facts != nil {
			return timelineToTriples(wrapper.Facts), nil
		}
	}
	var flat []kgTimelineItem
	if err := json.Unmarshal([]byte(text), &flat); err != nil {
		return nil, fmt.Errorf("decode kg timeline payload: %w", err)
	}
	return timelineToTriples(flat), nil
}

func timelineToTriples(items []kgTimelineItem) []KGTriple {
	out := make([]KGTriple, 0, len(items))
	for _, it := range items {
		written := it.WrittenAt
		if written.IsZero() {
			written = it.ValidFrom
		}
		// MemPalace 3.3.2's kg_timeline doesn't return a per-triple id.
		// Synthesise a stable composite key so React's list keying
		// doesn't collide on repeated (subject, predicate, object)
		// pairs that differ only by valid_from. Format chosen so it's
		// stable across pages-of-results: subject|predicate|object|valid_from.
		id := it.ID
		if id == "" {
			id = it.Subject + "|" + it.Predicate + "|" + it.Object + "|" + written.Format(time.RFC3339)
		}
		out = append(out, KGTriple{
			ID:                  id,
			Subject:             it.Subject,
			Predicate:           it.Predicate,
			Object:              it.Object,
			SourceTicketID:      it.SourceTicketID,
			SourceAgentRoleSlug: it.SourceAgentRoleSlug,
			WrittenAt:           written,
		})
	}
	return out
}

// extractToolCallPayload reads the newline-delimited JSON-RPC stream and
// returns the text payload for the tools/call response (id=2). Returns
// "" if no tools/call response is present (no payload to decode); error
// if the stream is malformed or carries an MCP error.
func extractToolCallPayload(stdout []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for dec.More() {
		var resp rpcResponse
		if err := dec.Decode(&resp); err != nil {
			return "", fmt.Errorf("decode rpcResponse: %w", err)
		}
		if resp.Error != nil {
			return "", fmt.Errorf("mcp error (id=%d): %s", resp.ID, string(*resp.Error))
		}
		if resp.ID != 2 || resp.Result == nil || len(resp.Result.Content) == 0 {
			continue
		}
		return resp.Result.Content[0].Text, nil
	}
	return "", nil
}

// truncateRunes returns s truncated to at most max runes, never splitting
// a multi-byte UTF-8 rune. If s already fits, returns s unchanged.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	count := 0
	for i := range s {
		if count == max {
			return s[:i]
		}
		count++
	}
	return s
}
