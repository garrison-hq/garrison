// Package finalize implements the in-tree MCP server that Claude
// invokes as `supervisor mcp finalize` on every M2.2.1 spawn. It
// exposes exactly one tool, `finalize_ticket`, whose successful call
// triggers the supervisor-side atomic palace+transition write (FR-261).
//
// The server is stateless across tool calls (retry state lives
// supervisor-side in internal/spawn/pipeline.go per FR-257/FR-258);
// the server's only responsibility is schema validation and rejecting
// double-finalize attempts by querying agent_instances (FR-260). See
// plan §"New package internal/finalize" for the full shape.
package finalize

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Schema caps per spec FR-253 + Clarification 2026-04-23 Q3.
const (
	// SchemaVersion appears in the ToolDescriptor's description. The
	// server refuses non-v1 calls (future revisions bump the version).
	SchemaVersion = "garrison.finalize_ticket.v1"

	OutcomeMin       = 10
	OutcomeMax       = 500
	RationaleMin     = 50
	RationaleMax     = 4000
	ArtifactArrayMax = 50
	ArtifactItemMax  = 500
	KGTripleArrayMin = 1
	KGTripleArrayMax = 100
	TripleFieldMin   = 3
	TripleFieldMax   = 500
)

// ErrorType enumerates the response categories per FR-255.
type ErrorType string

const (
	ErrorTypeSchema          ErrorType = "schema"
	ErrorTypePalaceWrite     ErrorType = "palace_write"
	ErrorTypeTransitionWrite ErrorType = "transition_write"
	ErrorTypeBudgetExhausted ErrorType = "budget_exhausted"
)

// ValidationError is the structured reason a payload was rejected. Field
// is a JSON path (e.g. "diary_entry.rationale") or empty for non-field-
// scoped errors. The handler returns these as MCP tool_result payloads;
// the agent can correct its next attempt from the error's contents.
type ValidationError struct {
	ErrorType ErrorType
	Field     string
	Message   string
}

func (v *ValidationError) Error() string {
	if v.Field == "" {
		return fmt.Sprintf("finalize: %s: %s", v.ErrorType, v.Message)
	}
	return fmt.Sprintf("finalize: %s at %s: %s", v.ErrorType, v.Field, v.Message)
}

// DiaryEntry is the structured reflection block inside FinalizePayload.
type DiaryEntry struct {
	Rationale   string   `json:"rationale"`
	Artifacts   []string `json:"artifacts"`
	Blockers    []string `json:"blockers"`
	Discoveries []string `json:"discoveries"`
}

// KGTriple is one knowledge-graph fact. ValidFrom is a concrete time
// — Validate substitutes the "now" literal for time.Now().UTC() at
// validation time per plan §"`valid_from` literal substitution".
type KGTriple struct {
	Subject   string    `json:"subject"`
	Predicate string    `json:"predicate"`
	Object    string    `json:"object"`
	ValidFrom time.Time `json:"-"`
}

// rawKGTriple mirrors the wire shape before valid_from substitution.
type rawKGTriple struct {
	Subject   string `json:"subject"`
	Predicate string `json:"predicate"`
	Object    string `json:"object"`
	ValidFrom string `json:"valid_from"`
}

// FinalizePayload is the validated and substituted form the handler
// passes downstream. The Rational literal "now" never propagates past
// Validate — every KGTriple has a concrete time.Time ValidFrom.
type FinalizePayload struct {
	TicketID   string
	Outcome    string
	DiaryEntry DiaryEntry
	KGTriples  []KGTriple
}

// rawPayload is the on-wire shape the agent sends. Kept private — callers
// only see the validated FinalizePayload.
type rawPayload struct {
	TicketID   string        `json:"ticket_id"`
	Outcome    string        `json:"outcome"`
	DiaryEntry DiaryEntry    `json:"diary_entry"`
	KGTriples  []rawKGTriple `json:"kg_triples"`
}

// uuidRe matches 8-4-4-4-12 hex with optional hyphens; case-insensitive.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ToolDescriptor returns the MCP tool-schema JSON for finalize_ticket.
// The shape mirrors MCP's conventions (inputSchema as a JSON-Schema
// object). Callers marshal this into the `tools/list` response.
func ToolDescriptor() map[string]any {
	stringMin := func(min int) map[string]any {
		return map[string]any{"type": "string", "minLength": min}
	}
	stringRange := func(min, max int) map[string]any {
		return map[string]any{"type": "string", "minLength": min, "maxLength": max}
	}
	return map[string]any{
		"name": "finalize_ticket",
		"description": "Commit the ticket's structured completion. Schema: " + SchemaVersion +
			". The supervisor atomically writes the diary + KG triples to MemPalace " +
			"and transitions the ticket when this call succeeds. This is the only " +
			"way to complete a ticket in M2.2.1 onwards.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"ticket_id", "outcome", "diary_entry", "kg_triples"},
			"properties": map[string]any{
				"ticket_id": map[string]any{"type": "string", "description": "UUID of the ticket being finalized."},
				"outcome":   stringRange(OutcomeMin, OutcomeMax),
				"diary_entry": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"rationale", "artifacts", "blockers", "discoveries"},
					"properties": map[string]any{
						"rationale": stringRange(RationaleMin, RationaleMax),
						"artifacts": map[string]any{
							"type":     "array",
							"maxItems": ArtifactArrayMax,
							"items":    map[string]any{"type": "string", "maxLength": ArtifactItemMax},
						},
						"blockers": map[string]any{
							"type":     "array",
							"maxItems": ArtifactArrayMax,
							"items":    map[string]any{"type": "string", "maxLength": ArtifactItemMax},
						},
						"discoveries": map[string]any{
							"type":     "array",
							"maxItems": ArtifactArrayMax,
							"items":    map[string]any{"type": "string", "maxLength": ArtifactItemMax},
						},
					},
				},
				"kg_triples": map[string]any{
					"type":     "array",
					"minItems": KGTripleArrayMin,
					"maxItems": KGTripleArrayMax,
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"required":             []string{"subject", "predicate", "object", "valid_from"},
						"properties": map[string]any{
							"subject":    stringMin(TripleFieldMin),
							"predicate":  stringMin(TripleFieldMin),
							"object":     stringMin(TripleFieldMin),
							"valid_from": map[string]any{"type": "string", "description": "ISO-8601 timestamp or literal \"now\"."},
						},
					},
				},
			},
		},
	}
}

// Validate runs the hand-written type-switch validator over the raw
// JSON arguments the MCP tools/call carries. Returns the populated
// FinalizePayload (with "now" substituted) on success, or a
// ValidationError pointing at the first field that failed.
//
// "now" substitution happens here per plan §"internal/finalize/tool.go >
// valid_from literal substitution" — downstream code sees only
// concrete time.Time values.
func Validate(raw json.RawMessage) (*FinalizePayload, *ValidationError) {
	var p rawPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &ValidationError{
			ErrorType: ErrorTypeSchema,
			Field:     "",
			Message:   "arguments are not a valid JSON object: " + err.Error(),
		}
	}

	if p.TicketID == "" {
		return nil, &ValidationError{ErrorType: ErrorTypeSchema, Field: "ticket_id", Message: "ticket_id is required"}
	}
	if !uuidRe.MatchString(p.TicketID) {
		return nil, &ValidationError{ErrorType: ErrorTypeSchema, Field: "ticket_id", Message: "ticket_id is not a valid UUID"}
	}

	if n := len(p.Outcome); n < OutcomeMin {
		return nil, &ValidationError{ErrorType: ErrorTypeSchema, Field: "outcome",
			Message: fmt.Sprintf("outcome length %d is below minimum %d", n, OutcomeMin)}
	} else if n > OutcomeMax {
		return nil, &ValidationError{ErrorType: ErrorTypeSchema, Field: "outcome",
			Message: fmt.Sprintf("outcome length %d exceeds maximum %d", n, OutcomeMax)}
	}

	if n := len(p.DiaryEntry.Rationale); n < RationaleMin {
		return nil, &ValidationError{ErrorType: ErrorTypeSchema, Field: "diary_entry.rationale",
			Message: fmt.Sprintf("rationale length %d is below minimum %d", n, RationaleMin)}
	} else if n > RationaleMax {
		return nil, &ValidationError{ErrorType: ErrorTypeSchema, Field: "diary_entry.rationale",
			Message: fmt.Sprintf("rationale length %d exceeds maximum %d", n, RationaleMax)}
	}

	if err := validateStringArray(p.DiaryEntry.Artifacts, "diary_entry.artifacts"); err != nil {
		return nil, err
	}
	if err := validateStringArray(p.DiaryEntry.Blockers, "diary_entry.blockers"); err != nil {
		return nil, err
	}
	if err := validateStringArray(p.DiaryEntry.Discoveries, "diary_entry.discoveries"); err != nil {
		return nil, err
	}

	if n := len(p.KGTriples); n < KGTripleArrayMin {
		return nil, &ValidationError{ErrorType: ErrorTypeSchema, Field: "kg_triples",
			Message: fmt.Sprintf("kg_triples length %d is below minimum %d", n, KGTripleArrayMin)}
	} else if n > KGTripleArrayMax {
		return nil, &ValidationError{ErrorType: ErrorTypeSchema, Field: "kg_triples",
			Message: fmt.Sprintf("kg_triples length %d exceeds maximum %d", n, KGTripleArrayMax)}
	}

	now := time.Now().UTC()
	triples := make([]KGTriple, 0, len(p.KGTriples))
	for i, t := range p.KGTriples {
		fieldPrefix := fmt.Sprintf("kg_triples[%d]", i)
		if err := validateTripleField(t.Subject, fieldPrefix+".subject"); err != nil {
			return nil, err
		}
		if err := validateTripleField(t.Predicate, fieldPrefix+".predicate"); err != nil {
			return nil, err
		}
		if err := validateTripleField(t.Object, fieldPrefix+".object"); err != nil {
			return nil, err
		}
		validFrom, verr := parseValidFrom(t.ValidFrom, fieldPrefix+".valid_from", now)
		if verr != nil {
			return nil, verr
		}
		triples = append(triples, KGTriple{
			Subject:   t.Subject,
			Predicate: t.Predicate,
			Object:    t.Object,
			ValidFrom: validFrom,
		})
	}

	return &FinalizePayload{
		TicketID:   p.TicketID,
		Outcome:    p.Outcome,
		DiaryEntry: p.DiaryEntry,
		KGTriples:  triples,
	}, nil
}

func validateStringArray(arr []string, field string) *ValidationError {
	if len(arr) > ArtifactArrayMax {
		return &ValidationError{ErrorType: ErrorTypeSchema, Field: field,
			Message: fmt.Sprintf("array length %d exceeds maximum %d", len(arr), ArtifactArrayMax)}
	}
	for i, s := range arr {
		if len(s) > ArtifactItemMax {
			return &ValidationError{ErrorType: ErrorTypeSchema, Field: fmt.Sprintf("%s[%d]", field, i),
				Message: fmt.Sprintf("string length %d exceeds maximum %d", len(s), ArtifactItemMax)}
		}
	}
	return nil
}

func validateTripleField(s, field string) *ValidationError {
	if n := len(s); n < TripleFieldMin {
		return &ValidationError{ErrorType: ErrorTypeSchema, Field: field,
			Message: fmt.Sprintf("length %d is below minimum %d", n, TripleFieldMin)}
	} else if n > TripleFieldMax {
		return &ValidationError{ErrorType: ErrorTypeSchema, Field: field,
			Message: fmt.Sprintf("length %d exceeds maximum %d", n, TripleFieldMax)}
	}
	return nil
}

// parseValidFrom accepts either "now" (case-insensitive, substituted
// to the supplied now argument) or an RFC3339 / ISO-8601 timestamp.
// The substitution happens here so every downstream consumer sees a
// concrete time.Time.
func parseValidFrom(s, field string, now time.Time) (time.Time, *ValidationError) {
	if strings.EqualFold(strings.TrimSpace(s), "now") {
		return now, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Try RFC3339Nano fallback.
		if t2, err2 := time.Parse(time.RFC3339Nano, s); err2 == nil {
			return t2.UTC(), nil
		}
		return time.Time{}, &ValidationError{ErrorType: ErrorTypeSchema, Field: field,
			Message: "valid_from must be ISO-8601 or \"now\": " + err.Error()}
	}
	return t.UTC(), nil
}
