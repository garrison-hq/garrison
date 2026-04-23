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
//
// M2.2.2 extends the error-response layer with richer fields
// (Failure/Line/Column/Excerpt/Constraint/Expected/Actual/Hint per
// FR-301) so agents can diagnose + retry schema rejections. The
// schema itself is unchanged (FR-321).
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

	// ActualTruncateMax caps how long an Actual field can be in a
	// tool_result envelope. Applied by the handler per FR-303.
	ActualTruncateMax = 100
)

// ErrorType enumerates the response categories per FR-255.
type ErrorType string

const (
	ErrorTypeSchema          ErrorType = "schema"
	ErrorTypePalaceWrite     ErrorType = "palace_write"
	ErrorTypeTransitionWrite ErrorType = "transition_write"
	ErrorTypeBudgetExhausted ErrorType = "budget_exhausted"
)

// Failure narrows ValidationError into one of three classes per
// Clarifications 2026-04-23 Q1 + Q3. Decode = JSON broken; validation
// = payload shape wrong; state = server refuses on lifecycle grounds
// (already-committed, FR-260).
type Failure string

const (
	FailureDecode     Failure = "decode"
	FailureValidation Failure = "validation"
	FailureState      Failure = "state"
)

// Constraint enumerates the validation-failure subtypes per FR-303.
// Empty (ConstraintNone) for FailureDecode and FailureState responses.
type Constraint string

const (
	ConstraintNone         Constraint = ""
	ConstraintRequired     Constraint = "required"
	ConstraintMinLength    Constraint = "min_length"
	ConstraintMaxLength    Constraint = "max_length"
	ConstraintMinItems     Constraint = "min_items"
	ConstraintMaxItems     Constraint = "max_items"
	ConstraintTypeMismatch Constraint = "type_mismatch"
	ConstraintFormat       Constraint = "format"
)

// ValidationError is the structured reason a payload was rejected.
// Field is a JSON path (e.g. "diary_entry.rationale") or empty for
// non-field-scoped errors. The handler returns these as MCP
// tool_result payloads; the agent can correct its next attempt from
// the error's contents.
//
// M2.2.1 fields: ErrorType, Field, Message (preserved verbatim for
// backward compat per context §"Binding questions" Q9).
// M2.2.2 additions: Failure, Line, Column, Excerpt, Constraint,
// Expected, Actual, Hint — see FR-301.
type ValidationError struct {
	ErrorType ErrorType
	Field     string
	Message   string

	Failure    Failure
	Line       int        // 1-based; 0 when not applicable
	Column     int        // 1-based; 0 when not applicable
	Excerpt    string     // <=40 source bytes; empty when N/A
	Constraint Constraint // empty for decode + state
	Expected   string     // empty for decode + state
	Actual     string     // full value; handler truncates per FR-303
	Hint       string     // non-empty on every error per FR-305
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
// FinalizePayload (with "now" substituted) on success, or a populated
// ValidationError pointing at the first field that failed.
//
// Every error-path constructs the full M2.2.2 rich-error shape (FR-301)
// — Failure class, Constraint name, human-readable Expected/Actual,
// and a rendered Hint string — before returning.
//
// "now" substitution happens here per plan §"internal/finalize/tool.go >
// valid_from literal substitution" — downstream code sees only
// concrete time.Time values.
func Validate(raw json.RawMessage) (*FinalizePayload, *ValidationError) {
	var p rawPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, newDecodeOrTypeError(raw, err)
	}

	if p.TicketID == "" {
		return nil, newValidationError(
			"ticket_id", ConstraintRequired,
			"ticket_id is required",
			"non-empty UUID string", "",
		)
	}
	if !uuidRe.MatchString(p.TicketID) {
		return nil, newValidationError(
			"ticket_id", ConstraintFormat,
			"ticket_id is not a valid UUID",
			"UUID (8-4-4-4-12 hex)", p.TicketID,
		)
	}

	if n := len(p.Outcome); n < OutcomeMin {
		return nil, newValidationError(
			"outcome", ConstraintMinLength,
			fmt.Sprintf("outcome length %d is below minimum %d", n, OutcomeMin),
			fmt.Sprintf("string with min length %d", OutcomeMin), p.Outcome,
		)
	} else if n > OutcomeMax {
		return nil, newValidationError(
			"outcome", ConstraintMaxLength,
			fmt.Sprintf("outcome length %d exceeds maximum %d", n, OutcomeMax),
			fmt.Sprintf("string with max length %d", OutcomeMax), p.Outcome,
		)
	}

	if n := len(p.DiaryEntry.Rationale); n < RationaleMin {
		return nil, newValidationError(
			"diary_entry.rationale", ConstraintMinLength,
			fmt.Sprintf("rationale length %d is below minimum %d", n, RationaleMin),
			fmt.Sprintf("string with min length %d", RationaleMin), p.DiaryEntry.Rationale,
		)
	} else if n > RationaleMax {
		return nil, newValidationError(
			"diary_entry.rationale", ConstraintMaxLength,
			fmt.Sprintf("rationale length %d exceeds maximum %d", n, RationaleMax),
			fmt.Sprintf("string with max length %d", RationaleMax), p.DiaryEntry.Rationale,
		)
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
		return nil, newValidationError(
			"kg_triples", ConstraintMinItems,
			fmt.Sprintf("kg_triples length %d is below minimum %d", n, KGTripleArrayMin),
			fmt.Sprintf("array with min length %d", KGTripleArrayMin),
			fmt.Sprintf("%d items", n),
		)
	} else if n > KGTripleArrayMax {
		return nil, newValidationError(
			"kg_triples", ConstraintMaxItems,
			fmt.Sprintf("kg_triples length %d exceeds maximum %d", n, KGTripleArrayMax),
			fmt.Sprintf("array with max length %d", KGTripleArrayMax),
			fmt.Sprintf("%d items", n),
		)
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

// newDecodeOrTypeError classifies a `json.Unmarshal` failure. A
// *json.UnmarshalTypeError is a schema-shape error (FailureValidation
// + ConstraintTypeMismatch), not a syntax failure — the JSON parsed
// but a field's Go type didn't match. Everything else is treated as a
// decode error with position info from decodePosition().
func newDecodeOrTypeError(raw []byte, err error) *ValidationError {
	if utErr, ok := err.(*json.UnmarshalTypeError); ok {
		verr := &ValidationError{
			ErrorType:  ErrorTypeSchema,
			Field:      utErr.Field,
			Message:    "type mismatch: " + utErr.Error(),
			Failure:    FailureValidation,
			Constraint: ConstraintTypeMismatch,
			Expected:   utErr.Type.String(),
			Actual:     utErr.Value,
		}
		verr.Hint = renderHint(verr)
		return verr
	}

	line, col, excerpt := 1, 1, ""
	if synErr, ok := err.(*json.SyntaxError); ok {
		line, col, excerpt = decodePosition(raw, synErr.Offset)
	}
	verr := &ValidationError{
		ErrorType: ErrorTypeSchema,
		Field:     "",
		Message:   "arguments are not a valid JSON object: " + err.Error(),
		Failure:   FailureDecode,
		Line:      line,
		Column:    col,
		Excerpt:   excerpt,
	}
	verr.Hint = renderHint(verr)
	return verr
}

// newValidationError builds a FailureValidation-class ValidationError
// with Hint rendered from the (Field, Constraint, Expected, Actual)
// tuple. Keeps the Validate() switch legible — one line per rejection
// site instead of nine.
func newValidationError(field string, constraint Constraint, message, expected, actual string) *ValidationError {
	verr := &ValidationError{
		ErrorType:  ErrorTypeSchema,
		Field:      field,
		Message:    message,
		Failure:    FailureValidation,
		Constraint: constraint,
		Expected:   expected,
		Actual:     actual,
	}
	verr.Hint = renderHint(verr)
	return verr
}

// renderHint composes the agent-facing hint from a populated
// ValidationError. Hints are derived from (Failure, Constraint, Field,
// Expected, Actual); per FR-305 boilerplate is acceptable. The hint is
// the single most important field for M2.2.2's compliance thesis — the
// agent reads it and corrects.
func renderHint(verr *ValidationError) string {
	switch verr.Failure {
	case FailureDecode:
		return fmt.Sprintf(
			"the arguments object must be valid JSON at line %d col %d; %s",
			verr.Line, verr.Column, stripMessagePrefix(verr.Message),
		)
	case FailureState:
		return verr.Message
	case FailureValidation:
		return renderValidationHint(verr)
	}
	return verr.Message
}

// renderValidationHint is the per-Constraint branch of renderHint. Kept
// separate so the outer switch stays flat and the constraint-specific
// templates are easy to scan.
func renderValidationHint(verr *ValidationError) string {
	field := verr.Field
	if field == "" {
		field = "(root)"
	}
	switch verr.Constraint {
	case ConstraintRequired:
		return fmt.Sprintf("the `%s` field is required and cannot be empty", field)
	case ConstraintMinLength:
		return fmt.Sprintf("the `%s` field must be %s; you sent length %d", field, verr.Expected, len(verr.Actual))
	case ConstraintMaxLength:
		return fmt.Sprintf("the `%s` field must be %s; you sent length %d", field, verr.Expected, len(verr.Actual))
	case ConstraintMinItems:
		return fmt.Sprintf("the `%s` array must be %s; you sent %s", field, verr.Expected, verr.Actual)
	case ConstraintMaxItems:
		return fmt.Sprintf("the `%s` array must be %s; you sent %s", field, verr.Expected, verr.Actual)
	case ConstraintTypeMismatch:
		return fmt.Sprintf("the `%s` field has the wrong type; expected %s", field, verr.Expected)
	case ConstraintFormat:
		return fmt.Sprintf("the `%s` field has an invalid format; expected %s", field, verr.Expected)
	}
	return verr.Message
}

// stripMessagePrefix removes the "arguments are not a valid JSON
// object: " prefix from a decode message so the hint doesn't repeat
// the framing. Leaves the underlying parser message intact.
func stripMessagePrefix(msg string) string {
	const prefix = "arguments are not a valid JSON object: "
	if strings.HasPrefix(msg, prefix) {
		return msg[len(prefix):]
	}
	return msg
}

func validateStringArray(arr []string, field string) *ValidationError {
	if len(arr) > ArtifactArrayMax {
		return newValidationError(
			field, ConstraintMaxItems,
			fmt.Sprintf("array length %d exceeds maximum %d", len(arr), ArtifactArrayMax),
			fmt.Sprintf("array with max length %d", ArtifactArrayMax),
			fmt.Sprintf("%d items", len(arr)),
		)
	}
	for i, s := range arr {
		if len(s) > ArtifactItemMax {
			return newValidationError(
				fmt.Sprintf("%s[%d]", field, i), ConstraintMaxLength,
				fmt.Sprintf("string length %d exceeds maximum %d", len(s), ArtifactItemMax),
				fmt.Sprintf("string with max length %d", ArtifactItemMax),
				s,
			)
		}
	}
	return nil
}

func validateTripleField(s, field string) *ValidationError {
	if n := len(s); n < TripleFieldMin {
		return newValidationError(
			field, ConstraintMinLength,
			fmt.Sprintf("length %d is below minimum %d", n, TripleFieldMin),
			fmt.Sprintf("string with min length %d", TripleFieldMin), s,
		)
	} else if n > TripleFieldMax {
		return newValidationError(
			field, ConstraintMaxLength,
			fmt.Sprintf("length %d exceeds maximum %d", n, TripleFieldMax),
			fmt.Sprintf("string with max length %d", TripleFieldMax), s,
		)
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
		return time.Time{}, newValidationError(
			field, ConstraintFormat,
			"valid_from must be ISO-8601 or \"now\": "+err.Error(),
			"ISO-8601 timestamp or literal \"now\"", s,
		)
	}
	return t.UTC(), nil
}
