package finalize

import (
	"encoding/json"
	"strings"
	"testing"
)

// mutateJSON takes the baseline valid payload, decodes, applies a
// mutator that reaches into the map, and returns the serialized result.
func mutateJSON(t *testing.T, mutate func(map[string]any)) json.RawMessage {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(validPayload()), &m); err != nil {
		t.Fatalf("decode valid payload: %v", err)
	}
	mutate(m)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	return raw
}

func expectSchemaError(t *testing.T, raw json.RawMessage, wantField string) {
	t.Helper()
	_, verr := Validate(raw)
	if verr == nil {
		t.Fatal("expected a ValidationError; got nil")
	}
	if verr.ErrorType != ErrorTypeSchema {
		t.Errorf("error_type = %q; want %q", verr.ErrorType, ErrorTypeSchema)
	}
	if wantField != "" && verr.Field != wantField {
		t.Errorf("field = %q; want %q (message: %s)", verr.Field, wantField, verr.Message)
	}
}

func TestSchemaRejectsMissingTicketID(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) { delete(m, "ticket_id") })
	expectSchemaError(t, raw, "ticket_id")
}

func TestSchemaRejectsMalformedUUID(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) { m["ticket_id"] = "not-a-uuid" })
	expectSchemaError(t, raw, "ticket_id")
}

func TestSchemaRejectsOutcomeTooShort(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) { m["outcome"] = "short" })
	expectSchemaError(t, raw, "outcome")
}

func TestSchemaRejectsOutcomeTooLong(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) { m["outcome"] = strings.Repeat("a", OutcomeMax+1) })
	expectSchemaError(t, raw, "outcome")
}

func TestSchemaRejectsRationaleTooShort(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) {
		d := m["diary_entry"].(map[string]any)
		d["rationale"] = "too short"
	})
	expectSchemaError(t, raw, "diary_entry.rationale")
}

func TestSchemaRejectsRationaleTooLong(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) {
		d := m["diary_entry"].(map[string]any)
		d["rationale"] = strings.Repeat("a", RationaleMax+1)
	})
	expectSchemaError(t, raw, "diary_entry.rationale")
}

func TestSchemaRejectsEmptyKGTriples(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) { m["kg_triples"] = []any{} })
	expectSchemaError(t, raw, "kg_triples")
}

func TestSchemaRejectsTooManyKGTriples(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) {
		tripleTemplate := map[string]any{
			"subject": "agent_instance_abc", "predicate": "completed", "object": "ticket_xyz", "valid_from": "now",
		}
		many := make([]any, KGTripleArrayMax+1)
		for i := range many {
			many[i] = tripleTemplate
		}
		m["kg_triples"] = many
	})
	expectSchemaError(t, raw, "kg_triples")
}

func TestSchemaRejectsTripleFieldTooShort(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) {
		triples := m["kg_triples"].([]any)
		triples[0].(map[string]any)["subject"] = "ab" // < min 3
	})
	expectSchemaError(t, raw, "kg_triples[0].subject")
}

func TestSchemaAcceptsValidFromNowLiteral(t *testing.T) {
	// Already the default; explicit re-assertion that "now" works.
	raw := mutateJSON(t, func(m map[string]any) {
		triples := m["kg_triples"].([]any)
		triples[0].(map[string]any)["valid_from"] = "NoW" // case-insensitive
	})
	p, verr := Validate(raw)
	if verr != nil {
		t.Fatalf("Validate rejected \"NoW\" literal: %v", verr)
	}
	if p.KGTriples[0].ValidFrom.IsZero() {
		t.Error("ValidFrom is zero; should be now-substituted")
	}
}

func TestSchemaAcceptsValidFromISO(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) {
		triples := m["kg_triples"].([]any)
		triples[0].(map[string]any)["valid_from"] = "2026-04-23T12:00:00Z"
	})
	p, verr := Validate(raw)
	if verr != nil {
		t.Fatalf("Validate rejected ISO timestamp: %v", verr)
	}
	if y := p.KGTriples[0].ValidFrom.Year(); y != 2026 {
		t.Errorf("parsed year = %d; want 2026", y)
	}
}

func TestSchemaRejectsMalformedValidFrom(t *testing.T) {
	raw := mutateJSON(t, func(m map[string]any) {
		triples := m["kg_triples"].([]any)
		triples[0].(map[string]any)["valid_from"] = "yesterday afternoon"
	})
	expectSchemaError(t, raw, "kg_triples[0].valid_from")
}
