package finalize

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// validPayload returns a raw JSON payload that passes every rule. Used
// as the baseline for schema-violation tests — each schema_test.go
// test mutates one field and asserts rejection.
func validPayload() string {
	return `{
		"ticket_id": "11111111-2222-4333-8444-555566667777",
		"outcome": "Implemented hello.md per spec",
		"diary_entry": {
			"rationale": "The ticket asked for a one-paragraph hello markdown. Wrote it in changes/ with the ticket id in the filename so the qa agent can confirm presence.",
			"artifacts": ["changes/hello.md"],
			"blockers": [],
			"discoveries": []
		},
		"kg_triples": [
			{"subject": "agent_instance_abc", "predicate": "completed", "object": "ticket_111", "valid_from": "now"}
		]
	}`
}

// TestToolDescriptorIsV1 — the tool descriptor carries the
// garrison.finalize_ticket.v1 version tag in its description.
func TestToolDescriptorIsV1(t *testing.T) {
	d := ToolDescriptor()
	desc, _ := d["description"].(string)
	if !strings.Contains(desc, SchemaVersion) {
		t.Errorf("description missing schema version tag %q: %s", SchemaVersion, desc)
	}
	name, _ := d["name"].(string)
	if name != "finalize_ticket" {
		t.Errorf("name = %q; want finalize_ticket", name)
	}
	// inputSchema should declare required top-level fields.
	schema, ok := d["inputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("inputSchema missing or not an object")
	}
	required, _ := schema["required"].([]string)
	wantReq := map[string]bool{"ticket_id": true, "outcome": true, "diary_entry": true, "kg_triples": true}
	for _, r := range required {
		if !wantReq[r] {
			t.Errorf("unexpected required field %q", r)
		}
		delete(wantReq, r)
	}
	for missing := range wantReq {
		t.Errorf("required field %q missing from inputSchema.required", missing)
	}
}

// TestValidateHappyPath — the baseline valid payload validates; returns
// a FinalizePayload with the "now" literal substituted to a concrete
// time near time.Now().
func TestValidateHappyPath(t *testing.T) {
	before := time.Now().UTC()
	p, err := Validate(json.RawMessage(validPayload()))
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("Validate(valid) returned err: %v", err)
	}
	if p.TicketID != "11111111-2222-4333-8444-555566667777" {
		t.Errorf("TicketID = %q", p.TicketID)
	}
	if p.Outcome != "Implemented hello.md per spec" {
		t.Errorf("Outcome = %q", p.Outcome)
	}
	if len(p.KGTriples) != 1 {
		t.Fatalf("kg_triples = %d; want 1", len(p.KGTriples))
	}
	vf := p.KGTriples[0].ValidFrom
	if vf.Before(before) || vf.After(after) {
		t.Errorf("valid_from = %s; expected between %s and %s (now-substituted)", vf, before, after)
	}
	if vf.Location() != time.UTC {
		t.Errorf("valid_from should be UTC; got %s", vf.Location())
	}
}
