package mempalace

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Tests for decodeOneQueryResponse + parseQueryResponses, the helpers
// extracted from Query during the M3 quality cleanup. The integration
// tests cover Query end-to-end against a real palace; these unit tests
// pin the parsing behaviour without needing Docker.

func encodeRPC(t *testing.T, msgs ...map[string]any) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, m := range msgs {
		if err := json.NewEncoder(&buf).Encode(m); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	return buf.Bytes()
}

func TestParseQueryResponsesEmpty(t *testing.T) {
	drawers, triples, err := parseQueryResponses(nil, TimeWindow{})
	if err != nil {
		t.Fatalf("nil stdout: want nil err, got %v", err)
	}
	if len(drawers) != 0 || len(triples) != 0 {
		t.Errorf("nil stdout: want empty slices, got %d drawers / %d triples", len(drawers), len(triples))
	}
}

func TestParseQueryResponsesInitOnly(t *testing.T) {
	// id=1 is the initialize response; produces neither drawers nor
	// triples but should not error.
	stdout := encodeRPC(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": "{}"}}},
	})
	drawers, triples, err := parseQueryResponses(stdout, TimeWindow{})
	if err != nil {
		t.Fatalf("init-only: %v", err)
	}
	if len(drawers) != 0 || len(triples) != 0 {
		t.Errorf("init-only: want empty, got %d drawers / %d triples", len(drawers), len(triples))
	}
}

func TestParseQueryResponsesSearchHit(t *testing.T) {
	now := time.Now().UTC()
	searchPayload := map[string]any{
		"results": []map[string]any{
			{
				"wing":       "engineering",
				"content":    "ticket xyz body",
				"created_at": now.Format(time.RFC3339),
			},
		},
	}
	searchText, _ := json.Marshal(searchPayload)
	stdout := encodeRPC(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": string(searchText)}}},
	})
	drawers, triples, err := parseQueryResponses(stdout, TimeWindow{})
	if err != nil {
		t.Fatalf("search hit: %v", err)
	}
	if len(drawers) != 1 {
		t.Fatalf("search hit: want 1 drawer, got %d", len(drawers))
	}
	if drawers[0].Wing != "engineering" {
		t.Errorf("drawer wing: %q", drawers[0].Wing)
	}
	if drawers[0].Body != "ticket xyz body" {
		t.Errorf("drawer body: %q", drawers[0].Body)
	}
	if len(triples) != 0 {
		t.Errorf("search hit: want 0 triples, got %d", len(triples))
	}
}

func TestParseQueryResponsesKGFacts(t *testing.T) {
	// MemPalace 3.3.2 returns kg_query results under the "facts" key
	// (see M2.2 live-run finding 2026-04-23). parseKGQueryPayload
	// tolerates both "facts" and "triples".
	now := time.Now().UTC()
	kgPayload := map[string]any{
		"facts": []map[string]any{
			{
				"subject":    "ticket-1",
				"predicate":  "transitions_to",
				"object":     "qa_review",
				"valid_from": now.Format(time.RFC3339),
			},
		},
	}
	kgText, _ := json.Marshal(kgPayload)
	stdout := encodeRPC(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": string(kgText)}}},
	})
	drawers, triples, err := parseQueryResponses(stdout, TimeWindow{})
	if err != nil {
		t.Fatalf("kg facts: %v", err)
	}
	if len(drawers) != 0 {
		t.Errorf("kg facts: want 0 drawers, got %d", len(drawers))
	}
	if len(triples) != 1 {
		t.Fatalf("kg facts: want 1 triple, got %d", len(triples))
	}
	if triples[0].Subject != "ticket-1" || triples[0].Object != "qa_review" {
		t.Errorf("triple shape unexpected: %+v", triples[0])
	}
}

func TestParseQueryResponsesMCPError(t *testing.T) {
	errResp := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"error":   map[string]any{"code": -32602, "message": "invalid params"},
	}
	stdout := encodeRPC(t, errResp)
	_, _, err := parseQueryResponses(stdout, TimeWindow{})
	if err == nil {
		t.Fatal("mcp error: want err, got nil")
	}
	if !strings.Contains(err.Error(), "mcp error (id=2)") {
		t.Errorf("err should contain 'mcp error (id=2)': %v", err)
	}
}

func TestParseQueryResponsesMalformed(t *testing.T) {
	_, _, err := parseQueryResponses([]byte("{not-json"), TimeWindow{})
	if err == nil {
		t.Fatal("malformed: want err, got nil")
	}
}

func TestDecodeOneQueryResponseEmptyContent(t *testing.T) {
	// Result with empty content array should produce no drawers / no
	// triples / no error — the caller's accumulator simply skips it.
	stdout := encodeRPC(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"result":  map[string]any{"content": []map[string]any{}},
	})
	dec := json.NewDecoder(bytes.NewReader(stdout))
	drawers, triples, err := decodeOneQueryResponse(dec)
	if err != nil {
		t.Fatalf("empty content: %v", err)
	}
	if drawers != nil || triples != nil {
		t.Errorf("empty content: want both nil, got drawers=%v triples=%v", drawers, triples)
	}
}

func TestDecodeOneQueryResponseUnknownIDIgnored(t *testing.T) {
	// id=99 doesn't match any handled case; should be a no-op.
	stdout := encodeRPC(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      99,
		"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": "{}"}}},
	})
	dec := json.NewDecoder(bytes.NewReader(stdout))
	drawers, triples, err := decodeOneQueryResponse(dec)
	if err != nil {
		t.Fatalf("unknown id: %v", err)
	}
	if drawers != nil || triples != nil {
		t.Errorf("unknown id should produce nothing, got drawers=%v triples=%v", drawers, triples)
	}
}
