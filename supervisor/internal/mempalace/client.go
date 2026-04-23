// Package mempalace's Client is the shared palace access surface for
// the Garrison supervisor. M2.2 shipped it inside internal/hygiene with
// only the read path (Query); M2.2.1 relocates it here so the atomic
// finalize writer (internal/spawn) can share one code path with the
// hygiene checker's read path — see FR-262 and plan §"Changes to
// existing M2.2 packages > internal/mempalace".
//
// The Client is stateless (no long-running subprocess held open). Each
// method call spawns a short-lived `docker exec -i <container> python
// -m mempalace.mcp_server --palace <path>` process, speaks JSON-RPC
// 2.0 over stdin, reads responses over stdout, closes stdin to trigger
// clean in-container Python exit, and returns parsed results.

package mempalace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Client is the shared MemPalace access handle. Fields mirror the M2.2
// shape verbatim (same field names, types, and semantics). Exec is the
// DockerExec seam injected at wire-up time; tests substitute a fake
// implementation to exercise the JSON-RPC protocol without a Docker
// daemon.
type Client struct {
	DockerBin          string
	MempalaceContainer string
	PalacePath         string
	DockerHost         string
	Timeout            time.Duration // default 10s on Query; callers set explicitly on Add*
	Exec               DockerExec
}

// TimeWindow is the [Start, End] range the hygiene checker evaluates
// against. Passed to Client.Query so the palace-side search can be
// scoped to the agent instance's run window; currently the MCP search
// tool doesn't natively filter by time so the client returns all matches
// and the caller (evaluator) applies the window filter pure-side.
type TimeWindow struct {
	Start time.Time
	End   time.Time
}

// Drawer is the read-side projection of a MemPalace drawer. Body carries
// the drawer's textual content; Wing is the drawer's wing name; CreatedAt
// is the MemPalace-side filing timestamp. The hygiene checker searches
// Body for the ticket id substring and filters by CreatedAt against the
// run window.
type Drawer struct {
	Body      string
	Wing      string
	CreatedAt time.Time
}

// Triple is the read-side and write-side shape for a MemPalace KG triple.
// For writes, ValidFrom is supplied by the caller (the atomic writer
// normalises `valid_from: "now"` to time.Now().UTC() before calling
// AddTriples; the client never sees the literal "now"). For reads, it's
// the MemPalace-side timestamp.
type Triple struct {
	Subject   string
	Predicate string
	Object    string
	ValidFrom time.Time
}

// ErrQueryFailed is the class of errors the Client's methods wrap when
// the docker exec fails (timeout, non-zero exit, malformed JSON-RPC).
// Callers test via errors.Is — hygiene maps to StatusPending, the
// atomic writer maps to ExitFinalizePalaceWriteFailed.
var ErrQueryFailed = errors.New("mempalace: palace operation failed")

// Query runs the two JSON-RPC tool-calls (mempalace_search + mempalace_
// kg_query) against a fresh mempalace.mcp_server process. Returns
// ([]Drawer, []Triple, nil) on success or (nil, nil, wrapped
// ErrQueryFailed) on any failure. Behaviour and wire protocol are
// byte-for-byte preserved from M2.2's internal/hygiene/palace.go.
func (c *Client) Query(ctx context.Context, ticketIDText, wing string, window TimeWindow) ([]Drawer, []Triple, error) {
	if c.MempalaceContainer == "" || c.PalacePath == "" {
		return nil, nil, fmt.Errorf("%w: missing container or palace path", ErrQueryFailed)
	}
	if c.Exec == nil {
		return nil, nil, fmt.Errorf("%w: no DockerExec", ErrQueryFailed)
	}

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reqBody := buildQueryRequests(ticketIDText, wing)
	args := []string{
		"exec", "-i", c.MempalaceContainer,
		"python", "-m", "mempalace.mcp_server",
		"--palace", c.PalacePath,
	}
	stdout, stderr, err := c.Exec.Run(runCtx, args, bytes.NewReader(reqBody))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: docker exec: %v: stderr=%s", ErrQueryFailed, err, stderr)
	}

	drawers, triples, parseErr := parseQueryResponses(stdout, window)
	if parseErr != nil {
		return nil, nil, fmt.Errorf("%w: parse: %v", ErrQueryFailed, parseErr)
	}
	return drawers, triples, nil
}

// AddDrawer writes a single drawer to MemPalace via the
// mempalace_add_drawer tool on a fresh mempalace.mcp_server process.
// Returns nil on success or a wrapped ErrQueryFailed on any failure.
//
// Per M2.2 spike §3.7, wings are lazily created by add_drawer — a wing
// that doesn't exist at call time is materialised. The caller is
// responsible for supplying a meaningful wing/room; the supervisor's
// atomic writer supplies wing=<agent's palace_wing>, room="hall_events".
func (c *Client) AddDrawer(ctx context.Context, wing, room, content string) error {
	if c.MempalaceContainer == "" || c.PalacePath == "" {
		return fmt.Errorf("%w: missing container or palace path", ErrQueryFailed)
	}
	if c.Exec == nil {
		return fmt.Errorf("%w: no DockerExec", ErrQueryFailed)
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reqBody := buildAddDrawerRequests(wing, room, content)
	args := []string{
		"exec", "-i", c.MempalaceContainer,
		"python", "-m", "mempalace.mcp_server",
		"--palace", c.PalacePath,
	}
	stdout, stderr, err := c.Exec.Run(runCtx, args, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("%w: docker exec: %v: stderr=%s", ErrQueryFailed, err, stderr)
	}
	if err := expectOKResponses(stdout, []int{1, 2}); err != nil {
		return fmt.Errorf("%w: add_drawer: %v", ErrQueryFailed, err)
	}
	return nil
}

// AddTriples writes N KG triples to MemPalace via N successive
// mempalace_kg_add tool calls against one mempalace.mcp_server process.
// Per M2.2 spike §3.6, the 3.3.2 server has no batch add tool — so we
// issue one tools/call per triple, all inside a single server session
// for efficiency.
//
// Empty triples slice → no-op, returns nil.
//
// Partial failure: if triple k fails, earlier triples 0..k-1 have been
// written and cannot be rolled back (MemPalace has no transaction
// semantics — M2.2 spike §Part 2). The caller (atomic writer in
// internal/spawn) is responsible for handling orphan state via the
// FR-265 palace_write_orphaned log path.
func (c *Client) AddTriples(ctx context.Context, triples []Triple) error {
	if len(triples) == 0 {
		return nil
	}
	if c.MempalaceContainer == "" || c.PalacePath == "" {
		return fmt.Errorf("%w: missing container or palace path", ErrQueryFailed)
	}
	if c.Exec == nil {
		return fmt.Errorf("%w: no DockerExec", ErrQueryFailed)
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reqBody, expectIDs := buildAddTriplesRequests(triples)
	args := []string{
		"exec", "-i", c.MempalaceContainer,
		"python", "-m", "mempalace.mcp_server",
		"--palace", c.PalacePath,
	}
	stdout, stderr, err := c.Exec.Run(runCtx, args, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("%w: docker exec: %v: stderr=%s", ErrQueryFailed, err, stderr)
	}
	if err := expectOKResponses(stdout, expectIDs); err != nil {
		return fmt.Errorf("%w: kg_add: %v", ErrQueryFailed, err)
	}
	return nil
}

// ---------------------------------------------------------------------
// Request builders + response parsers. Private to the package.
// ---------------------------------------------------------------------

// buildQueryRequests composes the newline-delimited JSON-RPC request
// stream for the search + kg_query flow. IDs 1/2/3 for response
// correlation. Preserved verbatim from M2.2's internal/hygiene/palace.go.
func buildQueryRequests(ticketIDText, wing string) []byte {
	init := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "garrison-mempalace", "version": "0"},
		},
	}
	search := map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name": "mempalace_search",
			"arguments": map[string]any{
				"query": ticketIDText,
				"wing":  wing,
			},
		},
	}
	kgQuery := map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name": "mempalace_kg_query",
			"arguments": map[string]any{
				"entity": ticketIDText,
			},
		},
	}

	var buf bytes.Buffer
	for _, msg := range []any{init, search, kgQuery} {
		_ = json.NewEncoder(&buf).Encode(msg) // Encoder appends newline
	}
	return buf.Bytes()
}

// buildAddDrawerRequests composes init + one mempalace_add_drawer call.
// IDs 1/2 for response correlation.
func buildAddDrawerRequests(wing, room, content string) []byte {
	init := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "garrison-mempalace", "version": "0"},
		},
	}
	addDrawer := map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name": "mempalace_add_drawer",
			"arguments": map[string]any{
				"wing":    wing,
				"room":    room,
				"content": content,
			},
		},
	}

	var buf bytes.Buffer
	for _, msg := range []any{init, addDrawer} {
		_ = json.NewEncoder(&buf).Encode(msg)
	}
	return buf.Bytes()
}

// buildAddTriplesRequests composes init + N mempalace_kg_add calls.
// Returns the request bytes and the list of response IDs that must
// succeed (including the init response at id=1).
func buildAddTriplesRequests(triples []Triple) ([]byte, []int) {
	var buf bytes.Buffer
	init := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "garrison-mempalace", "version": "0"},
		},
	}
	_ = json.NewEncoder(&buf).Encode(init)

	ids := []int{1}
	for i, t := range triples {
		id := i + 2
		ids = append(ids, id)
		call := map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "tools/call",
			"params": map[string]any{
				"name": "mempalace_kg_add",
				"arguments": map[string]any{
					"subject":    t.Subject,
					"predicate":  t.Predicate,
					"object":     t.Object,
					"valid_from": t.ValidFrom.UTC().Format(time.RFC3339),
				},
			},
		}
		_ = json.NewEncoder(&buf).Encode(call)
	}
	return buf.Bytes(), ids
}

// ---------------------------------------------------------------------
// MCP protocol shapes (private).
// ---------------------------------------------------------------------

type rpcResponse struct {
	ID     int              `json:"id"`
	Result *rpcResult       `json:"result,omitempty"`
	Error  *json.RawMessage `json:"error,omitempty"`
}
type rpcResult struct {
	Content []rpcContent `json:"content"`
}
type rpcContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// parseQueryResponses consumes the newline-delimited JSON responses for
// the init + search + kg_query flow. Preserved verbatim from M2.2's
// internal/hygiene/palace.go including the "facts"-or-"triples" wrapper
// handling (M2.2 live-run finding 2026-04-23).
func parseQueryResponses(stdout []byte, _ TimeWindow) ([]Drawer, []Triple, error) {
	var drawers []Drawer
	var triples []Triple

	dec := json.NewDecoder(bytes.NewReader(stdout))
	for dec.More() {
		var resp rpcResponse
		if err := dec.Decode(&resp); err != nil {
			return nil, nil, err
		}
		if resp.Error != nil {
			return nil, nil, fmt.Errorf("mcp error (id=%d): %s", resp.ID, string(*resp.Error))
		}
		if resp.Result == nil || len(resp.Result.Content) == 0 {
			continue
		}
		text := resp.Result.Content[0].Text
		switch resp.ID {
		case 2:
			ds, err := parseSearchPayload(text)
			if err != nil {
				return nil, nil, fmt.Errorf("parse search: %w", err)
			}
			drawers = append(drawers, ds...)
		case 3:
			ts, err := parseKGQueryPayload(text)
			if err != nil {
				return nil, nil, fmt.Errorf("parse kg_query: %w", err)
			}
			triples = append(triples, ts...)
		}
	}
	return drawers, triples, nil
}

// expectOKResponses verifies that the stdout carries a success rpcResponse
// for each id in wantIDs and no error responses. Used by AddDrawer and
// AddTriples to validate write outcomes without needing to decode the
// tool return payloads (which add_drawer returns an opaque success
// marker for in 3.3.2).
func expectOKResponses(stdout []byte, wantIDs []int) error {
	seen := make(map[int]bool, len(wantIDs))
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for dec.More() {
		var resp rpcResponse
		if err := dec.Decode(&resp); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if resp.Error != nil {
			return fmt.Errorf("mcp error (id=%d): %s", resp.ID, string(*resp.Error))
		}
		seen[resp.ID] = true
	}
	for _, id := range wantIDs {
		if !seen[id] {
			return fmt.Errorf("missing ok response for id=%d", id)
		}
	}
	return nil
}

// searchItem / kgTriple mirror the JSON MemPalace embeds in content[0].text
// for the search and kg_query tools. Best-effort: the package tolerates
// extra JSON keys (standard encoding/json behaviour) and returns empty
// slices when the shape drifts. Preserved verbatim from M2.2.
type searchItem struct {
	Wing      string    `json:"wing"`
	Content   string    `json:"content"`
	Body      string    `json:"body"` // some versions name it 'body'
	CreatedAt time.Time `json:"created_at"`
}
type kgTriple struct {
	Subject   string    `json:"subject"`
	Predicate string    `json:"predicate"`
	Object    string    `json:"object"`
	ValidFrom time.Time `json:"valid_from"`
}

func parseSearchPayload(text string) ([]Drawer, error) {
	var wrapper struct {
		Results []searchItem `json:"results"`
	}
	if err := json.Unmarshal([]byte(text), &wrapper); err == nil && wrapper.Results != nil {
		return searchToDrawers(wrapper.Results), nil
	}
	var flat []searchItem
	if err := json.Unmarshal([]byte(text), &flat); err != nil {
		return nil, err
	}
	return searchToDrawers(flat), nil
}

func searchToDrawers(items []searchItem) []Drawer {
	out := make([]Drawer, 0, len(items))
	for _, it := range items {
		body := it.Content
		if body == "" {
			body = it.Body
		}
		out = append(out, Drawer{
			Body:      body,
			Wing:      it.Wing,
			CreatedAt: it.CreatedAt,
		})
	}
	return out
}

func parseKGQueryPayload(text string) ([]Triple, error) {
	// M2.2 live-run finding: MemPalace 3.3.2's mempalace_kg_query returns
	// {"entity": ..., "facts": [...], "count": N} — the array is keyed
	// "facts" not "triples". Tolerate both field names + flat-array form.
	var wrapper struct {
		Facts   []kgTriple `json:"facts"`
		Triples []kgTriple `json:"triples"`
	}
	if err := json.Unmarshal([]byte(text), &wrapper); err == nil {
		if wrapper.Facts != nil {
			return kgToTriples(wrapper.Facts), nil
		}
		if wrapper.Triples != nil {
			return kgToTriples(wrapper.Triples), nil
		}
	}
	var flat []kgTriple
	if err := json.Unmarshal([]byte(text), &flat); err != nil {
		return nil, err
	}
	return kgToTriples(flat), nil
}

func kgToTriples(items []kgTriple) []Triple {
	out := make([]Triple, 0, len(items))
	for _, it := range items {
		out = append(out, Triple{
			Subject:   it.Subject,
			Predicate: it.Predicate,
			Object:    it.Object,
			ValidFrom: it.ValidFrom,
		})
	}
	return out
}
