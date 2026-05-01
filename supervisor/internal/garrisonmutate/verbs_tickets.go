package garrisonmutate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// ticketURLPrefix is the dashboard route prefix for ticket detail
// pages. Centralised so the chat-mutation chip's deep-link target
// stays consistent across all three ticket verbs (create / edit /
// transition) and so the M3 dashboard's tickets-router base can
// shift here in one place if it ever does.
const ticketURLPrefix = "/tickets/"

// resourceTypeTicket is the affected_resource_type CHECK enum value
// for ticket-domain verbs. Mirrored from the migration's CHECK
// constraint; centralised so the three ticket verbs can pass a
// pointer to this constant rather than allocating a fresh string
// per call.
const resourceTypeTicket = "ticket"

// CreateTicketArgs is the JSON-encoded input shape for create_ticket.
// Field names match the chat-side tool schema; tags drive both
// json.Unmarshal and the eventual MCP tools/list inputSchema.
type CreateTicketArgs struct {
	Objective          string         `json:"objective"`
	DepartmentSlug     string         `json:"department_slug"`
	AcceptanceCriteria string         `json:"acceptance_criteria,omitempty"`
	ColumnSlug         string         `json:"column_slug,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

// realCreateTicketHandler implements garrison-mutate.create_ticket.
// Tier 3 reversibility per chat-threat-model.md §5: full pre-state
// args captured in args_jsonb; ticket can be deleted but downstream
// effects survive.
func realCreateTicketHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	var args CreateTicketArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return validationFailure("create_ticket: parse args: " + err.Error()), nil
	}
	args.Objective = strings.TrimSpace(args.Objective)
	args.DepartmentSlug = strings.TrimSpace(args.DepartmentSlug)
	if args.Objective == "" {
		return validationFailure("create_ticket: objective is required"), nil
	}
	if len(args.Objective) > 10000 {
		return validationFailure("create_ticket: objective exceeds 10000 chars"), nil
	}
	if args.DepartmentSlug == "" {
		return validationFailure("create_ticket: department_slug is required"), nil
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("create_ticket: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	deptID, err := q.SelectDepartmentIDBySlug(ctx, args.DepartmentSlug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return resourceNotFound("create_ticket: department %q not found", args.DepartmentSlug),
				writeFailureAudit(ctx, deps, "create_ticket", args, ErrResourceNotFound, 3, "")
		}
		return Result{}, fmt.Errorf("create_ticket: lookup department: %w", err)
	}

	metadataJSON := []byte("{}")
	if len(args.Metadata) > 0 {
		metadataJSON, err = json.Marshal(args.Metadata)
		if err != nil {
			return validationFailure("create_ticket: invalid metadata: " + err.Error()), nil
		}
	}

	// CEO-originated tickets default to in_dev — they're CEO-authored
	// and ready for immediate engineering work. The todo column is for
	// operator-staged tickets that need triage; landing CEO tickets at
	// todo silently routes them through the M2.2 back-compat dispatch
	// to the M1 hello-txt acceptance path (instead of the M2.2.1
	// finalize tool path) and the engineer exits acceptance_failed.
	// See A→Z smoke discovery 2026-05-01: CEO chat tickets at todo
	// never finalize because the engineer's M2.2.1 finalize handler
	// is wired for in_dev only. Operator can still override the column
	// explicitly via the column_slug arg.
	columnSlug := strings.TrimSpace(args.ColumnSlug)
	if columnSlug == "" {
		columnSlug = "in_dev"
	}
	ticket, err := q.InsertChatTicket(ctx, store.InsertChatTicketParams{
		DepartmentID:            deptID,
		Objective:               args.Objective,
		AcceptanceCriteria:      stringPtrOrNil(args.AcceptanceCriteria),
		ColumnSlug:              columnSlug,
		Metadata:                metadataJSON,
		CreatedViaChatSessionID: deps.ChatSessionID,
	})
	if err != nil {
		return Result{}, fmt.Errorf("create_ticket: insert ticket: %w", err)
	}

	resourceID := uuidString(ticket.ID)
	rt := "ticket"
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 "create_ticket",
		Args:                 args,
		Outcome:              "success",
		ReversibilityClass:   3,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("create_ticket: write audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("create_ticket: commit: %w", err)
	}

	// Post-commit notify (Rule 6 backstop: IDs only, no chat content).
	emitNotifyBestEffort(deps, "ticket.created", chatNotifyPayload{
		ChatSessionID:        uuidString(deps.ChatSessionID),
		ChatMessageID:        uuidString(deps.ChatMessageID),
		Verb:                 "create_ticket",
		AffectedResourceID:   resourceID,
		AffectedResourceType: rt,
	})

	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: ticketURLPrefix + resourceID,
		Message: fmt.Sprintf(
			"Created ticket %s in %s at column %q. The supervisor's spawn loop dispatches the appropriate agent automatically — do NOT call transition_ticket on this ticket as a follow-up unless you intend to override the agent's work.",
			resourceID, args.DepartmentSlug, columnSlug,
		),
	}, nil
}

// EditTicketArgs is the JSON input shape for edit_ticket. Pointer-typed
// fields signal "set if present, leave unchanged if nil"; this lets the
// verb implement partial-update semantics.
type EditTicketArgs struct {
	TicketID           string         `json:"ticket_id"`
	Objective          *string        `json:"objective,omitempty"`
	AcceptanceCriteria *string        `json:"acceptance_criteria,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

// realEditTicketHandler implements garrison-mutate.edit_ticket. Tier 2
// reversibility: diff captured in audit (before/after for each changed
// field).
func realEditTicketHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	var args EditTicketArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return validationFailure("edit_ticket: parse args: " + err.Error()), nil
	}
	if args.TicketID == "" {
		return validationFailure("edit_ticket: ticket_id is required"), nil
	}
	if args.Objective == nil && args.AcceptanceCriteria == nil && len(args.Metadata) == 0 {
		return validationFailure("edit_ticket: at least one of objective / acceptance_criteria / metadata required"), nil
	}
	var ticketID pgtype.UUID
	if err := ticketID.Scan(args.TicketID); err != nil {
		return validationFailure("edit_ticket: invalid ticket_id: " + err.Error()), nil
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("edit_ticket: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	if _, lockErr := q.LockTicketForUpdate(ctx, ticketID); lockErr != nil {
		if r, auditErr, ok := classifyTicketLockErr(ctx, deps, "edit_ticket", args, 2, args.TicketID, lockErr); !ok {
			return r, auditErr
		}
	}

	// Fetch the before-state for the audit diff.
	before, err := q.GetTicketByID(ctx, ticketID)
	if err != nil {
		return Result{}, fmt.Errorf("edit_ticket: get before: %w", err)
	}

	finalObjective, finalAcceptance, finalMetadata, vErr := mergeTicketFields(before, args)
	if vErr != nil {
		return *vErr, nil
	}
	if err := q.UpdateTicketEditableFields(ctx, store.UpdateTicketEditableFieldsParams{
		ID:                 ticketID,
		Objective:          finalObjective,
		AcceptanceCriteria: finalAcceptance,
		Metadata:           finalMetadata,
	}); err != nil {
		return Result{}, fmt.Errorf("edit_ticket: update: %w", err)
	}

	diff := map[string]any{
		"before": map[string]any{
			"objective":           before.Objective,
			"acceptance_criteria": derefOrNil(before.AcceptanceCriteria),
			"metadata":            json.RawMessage(before.Metadata),
		},
		"after": args,
	}

	rt := "ticket"
	resourceID := args.TicketID
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 "edit_ticket",
		Args:                 diff,
		Outcome:              "success",
		ReversibilityClass:   2,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("edit_ticket: write audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("edit_ticket: commit: %w", err)
	}

	emitNotifyBestEffort(deps, "ticket.edited", chatNotifyPayload{
		ChatSessionID:        uuidString(deps.ChatSessionID),
		ChatMessageID:        uuidString(deps.ChatMessageID),
		Verb:                 "edit_ticket",
		AffectedResourceID:   resourceID,
		AffectedResourceType: rt,
	})

	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: ticketURLPrefix + resourceID,
		Message:             "Edited ticket " + resourceID,
	}, nil
}

// TransitionTicketArgs is the JSON input shape for transition_ticket.
//
// ExpectedFromColumn is an optional optimistic-concurrency check
// (added live 2026-05-01 after the M5.4 A→Z smoke surfaced a
// CEO/engineer race). When supplied, the verb verifies the ticket's
// current column matches before transitioning; on mismatch it returns
// outcome=ticket_state_changed so the caller can reconcile against
// fresh state instead of clobbering another actor's work. Empty =
// skip the check (M5.3 back-compat for callers that haven't been
// updated to send the field).
type TransitionTicketArgs struct {
	TicketID           string `json:"ticket_id"`
	ToColumn           string `json:"to_column"`
	ExpectedFromColumn string `json:"expected_from_column,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

// realTransitionTicketHandler implements garrison-mutate.transition_ticket.
// Tier 1 reversibility: a paired call moves the ticket back to the
// previous column. Hooks into the existing M2.x ticket_transitions
// event-bus AND emits the chat-namespaced work.chat.ticket.transitioned
// channel.
func realTransitionTicketHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	args, ticketID, vRes := parseTransitionArgs(raw)
	if vRes != nil {
		return *vRes, nil
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("transition_ticket: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	if _, lockErr := q.LockTicketForUpdate(ctx, ticketID); lockErr != nil {
		if r, auditErr, ok := classifyTicketLockErr(ctx, deps, "transition_ticket", args, 1, args.TicketID, lockErr); !ok {
			return r, auditErr
		}
	}

	cur, err := q.GetTicketColumnAndDept(ctx, ticketID)
	if err != nil {
		return Result{}, fmt.Errorf("transition_ticket: get current: %w", err)
	}
	// Optimistic-concurrency check: when the caller supplied an
	// expected_from_column, verify it matches what's actually on the
	// ticket right now. Discovered live during the M5.4-ship A→Z smoke
	// (2026-05-01): the CEO LLM raced with an agent — the engineer
	// finalized in_dev → qa_review within seconds of create_ticket,
	// the CEO (still holding stale state) issued
	// transition_ticket(to_column=in_dev) and clobbered the engineer's
	// work because there was no concurrency check at all.
	//
	// Same shape as the chat-threat-model.md "ticket_state_changed"
	// outcome: when the caller-observed state diverges from current
	// state, fail with a typed error so the CEO can reconcile (or
	// just acknowledge the agent already did the work).
	//
	// Backwards transitions (e.g. QA bouncing back to in_dev) remain
	// fully allowed — they're a legitimate workflow primitive. This
	// check is purely about staleness, not direction.
	if args.ExpectedFromColumn != "" && args.ExpectedFromColumn != cur.ColumnSlug {
		writeErr := writeFailureAudit(ctx, deps, "transition_ticket", args, ErrTicketStateChanged, 1, args.TicketID)
		return Result{
			Success:   false,
			ErrorKind: string(ErrTicketStateChanged),
			Message: fmt.Sprintf(
				"Ticket %s is now at column %q, not %q as you expected — another actor (likely the agent assigned to it) moved it. Check the ticket's current state before deciding what to do next.",
				args.TicketID, cur.ColumnSlug, args.ExpectedFromColumn,
			),
		}, writeErr
	}
	if cur.ColumnSlug == args.ToColumn {
		return commitNoopTransition(ctx, tx, q, deps, args)
	}
	return commitTransition(ctx, tx, q, deps, args, ticketID, cur.ColumnSlug)
}

// parseTransitionArgs unmarshals + validates transition_ticket's input
// shape. Returns a non-nil *Result on validation failure; the caller
// returns it verbatim with nil error per the verb-handler contract.
func parseTransitionArgs(raw json.RawMessage) (TransitionTicketArgs, pgtype.UUID, *Result) {
	var args TransitionTicketArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		r := validationFailure("transition_ticket: parse args: " + err.Error())
		return args, pgtype.UUID{}, &r
	}
	args.ToColumn = strings.TrimSpace(args.ToColumn)
	if args.TicketID == "" {
		r := validationFailure("transition_ticket: ticket_id is required")
		return args, pgtype.UUID{}, &r
	}
	if args.ToColumn == "" {
		r := validationFailure("transition_ticket: to_column is required")
		return args, pgtype.UUID{}, &r
	}
	var ticketID pgtype.UUID
	if err := ticketID.Scan(args.TicketID); err != nil {
		r := validationFailure("transition_ticket: invalid ticket_id: " + err.Error())
		return args, pgtype.UUID{}, &r
	}
	return args, ticketID, nil
}

// commitNoopTransition handles the same-column idempotent branch:
// audit-only commit, no notify, success result describing the no-op.
func commitNoopTransition(ctx context.Context, tx pgx.Tx, q *store.Queries, deps Deps, args TransitionTicketArgs) (Result, error) {
	rt := resourceTypeTicket
	resourceID := args.TicketID
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 "transition_ticket",
		Args:                 args,
		Outcome:              "success",
		ReversibilityClass:   1,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("transition_ticket: write audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("transition_ticket: commit: %w", err)
	}
	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: ticketURLPrefix + resourceID,
		Message:             "Ticket " + resourceID + " already in column " + args.ToColumn,
	}, nil
}

// commitTransition handles the non-idempotent path: insert the
// ticket_transitions row, update tickets.column_slug, write the audit
// row, commit, then post-commit notify.
func commitTransition(ctx context.Context, tx pgx.Tx, q *store.Queries, deps Deps, args TransitionTicketArgs, ticketID pgtype.UUID, fromColumn string) (Result, error) {
	if _, err := q.InsertTicketTransition(ctx, store.InsertTicketTransitionParams{
		TicketID:                   ticketID,
		FromColumn:                 &fromColumn,
		ToColumn:                   args.ToColumn,
		TriggeredByAgentInstanceID: pgtype.UUID{},
	}); err != nil {
		return Result{}, fmt.Errorf("transition_ticket: insert transition: %w", err)
	}
	if err := q.UpdateTicketColumnSlug(ctx, store.UpdateTicketColumnSlugParams{
		ID:         ticketID,
		ColumnSlug: args.ToColumn,
	}); err != nil {
		return Result{}, fmt.Errorf("transition_ticket: update column: %w", err)
	}
	rt := resourceTypeTicket
	resourceID := args.TicketID
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 "transition_ticket",
		Args:                 args,
		Outcome:              "success",
		ReversibilityClass:   1,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("transition_ticket: write audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("transition_ticket: commit: %w", err)
	}
	emitNotifyBestEffort(deps, "ticket.transitioned", chatNotifyPayload{
		ChatSessionID:        uuidString(deps.ChatSessionID),
		ChatMessageID:        uuidString(deps.ChatMessageID),
		Verb:                 "transition_ticket",
		AffectedResourceID:   resourceID,
		AffectedResourceType: rt,
		Extras: map[string]string{
			"from_column": fromColumn,
			"to_column":   args.ToColumn,
		},
	})
	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: ticketURLPrefix + resourceID,
		Message:             fmt.Sprintf("Transitioned ticket %s: %s → %s", resourceID, fromColumn, args.ToColumn),
	}, nil
}

// helpers shared by ticket verbs

func validationFailure(msg string) Result {
	return Result{Success: false, ErrorKind: string(ErrValidationFailed), Message: msg}
}

func resourceNotFound(format string, args ...any) Result {
	return Result{Success: false, ErrorKind: string(ErrResourceNotFound), Message: fmt.Sprintf(format, args...)}
}

func ticketStateChanged(msg string) Result {
	return Result{Success: false, ErrorKind: string(ErrTicketStateChanged), Message: msg}
}

func concurrencyCapFull(msg string) Result {
	return Result{Success: false, ErrorKind: string(ErrConcurrencyCapFull), Message: msg}
}

func leakScanFailure(msg string) Result {
	return Result{Success: false, ErrorKind: string(ErrLeakScanFailed), Message: msg}
}

// isLockNotAvailable detects PostgreSQL's lock_not_available SQLSTATE
// (55P03), returned by SELECT ... FOR UPDATE NOWAIT when another tx
// holds the lock. Per chat-threat-model.md Rule 4 this is the
// concurrent-mutation conflict signal.
func isLockNotAvailable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "55P03"
	}
	return false
}

// classifyTicketLockErr maps the LockTicketForUpdate error onto the
// shared (concurrency-conflict / not-found / generic) shape that
// edit_ticket and transition_ticket both need. Returns:
//   - locked=true: the lock was acquired; the caller proceeds.
//   - locked=false + result/audit: the lock failed for a known reason
//     (ErrTicketStateChanged or ErrResourceNotFound); caller returns
//     the result + audit-write error verbatim.
//   - locked=false + result.Success=false + result.ErrorKind=="" +
//     unhandled error: an unexpected pgx error; caller wraps it.
func classifyTicketLockErr(ctx context.Context, deps Deps, verb string, args any, class int16, ticketIDText string, lockErr error) (Result, error, bool) {
	if lockErr == nil {
		return Result{}, nil, true
	}
	if isLockNotAvailable(lockErr) {
		return ticketStateChanged(verb + ": another mutation got there first"),
			writeFailureAudit(ctx, deps, verb, args, ErrTicketStateChanged, class, ticketIDText), false
	}
	if errors.Is(lockErr, pgx.ErrNoRows) {
		return resourceNotFound("%s: ticket %q not found", verb, ticketIDText),
			writeFailureAudit(ctx, deps, verb, args, ErrResourceNotFound, class, ticketIDText), false
	}
	return Result{}, fmt.Errorf("%s: lock: %w", verb, lockErr), false
}

// mergeTicketFields applies edit_ticket's Go-side COALESCE merge:
// nil-pointer args mean "leave unchanged"; populated args overwrite.
// Returns the resolved final values plus an optional validation
// failure when args.Metadata fails to JSON-encode.
func mergeTicketFields(before store.Ticket, args EditTicketArgs) (string, *string, []byte, *Result) {
	finalObjective := before.Objective
	if args.Objective != nil {
		finalObjective = *args.Objective
	}
	finalAcceptance := before.AcceptanceCriteria
	if args.AcceptanceCriteria != nil {
		finalAcceptance = args.AcceptanceCriteria
	}
	finalMetadata := []byte(before.Metadata)
	if len(args.Metadata) > 0 {
		raw, err := json.Marshal(args.Metadata)
		if err != nil {
			r := validationFailure("edit_ticket: invalid metadata: " + err.Error())
			return "", nil, nil, &r
		}
		finalMetadata = raw
	}
	return finalObjective, finalAcceptance, finalMetadata, nil
}

// stringPtrOrNil returns a *string pointing at s if non-empty, nil
// otherwise. Used to satisfy sqlc-generated nullable text columns.
func stringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// derefOrNil returns *p as a value, or nil for nil pointers. Used
// in audit diffs.
func derefOrNil(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// emitNotifyBestEffort is the post-commit notify emitter. Runs
// inline in the tool-call path under a 5-second background ctx; if
// pg_notify drops a payload, the activity feed picks it up via the
// existing event_outbox poll fallback (M1 retro precedent).
func emitNotifyBestEffort(deps Deps, channelSuffix string, payload chatNotifyPayload) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := EmitChatMutationNotify(ctx, deps.Pool, channelSuffix, payload); err != nil && deps.Logger != nil {
		deps.Logger.Warn("garrison-mutate: post-commit notify failed",
			"channel_suffix", channelSuffix,
			"error", err,
		)
	}
}

// writeFailureAudit opens a separate audit-only transaction to record a
// failed verb call. Best-effort: the verb's main path returns the
// failure result regardless of whether this audit row commits. Per
// chat-threat-model.md Rule 3 + plan §4.5.
func writeFailureAudit(ctx context.Context, deps Deps, verb string, args any, kind MutateErrorKind, class int16, resourceID string) error {
	if deps.Pool == nil {
		return nil
	}
	q := store.New(deps.Pool)
	rt := ""
	switch v := FindVerb(verb); {
	case v != nil:
		rt = v.AffectedResourceType
	}
	_, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 verb,
		Args:                 args,
		Outcome:              kind.String(),
		ReversibilityClass:   class,
		AffectedResourceID:   stringPtrOrNil(resourceID),
		AffectedResourceType: stringPtrOrNil(rt),
	})
	if err != nil && deps.Logger != nil {
		deps.Logger.Warn("garrison-mutate: failure audit insert failed",
			"verb", verb, "error", err)
	}
	return err
}

// init wires the per-verb handlers into the registry, replacing the
// stubHandler placeholders verbs.go declares at package init.
func init() {
	handleCreateTicket = realCreateTicketHandler
	handleEditTicket = realEditTicketHandler
	handleTransitionTicket = realTransitionTicketHandler
}
