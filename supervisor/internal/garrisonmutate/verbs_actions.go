package garrisonmutate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/actionbroker"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// actionBrokerDispatchChannel is the pg_notify channel the dispatcher
// worker listens on (plan D18, FR-018). Defined here so the verb can
// emit the notify for auto/notify-tier rows post-commit without
// importing actionbroker (which would create a circular import).
// actionbroker/dispatcher.go defines the same string as Channel.
const actionBrokerDispatchChannel = "work.action.dispatch_requested"

// resourceTypePendingAction is the affected_resource_type CHECK value
// for the action-broker domain verbs (M11 migration, FR-001).
const resourceTypePendingAction = "pending_action"

// verbRequestExternalAction is the registry name for the 12th sealed
// verb. Centralised so audit rows, failure audits, and notify payloads
// cannot drift from the Verbs entry.
const verbRequestExternalAction = "request_external_action"

// RequestExternalActionArgs is the JSON-encoded input shape for
// request_external_action. Per FR-005/plan D7, there is NO tier field:
// any agent-supplied "tier" key in the JSON args is silently dropped
// on unmarshal, so the agent cannot self-classify the action tier.
// The tier is always the policy-table lookup on ActionType.
type RequestExternalActionArgs struct {
	// ActionType is the registered external action type (e.g.
	// "github_issue_comment"). Determines the tier via
	// actionbroker.Classify.
	ActionType string `json:"action_type"`

	// Target is the action target encoded as a JSON object. The schema
	// depends on the action type; for github_issue_comment it must be
	// {"owner":"…","repo":"…","issue_number":N}.
	Target json.RawMessage `json:"target"`

	// Payload is the rendered action payload — the comment body for
	// github_issue_comment. Must be non-empty.
	Payload string `json:"payload"`

	// TicketID is the serving ticket's UUID (optional). When set, the
	// pending_actions row records it so the Outbox can display it.
	TicketID *string `json:"ticket_id,omitempty"`
}

// realRequestExternalActionHandler implements
// garrison-mutate.request_external_action (M11 FR-001..FR-006).
// Tier 3 reversibility per chat-threat-model.md §5: "queues an
// attacker-influenceable effect on the outside world; an executed
// external action does not reverse."
//
// Agent-callers only (Q-D resolution): the verb rejects any call where
// deps.AgentInstanceID is not set. This is the inverse of M9's
// chat-only guard on create_scheduled_task.
//
// The verb does NOT perform any external action (FR-003, FR-008). Its
// sole effect is:
//  1. Classify the action type via actionbroker.Classify (FR-011).
//  2. In one transaction: InsertPendingAction + InsertPendingActionOutcome
//     ('requested') + WriteAudit (all in the same tx — Rule 3).
//  3. Post-commit (auto/notify tier only): pg_notify the dispatcher
//     channel with the pending_actions.id so the worker reacts
//     immediately (D18). approve/human_only tiers emit no notify here;
//     the approve Server Action emits it later.
//  4. Return a typed Result stating the action is queued and at which
//     tier — never implying the action was performed (FR-004).
func realRequestExternalActionHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	// Guard 1: agent-callers only (Q-D, D7). The dispatch layer already
	// hides this verb from chat-mode servers (agentVerbNames); this check
	// is defence-in-depth against a wiring regression.
	if !deps.AgentInstanceID.Valid {
		return validationFailure("request_external_action: callable only by agents"), nil
	}

	args, vRes := parseRequestExternalActionArgs(raw)
	if vRes != nil {
		return *vRes, nil
	}

	// Tier classification (FR-011, D4/D5/D6). The floor check runs first
	// so an agent-supplied "tier" field (absent from the struct) cannot
	// influence the outcome.
	tier, tierReason := actionbroker.Classify(args.ActionType)

	// Resolve the optional ticket_id to a pgtype.UUID.
	ticketID := pgtype.UUID{Valid: false}
	if args.TicketID != nil && strings.TrimSpace(*args.TicketID) != "" {
		if err := ticketID.Scan(*args.TicketID); err != nil {
			return validationFailure(fmt.Sprintf(
				"request_external_action: ticket_id %q is not a valid UUID: %v", *args.TicketID, err,
			)), nil
		}
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("request_external_action: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := store.New(tx)

	// Step 1: write the immutable pending-action row (FR-003, FR-013).
	row, err := q.InsertPendingAction(ctx, store.InsertPendingActionParams{
		ActionType:      args.ActionType,
		Target:          args.Target,
		RenderedPayload: args.Payload,
		AgentInstanceID: deps.AgentInstanceID,
		TicketID:        ticketID,
		Tier:            string(tier),
		TierReason:      tierReason,
	})
	if err != nil {
		return Result{}, fmt.Errorf("request_external_action: insert pending action: %w", err)
	}

	// Step 2: append the 'requested' outcome to the immutable history
	// (FR-024). The outcome anchors on the same agent_instance_id.
	if err := q.InsertPendingActionOutcome(ctx, store.InsertPendingActionOutcomeParams{
		PendingActionID:   row.ID,
		AgentInstanceID:   deps.AgentInstanceID,
		Outcome:           "requested",
		Detail:            "",
		StructuredOutcome: nil,
	}); err != nil {
		return Result{}, fmt.Errorf("request_external_action: insert outcome: %w", err)
	}

	// Step 3: write the chat_mutation_audit row in the same transaction
	// (Rule 3 — same tx as the data write; FR-001).
	resourceID := uuidString(row.ID)
	rt := resourceTypePendingAction
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		AgentInstanceID:      deps.AgentInstanceID,
		Verb:                 verbRequestExternalAction,
		Args:                 args,
		Outcome:              "success",
		ReversibilityClass:   3,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("request_external_action: write audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("request_external_action: commit: %w", err)
	}

	// Step 4 (post-commit): for auto/notify tiers only, emit the
	// dispatch-requested notify so the dispatcher reacts immediately
	// (D18/FR-018). For approve/human_only, the notify is emitted by
	// the approve Server Action (on operator click) or never (human_only).
	if tier == actionbroker.TierAuto || tier == actionbroker.TierNotify {
		emitDispatchNotifyBestEffort(ctx, deps, uuidString(row.ID))
	}

	tierMsg := dispatchTierMessage(tier)
	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: "/admin/outbox",
		Message: fmt.Sprintf(
			"Action queued at the %s tier, %s. Nothing has reached the outside world yet.",
			string(tier), tierMsg,
		),
	}, nil
}

// parseRequestExternalActionArgs unmarshals + validates the verb's
// input. Returns the args and a nil Result on success; returns a zero
// args and a non-nil validation Result on failure.
func parseRequestExternalActionArgs(raw json.RawMessage) (RequestExternalActionArgs, *Result) {
	var args RequestExternalActionArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		r := validationFailure("request_external_action: parse args: " + err.Error())
		return RequestExternalActionArgs{}, &r
	}
	args.ActionType = strings.TrimSpace(args.ActionType)
	args.Payload = strings.TrimSpace(args.Payload)

	if args.ActionType == "" {
		r := validationFailure("request_external_action: action_type is required")
		return RequestExternalActionArgs{}, &r
	}
	if len(args.Target) == 0 || string(args.Target) == "null" {
		r := validationFailure("request_external_action: target is required")
		return RequestExternalActionArgs{}, &r
	}
	if args.Payload == "" {
		r := validationFailure("request_external_action: payload is required")
		return RequestExternalActionArgs{}, &r
	}
	return args, nil
}

// emitDispatchNotifyBestEffort emits a pg_notify on the dispatcher
// channel post-commit. The payload is the pending_actions.id UUID string
// so the dispatcher's Handle(ctx, eventID pgtype.UUID) can look up the
// specific row immediately (plan D18). Uses a short background timeout
// so a SIGTERM mid-emit does not block graceful shutdown.
func emitDispatchNotifyBestEffort(_ context.Context, deps Deps, pendingActionID string) {
	notifyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := deps.Pool.Exec(notifyCtx,
		"SELECT pg_notify($1, $2)",
		actionBrokerDispatchChannel,
		pendingActionID,
	); err != nil && deps.Logger != nil {
		deps.Logger.Warn("request_external_action: post-commit dispatch notify failed",
			"channel", actionBrokerDispatchChannel,
			"pending_action_id", pendingActionID,
			"error", err,
		)
	}
}

// dispatchTierMessage returns the human-readable suffix for the Result
// message describing what happens next at each tier (FR-004: the result
// must never imply the action was performed).
func dispatchTierMessage(tier actionbroker.Tier) string {
	switch tier {
	case actionbroker.TierAuto:
		return "pending execution (no operator gate required)"
	case actionbroker.TierNotify:
		return "pending execution (operator will be notified post-hoc)"
	case actionbroker.TierApprove:
		return "pending operator approval"
	case actionbroker.TierHumanOnly:
		return "pending manual completion by the operator"
	default:
		return "pending operator review"
	}
}

// init wires the real handler into the Verbs registry, replacing the
// stubHandler placeholder verbs.go declares at package init.
func init() {
	handleRequestExternalAction = realRequestExternalActionHandler
}
