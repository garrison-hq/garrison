package hygiene

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
)

// Client queries MemPalace for drawers + KG triples feeding the hygiene
// evaluator. Per plan §"`internal/hygiene`", each evaluation spawns a
// short-lived `docker exec -i <container> python -m mempalace.mcp_server
// --palace <path>` process, sends two JSON-RPC requests over stdin
// (mempalace_search and mempalace_kg_query), reads responses, closes
// stdin to trigger in-container Python exit, and returns parsed results.
// No long-running daemon in M2.2.
//
// Exec is the DockerExec seam shared with internal/mempalace — tests
// inject a fake that returns canned JSON-RPC responses.
type Client struct {
	DockerBin          string
	MempalaceContainer string
	PalacePath         string
	DockerHost         string
	Timeout            time.Duration // default 10s
	Exec               mempalace.DockerExec
}

// TimeWindow is the [Start, End] range the hygiene checker evaluates
// against. Passed to Client.Query so the palace-side search can be
// scoped to the agent instance's run window; currently the MCP search
// tool doesn't natively filter by time so the client returns all matches
// and the evaluator applies the window filter pure-side.
type TimeWindow struct {
	Start time.Time
	End   time.Time
}

// ErrPalaceQueryFailed is the class of errors Query wraps when the
// docker exec fails (timeout, non-zero exit, malformed JSON-RPC). The
// evaluator receives the error via EvaluationInput.PalaceErr and
// short-circuits to StatusPending.
var ErrPalaceQueryFailed = errors.New("hygiene: palace query failed")

// Query runs the two JSON-RPC tool-calls against a fresh
// mempalace.mcp_server process. Returns ([]drawers, []triples, nil) on
// success or (nil, nil, wrapped ErrPalaceQueryFailed) on any failure —
// the sweep cycle reprocesses on the next tick.
//
// The JSON-RPC protocol is a subset of MCP:
//  1. initialize
//  2. tools/call mempalace_search (wing=<wing>, query=<ticketIDText>)
//  3. tools/call mempalace_kg_query (entity=<ticketIDText>)
//
// The mempalace.mcp_server exits on EOF of stdin; closing the Exec's
// stdin reader after the last request-line triggers clean teardown.
func (c *Client) Query(ctx context.Context, ticketIDText, wing string, window TimeWindow) ([]PalaceDrawer, []PalaceTriple, error) {
	if c.MempalaceContainer == "" || c.PalacePath == "" {
		return nil, nil, fmt.Errorf("%w: missing container or palace path", ErrPalaceQueryFailed)
	}
	if c.Exec == nil {
		return nil, nil, fmt.Errorf("%w: no DockerExec", ErrPalaceQueryFailed)
	}

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reqBody := buildPalaceRequests(ticketIDText, wing)

	args := []string{
		"exec", "-i", c.MempalaceContainer,
		"python", "-m", "mempalace.mcp_server",
		"--palace", c.PalacePath,
	}
	stdout, stderr, err := c.Exec.Run(runCtx, args, bytes.NewReader(reqBody))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: docker exec: %v: stderr=%s", ErrPalaceQueryFailed, err, stderr)
	}

	drawers, triples, parseErr := parsePalaceResponses(stdout, window)
	if parseErr != nil {
		return nil, nil, fmt.Errorf("%w: parse: %v", ErrPalaceQueryFailed, parseErr)
	}
	return drawers, triples, nil
}

// buildPalaceRequests composes the newline-delimited JSON-RPC request
// stream the mempalace.mcp_server reads from stdin. Three calls: init,
// search, kg-query. IDs 1/2/3 for response correlation.
func buildPalaceRequests(ticketIDText, wing string) []byte {
	init := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "garrison-hygiene", "version": "0"},
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

// rpcResponse is the minimum shape we care about from MCP responses.
// Result.Content[0].Text is the JSON-encoded tool return (per MemPalace's
// MCP server: the tool result is stringified JSON inside content[0].text).
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

// parsePalaceResponses consumes the newline-delimited JSON responses
// from mempalace.mcp_server's stdout and extracts drawers (from the
// search response) + triples (from the kg_query response). The window
// parameter is plumbed through so future server-side filtering can use
// it; the current implementation returns all rows and lets the
// Evaluator apply the window filter pure-side.
func parsePalaceResponses(stdout []byte, _ TimeWindow) ([]PalaceDrawer, []PalaceTriple, error) {
	var drawers []PalaceDrawer
	var triples []PalaceTriple

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

// searchItem / kgTriple mirror the JSON MemPalace embeds in content[0].text
// for the two tools we call. Fields are best-effort: the package tolerates
// extra JSON keys (standard encoding/json behaviour) and returns empty
// slices when the shape drifts, so a MemPalace version bump doesn't hard-
// fail hygiene evaluation.
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

func parseSearchPayload(text string) ([]PalaceDrawer, error) {
	// MemPalace returns a wrapper object with a "results" array in
	// typical responses; gracefully handle both shapes.
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

func searchToDrawers(items []searchItem) []PalaceDrawer {
	out := make([]PalaceDrawer, 0, len(items))
	for _, it := range items {
		body := it.Content
		if body == "" {
			body = it.Body
		}
		out = append(out, PalaceDrawer{
			Body:      body,
			Wing:      it.Wing,
			CreatedAt: it.CreatedAt,
		})
	}
	return out
}

func parseKGQueryPayload(text string) ([]PalaceTriple, error) {
	// Live-acceptance finding 2026-04-23: MemPalace 3.3.2's
	// mempalace_kg_query returns {"entity": ..., "facts": [...], "count": N}
	// — the array is keyed "facts" not "triples". Try both field names +
	// flat-array form so the client is robust across MemPalace versions.
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

func kgToTriples(items []kgTriple) []PalaceTriple {
	out := make([]PalaceTriple, 0, len(items))
	for _, it := range items {
		out = append(out, PalaceTriple{
			Subject:   it.Subject,
			Predicate: it.Predicate,
			Object:    it.Object,
			ValidFrom: it.ValidFrom,
		})
	}
	return out
}
