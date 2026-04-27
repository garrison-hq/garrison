// M2.2.1 T007: supervisor-side atomic finalize writer. When the stream-
// json parser observes a successful finalize_ticket tool_result, the
// pipeline's onCommit callback invokes WriteFinalize. This function
// runs the one-shot atomic write per plan §"Subsystem state machines >
// Atomic-write state machine":
//
//   BEGIN tx
//   → SELECT tickets.objective (for the FR-263 diary prepend)
//   → MemPalace.AddDrawer(wing, "hall_events", serialized body)
//   → MemPalace.AddTriples(triples)
//   → INSERT ticket_transitions (hygiene_status='clean')
//   → UPDATE tickets SET column_slug = <to>
//   → UPDATE agent_instances SET status='succeeded', ...
//   → UPDATE event_outbox.processed_at
//   COMMIT
//
// The entire path is bracketed by context.WithTimeout(context.Without
// Cancel(parent), FinalizeWriteTimeout) per FR-261 and clarify Q5, so
// supervisor SIGTERM does not abort an in-flight commit but a hung
// MemPalace sidecar fires finalize_write_timeout within the ceiling.
//
// Every failure branch writes a separate terminal agent_instances row
// (outside the rolled-back tx) with the matching exit_reason /
// hygiene_status per FR-264 / FR-265 / FR-265a.

package spawn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/finalize"
	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FinalizeDiaryHygieneStatus is what WriteFinalize writes into
// ticket_transitions.hygiene_status and agent_instances.hygiene_status
// on the happy path (per FR-261 step c/e and FR-267).
const FinalizeDiaryHygieneStatus = "clean"

// diaryListItemFmt is the YAML list-item line format used in serializeDiary.
const diaryListItemFmt = "  - %s\n"

// FinalizeDeps bundles everything WriteFinalize needs that isn't in
// the payload itself. The caller (pipeline's onCommit) wires these from
// spawn.Deps + per-spawn metadata at invocation time.
type FinalizeWriteDeps struct {
	Pool         *pgxpool.Pool
	Queries      *store.Queries
	Palace       *mempalace.Client
	Logger       *slog.Logger
	WriteTimeout time.Duration
}

// FinalizeMeta is the per-spawn metadata the atomic write needs.
// Populated by spawn.go from the ticket/agent_instance row it already
// holds at the moment the pipeline fires onCommit.
type FinalizeMeta struct {
	AgentInstanceID pgtype.UUID
	TicketID        pgtype.UUID
	EventID         pgtype.UUID
	Wing            string // agent's palace_wing
	FromColumn      string // ticket's current column
	ToColumn        string // destination column (engineer: qa_review, qa: done)
	Cost            pgtype.Numeric
	WakeUpStatus    string // "ok" / "failed" / "skipped"
}

// writeTerminalOutcome is the disposition WriteFinalize records for the
// non-happy paths. The caller (pipeline's onCommit wrapper) may surface
// these as the FinalizeState / exit_reason on the agent_instances row.
type writeTerminalOutcome struct {
	ExitReason    string
	HygieneStatus string
	OrphanWarn    bool   // set when the palace write completed before commit failure
	FailureClass  string // "palace_write" | "commit" | "timeout" — used by the FR-277 log
	UnderlyingErr error
}

// WriteFinalize performs the atomic write. Returns nil on success
// (hygiene_status='clean' committed, terminal row written inside tx).
// On failure, returns an error whose string includes the exit_reason
// canonical value AND writes a separate terminal agent_instances row
// describing the failure. The pipeline's onCommit callback propagates
// the returned error to its logger; the retry counter already marked
// Committed=true optimistically, so this function is responsible for
// the cleanup on the sad paths.
func WriteFinalize(parentCtx context.Context, deps FinalizeWriteDeps, payload *finalize.FinalizePayload, meta FinalizeMeta) error {
	start := time.Now()
	timeout := deps.WriteTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	// WithoutCancel: supervisor SIGTERM does NOT abort an in-flight
	// commit per AGENTS.md rule 6. Timeout: 30s wall-clock ceiling per
	// FR-261 + clarify Q5.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), timeout)
	defer cancel()

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(
		"ticket_id", uuidString(meta.TicketID),
		"agent_instance_id", uuidString(meta.AgentInstanceID),
		"wing", meta.Wing,
	)

	// Step 0: begin tx + read ticket objective.
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return failWithoutOrphan(parentCtx, deps, meta, logger,
			classifyCtxErr(ctx, ExitFinalizePalaceWriteFailed), "palace_write",
			fmt.Errorf("begin tx: %w", err))
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	q := deps.Queries.WithTx(tx)
	objective, err := q.SelectTicketObjective(ctx, meta.TicketID)
	if err != nil {
		return failWithoutOrphan(parentCtx, deps, meta, logger,
			ExitFinalizePalaceWriteFailed, "palace_write",
			fmt.Errorf("SelectTicketObjective: %w", err))
	}

	// M2.3 T009: pattern-scanner hook (FR-418 / FR-419 / D7.3). Scan and
	// redact secret patterns from the finalize payload fields before the
	// MemPalace write so no raw credential ever reaches the palace drawer.
	// Non-blocking: redact-and-warn only (never blocks the write per FR-419).
	//
	// M4 / T015 / FR-115: when matches are found, the FIRST matching
	// label is captured into a local var so writeTransitionRows can
	// record it on ticket_transitions.suspected_secret_pattern_category
	// for the dashboard's hygiene table to render. The supervisor
	// scanner returns multiple labels per call (the M2.3 scanner
	// chains pattern matches), but the dashboard hygiene UI shows
	// one category per row — first-match wins.
	hygieneStatus := FinalizeDiaryHygieneStatus
	var patternCategory string
	if matched := scanAndRedactPayload(payload); len(matched) > 0 {
		hygieneStatus = "suspected_secret_emitted"
		patternCategory = string(matched[0])
		logger.Warn("pattern scanner matched secrets in finalize payload; redacted before palace write",
			"labels", matched)
	}

	if err := writePalaceArtifacts(ctx, parentCtx, deps, meta, payload, objective, logger); err != nil {
		return err
	}
	if err := writeTransitionRows(writeTransitionRowsArgs{
		ctx:             ctx,
		parentCtx:       parentCtx,
		deps:            deps,
		q:               q,
		meta:            meta,
		hygieneStatus:   hygieneStatus,
		patternCategory: patternCategory,
		logger:          logger,
	}); err != nil {
		return err
	}
	if err := writeTerminalAndMark(ctx, parentCtx, deps, q, meta, logger); err != nil {
		return err
	}

	// Step 7: commit. Post-commit failures are the FR-265 palace_write_
	// orphaned case: MemPalace has the drawer + triples, but Postgres
	// couldn't persist the transition.
	if err := tx.Commit(ctx); err != nil {
		class := "commit"
		reason := ExitFinalizeCommitFailed
		if errors.Is(err, context.DeadlineExceeded) || isCtxDeadlineExceeded(ctx) {
			reason = ExitFinalizeWriteTimeout
			class = "timeout"
		}
		return failOrphan(parentCtx, deps, meta, logger, reason, class,
			fmt.Errorf("Commit: %w", err))
	}
	committed = true

	// FR-277 happy-path log.
	logger.Info("atomic_write_committed",
		"triple_count", len(payload.KGTriples),
		"duration_ms", time.Since(start).Milliseconds(),
		"from_column", meta.FromColumn,
		"to_column", meta.ToColumn,
	)
	return nil
}

// writePalaceArtifacts performs the M2.2 MemPalace writes (drawer +
// triples). After AddDrawer succeeds and AddTriples is reached, any
// failure leaves MemPalace with an orphan drawer.
func writePalaceArtifacts(
	ctx, parentCtx context.Context,
	deps FinalizeWriteDeps,
	meta FinalizeMeta,
	payload *finalize.FinalizePayload,
	objective string,
	logger *slog.Logger,
) error {
	body := serializeDiary(objective, meta.TicketID, payload, time.Now().UTC())
	if err := deps.Palace.AddDrawer(ctx, meta.Wing, "hall_events", body); err != nil {
		reason, class := classifyPalaceErr(ctx, err)
		return failWithoutOrphan(parentCtx, deps, meta, logger, reason, class,
			fmt.Errorf("AddDrawer: %w", err))
	}
	triples := toMempalaceTriples(payload.KGTriples)
	if err := deps.Palace.AddTriples(ctx, triples); err != nil {
		reason, class := classifyPalaceErr(ctx, err)
		return failOrphan(parentCtx, deps, meta, logger, reason, class,
			fmt.Errorf("AddTriples: %w", err))
	}
	return nil
}

// writeTransitionRowsArgs bundles the writeTransitionRows args
// per linter S107 (function-arg cap). The set has grown across
// milestones — M2.x added hygiene_status, M4 / T015 added
// patternCategory; passing as a struct keeps the call sites
// readable and the signature stable.
type writeTransitionRowsArgs struct {
	ctx             context.Context
	parentCtx       context.Context
	deps            FinalizeWriteDeps
	q               *store.Queries
	meta            FinalizeMeta
	hygieneStatus   string
	patternCategory string
	logger          *slog.Logger
}

// writeTransitionRows performs Step 3+4 (InsertTicketTransition,
// UpdateTicketTransitionHygiene, UpdateTicketColumnSlug). Any failure
// here leaves a palace orphan because Step 1+2 already succeeded.
func writeTransitionRows(args writeTransitionRowsArgs) error {
	ctx := args.ctx
	parentCtx := args.parentCtx
	deps := args.deps
	q := args.q
	meta := args.meta
	hygieneStatus := args.hygieneStatus
	patternCategory := args.patternCategory
	logger := args.logger
	clean := hygieneStatus
	transitionID, err := q.InsertTicketTransition(ctx, store.InsertTicketTransitionParams{
		TicketID:                   meta.TicketID,
		FromColumn:                 &meta.FromColumn,
		ToColumn:                   meta.ToColumn,
		TriggeredByAgentInstanceID: meta.AgentInstanceID,
	})
	if err != nil {
		return failOrphan(parentCtx, deps, meta, logger, ExitFinalizePalaceWriteFailed, "palace_write",
			fmt.Errorf("InsertTicketTransition: %w", err))
	}
	if err := q.UpdateTicketTransitionHygiene(ctx, store.UpdateTicketTransitionHygieneParams{
		ID:            transitionID,
		HygieneStatus: &clean,
	}); err != nil {
		return failOrphan(parentCtx, deps, meta, logger, ExitFinalizePalaceWriteFailed, "palace_write",
			fmt.Errorf("UpdateTicketTransitionHygiene: %w", err))
	}
	// M4 / T015 / FR-115: record the matched pattern label when
	// a secret-shape match was found. Non-blocking on failure —
	// the hygiene_status row is the load-bearing audit; the
	// pattern category is a UI-side enrichment.
	if patternCategory != "" {
		if err := q.UpdateTicketTransitionPatternCategory(ctx, store.UpdateTicketTransitionPatternCategoryParams{
			ID:                             transitionID,
			SuspectedSecretPatternCategory: &patternCategory,
		}); err != nil {
			logger.Warn("failed to record suspected_secret_pattern_category",
				"transition_id", transitionID,
				"category", patternCategory,
				"err", err.Error())
		}
	}
	if err := q.UpdateTicketColumnSlug(ctx, store.UpdateTicketColumnSlugParams{
		ID:         meta.TicketID,
		ColumnSlug: meta.ToColumn,
	}); err != nil {
		return failOrphan(parentCtx, deps, meta, logger, ExitFinalizePalaceWriteFailed, "palace_write",
			fmt.Errorf("UpdateTicketColumnSlug: %w", err))
	}
	return nil
}

// writeTerminalAndMark performs Step 5+6 (UpdateInstanceTerminal*,
// MarkEventProcessed). Any failure here leaves a palace orphan.
func writeTerminalAndMark(
	ctx, parentCtx context.Context,
	deps FinalizeWriteDeps,
	q *store.Queries,
	meta FinalizeMeta,
	logger *slog.Logger,
) error {
	completedReason := ExitCompleted
	var wakeUpPtr *string
	if meta.WakeUpStatus != "" {
		wakeUpPtr = &meta.WakeUpStatus
	}
	if err := q.UpdateInstanceTerminalWithCostAndWakeup(ctx, store.UpdateInstanceTerminalWithCostAndWakeupParams{
		ID:           meta.AgentInstanceID,
		Status:       "succeeded",
		ExitReason:   &completedReason,
		TotalCostUsd: meta.Cost,
		WakeUpStatus: wakeUpPtr,
	}); err != nil {
		return failOrphan(parentCtx, deps, meta, logger, ExitFinalizePalaceWriteFailed, "palace_write",
			fmt.Errorf("UpdateInstanceTerminalWithCostAndWakeup: %w", err))
	}
	if err := q.MarkEventProcessed(ctx, meta.EventID); err != nil {
		return failOrphan(parentCtx, deps, meta, logger, ExitFinalizePalaceWriteFailed, "palace_write",
			fmt.Errorf("MarkEventProcessed: %w", err))
	}
	return nil
}

// classifyPalaceErr maps an error from AddDrawer/AddTriples to the
// (exit_reason, failure_class) pair: timeout vs. generic palace write.
func classifyPalaceErr(ctx context.Context, err error) (string, string) {
	if errors.Is(err, context.DeadlineExceeded) || isCtxDeadlineExceeded(ctx) {
		return ExitFinalizeWriteTimeout, "timeout"
	}
	return ExitFinalizePalaceWriteFailed, "palace_write"
}

// failWithoutOrphan + failOrphan are thin wrappers that build the
// writeTerminalOutcome and call writeFinalizeFailure. The OrphanWarn
// flag is the only meaningful difference and tracks "did the MemPalace
// drawer write succeed before this failure?".
func failWithoutOrphan(parentCtx context.Context, deps FinalizeWriteDeps, meta FinalizeMeta, logger *slog.Logger, reason, class string, underlying error) error {
	return writeFinalizeFailure(parentCtx, deps, meta, writeTerminalOutcome{
		ExitReason:    reason,
		HygieneStatus: "finalize_partial",
		FailureClass:  class,
		UnderlyingErr: underlying,
	}, logger)
}

func failOrphan(parentCtx context.Context, deps FinalizeWriteDeps, meta FinalizeMeta, logger *slog.Logger, reason, class string, underlying error) error {
	return writeFinalizeFailure(parentCtx, deps, meta, writeTerminalOutcome{
		ExitReason:    reason,
		HygieneStatus: "finalize_partial",
		FailureClass:  class,
		OrphanWarn:    true,
		UnderlyingErr: underlying,
	}, logger)
}

// writeFinalizeFailure emits the FR-277 failure log, writes the terminal
// agent_instances row outside the rolled-back tx, and returns a wrapped
// error whose string includes the exit_reason. Uses a background ctx
// derived from the parent so the terminal write isn't cancelled by the
// SIGTERM that triggered the rollback.
func writeFinalizeFailure(parentCtx context.Context, deps FinalizeWriteDeps, meta FinalizeMeta, outcome writeTerminalOutcome, logger *slog.Logger) error {
	// Log before writing so even a failing terminal write has an audit trail.
	if outcome.OrphanWarn {
		logger.Warn("palace_write_orphaned",
			"ticket_id", uuidString(meta.TicketID),
			"agent_instance_id", uuidString(meta.AgentInstanceID),
			"wing", meta.Wing,
			"failure_class", outcome.FailureClass,
			"err", fmt.Sprintf("%v", outcome.UnderlyingErr),
		)
	}
	level := slog.LevelError
	if outcome.FailureClass == "timeout" {
		level = slog.LevelWarn
	}
	logger.Log(parentCtx, level, "finalize_write_failed",
		"ticket_id", uuidString(meta.TicketID),
		"agent_instance_id", uuidString(meta.AgentInstanceID),
		"exit_reason", outcome.ExitReason,
		"failure_class", outcome.FailureClass,
		"err", fmt.Sprintf("%v", outcome.UnderlyingErr),
	)

	// Terminal row write using a fresh 10s context so it survives
	// supervisor SIGTERM long enough to commit.
	termCtx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), TerminalWriteGrace)
	defer cancel()
	reason := outcome.ExitReason
	wakeUpPtr := (*string)(nil)
	if meta.WakeUpStatus != "" {
		wakeUpPtr = &meta.WakeUpStatus
	}
	// Use a plain (non-transactional) call path — the tx that would have
	// been atomic is already rolled back; this is a single UPDATE.
	if err := deps.Queries.UpdateInstanceTerminalWithCostAndWakeup(termCtx, store.UpdateInstanceTerminalWithCostAndWakeupParams{
		ID:           meta.AgentInstanceID,
		Status:       "failed",
		ExitReason:   &reason,
		TotalCostUsd: meta.Cost,
		WakeUpStatus: wakeUpPtr,
	}); err != nil {
		logger.Error("terminal-failure write failed",
			"ticket_id", uuidString(meta.TicketID),
			"agent_instance_id", uuidString(meta.AgentInstanceID),
			"err", err)
	}
	return fmt.Errorf("spawn: finalize %s: %w", outcome.ExitReason, outcome.UnderlyingErr)
}

// serializeDiary composes the drawer body per FR-263 + clarify Q6:
//
//	<ticket_objective>
//
//	---
//	<yaml_frontmatter>
//	---
//
//	<rationale>
//
// The YAML frontmatter carries `ticket_id`, `outcome`, `artifacts`,
// `blockers`, `discoveries`, and `completed_at` (supervisor-generated).
// The leading objective prose is what makes mempalace_search by
// objective text land on this drawer.
func serializeDiary(objective string, ticketID pgtype.UUID, payload *finalize.FinalizePayload, completedAt time.Time) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(objective, "\n"))
	b.WriteString("\n\n---\n")
	fmt.Fprintf(&b, "ticket_id: %s\n", uuidString(ticketID))
	fmt.Fprintf(&b, "outcome: %s\n", escapeYAML(payload.Outcome))
	b.WriteString("artifacts:\n")
	for _, a := range payload.DiaryEntry.Artifacts {
		fmt.Fprintf(&b, diaryListItemFmt, escapeYAML(a))
	}
	b.WriteString("blockers:\n")
	for _, a := range payload.DiaryEntry.Blockers {
		fmt.Fprintf(&b, diaryListItemFmt, escapeYAML(a))
	}
	b.WriteString("discoveries:\n")
	for _, a := range payload.DiaryEntry.Discoveries {
		fmt.Fprintf(&b, diaryListItemFmt, escapeYAML(a))
	}
	fmt.Fprintf(&b, "completed_at: %s\n", completedAt.Format(time.RFC3339))
	b.WriteString("---\n\n")
	b.WriteString(payload.DiaryEntry.Rationale)
	return b.String()
}

// escapeYAML wraps a string in double quotes + JSON-escapes it when
// necessary. Keeps the serializer dependency-free: a value that happens
// to contain newlines, colons, or quotes always produces a valid YAML
// scalar without needing gopkg.in/yaml.v3.
func escapeYAML(s string) string {
	// Fast path: pure safe ASCII (no control chars, no YAML-significant
	// punctuation) → bare form.
	if yamlSafe(s) {
		return s
	}
	// Fallback: JSON's string encoder emits a valid YAML flow-scalar
	// (double-quoted with \uXXXX escapes) — YAML 1.2 §7.3.2.
	b, _ := json.Marshal(s)
	return string(b)
}

func yamlSafe(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f || c == '"' || c == '\'' || c == ':' || c == '#' ||
			c == '\n' || c == '\r' || c == '\t' || c == '&' || c == '*' || c == '!' ||
			c == '|' || c == '>' || c == '%' || c == '@' || c == '`' {
			return false
		}
	}
	return true
}

// toMempalaceTriples converts finalize.KGTriple (internal) to
// mempalace.Triple (write-method arg).
func toMempalaceTriples(in []finalize.KGTriple) []mempalace.Triple {
	out := make([]mempalace.Triple, 0, len(in))
	for _, t := range in {
		out = append(out, mempalace.Triple{
			Subject:   t.Subject,
			Predicate: t.Predicate,
			Object:    t.Object,
			ValidFrom: t.ValidFrom,
		})
	}
	return out
}

// isCtxDeadlineExceeded returns true when ctx.Err() is context.DeadlineExceeded.
// Defensive check for cases where the underlying error doesn't unwrap
// to DeadlineExceeded (e.g. wrapped in a network error).
func isCtxDeadlineExceeded(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// classifyCtxErr picks between the caller's default exit reason and
// ExitFinalizeWriteTimeout when the ctx has already exceeded its
// deadline. Used for early-stage failures (Begin tx) where a ctx
// timeout would manifest as a begin error.
func classifyCtxErr(ctx context.Context, defaultReason string) string {
	if isCtxDeadlineExceeded(ctx) {
		return ExitFinalizeWriteTimeout
	}
	return defaultReason
}

// scanAndRedactPayload runs vault.ScanAndRedact over the finalize payload
// fields named in FR-418 (rationale + kg_triple subject/predicate/object).
// Each field is replaced with its redacted form in-place. The union of all
// matched labels is returned so the caller can decide the hygiene_status.
func scanAndRedactPayload(p *finalize.FinalizePayload) []vault.Label {
	var matched []vault.Label

	redacted, labels := vault.ScanAndRedact(p.DiaryEntry.Rationale)
	p.DiaryEntry.Rationale = redacted
	matched = append(matched, labels...)

	for i := range p.KGTriples {
		if redacted, labels := vault.ScanAndRedact(p.KGTriples[i].Subject); len(labels) > 0 {
			p.KGTriples[i].Subject = redacted
			matched = append(matched, labels...)
		}
		if redacted, labels := vault.ScanAndRedact(p.KGTriples[i].Predicate); len(labels) > 0 {
			p.KGTriples[i].Predicate = redacted
			matched = append(matched, labels...)
		}
		if redacted, labels := vault.ScanAndRedact(p.KGTriples[i].Object); len(labels) > 0 {
			p.KGTriples[i].Object = redacted
			matched = append(matched, labels...)
		}
	}

	return matched
}
