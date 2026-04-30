package garrisonmutate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
)

// secretLeakPatterns mirrors M2.3's internal/finalize.scanAndRedactPayload
// pattern set. Used by edit_agent_config to reject proposed agent_md
// containing a verbatim secret value (M2.3 Rule 1 carryover into the
// chat mutation surface).
var secretLeakPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_\-]{20,}`),             // Anthropic / OpenAI key shape
	regexp.MustCompile(`xoxb-[A-Za-z0-9_\-]{20,}`),           // Slack bot token
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                   // AWS access key id
	regexp.MustCompile(`-----BEGIN [A-Z ]+PRIVATE KEY-----`), // PEM header
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),                // GitHub PAT
	regexp.MustCompile(`ghs_[A-Za-z0-9]{36}`),                // GitHub server-to-server
	regexp.MustCompile(`gho_[A-Za-z0-9]{36}`),                // GitHub OAuth
	regexp.MustCompile(`ghr_[A-Za-z0-9]{36}`),                // GitHub refresh
	regexp.MustCompile(`ghu_[A-Za-z0-9]{36}`),                // GitHub user-to-server
	regexp.MustCompile(`Bearer [A-Za-z0-9_\-\.]{30,}`),       // bearer-shape
}

// scanForSecrets returns the set of pattern indices that match the
// input. Empty result means clean.
func scanForSecrets(input string) []int {
	var hits []int
	for i, pat := range secretLeakPatterns {
		if pat.MatchString(input) {
			hits = append(hits, i)
		}
	}
	return hits
}

// PauseAgentArgs / ResumeAgentArgs share shape: just the role_slug + an
// optional reason captured in the audit row.
type PauseAgentArgs struct {
	AgentRoleSlug string `json:"agent_role_slug"`
	Reason        string `json:"reason,omitempty"`
}

type ResumeAgentArgs struct {
	AgentRoleSlug string `json:"agent_role_slug"`
}

func realPauseAgentHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	var args PauseAgentArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return validationFailure("pause_agent: parse args: " + err.Error()), nil
	}
	args.AgentRoleSlug = strings.TrimSpace(args.AgentRoleSlug)
	if args.AgentRoleSlug == "" {
		return validationFailure("pause_agent: agent_role_slug is required"), nil
	}
	return setAgentStatus(ctx, deps, "pause_agent", args.AgentRoleSlug, "paused", args)
}

func realResumeAgentHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	var args ResumeAgentArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return validationFailure("resume_agent: parse args: " + err.Error()), nil
	}
	args.AgentRoleSlug = strings.TrimSpace(args.AgentRoleSlug)
	if args.AgentRoleSlug == "" {
		return validationFailure("resume_agent: agent_role_slug is required"), nil
	}
	return setAgentStatus(ctx, deps, "resume_agent", args.AgentRoleSlug, "active", args)
}

// setAgentStatus is the shared body for pause/resume. Idempotent: a
// no-op call (status already at target) returns success and writes a
// no-op audit row.
func setAgentStatus(ctx context.Context, deps Deps, verb, roleSlug, target string, args any) (Result, error) {
	count, err := store.New(deps.Pool).CountAgentsByRoleSlug(ctx, roleSlug)
	if err != nil {
		return Result{}, fmt.Errorf("%s: count agents: %w", verb, err)
	}
	if count == 0 {
		return resourceNotFound("%s: agent role %q not found", verb, roleSlug),
			writeFailureAudit(ctx, deps, verb, args, ErrResourceNotFound, 1, roleSlug)
	}
	if count > 1 {
		return validationFailure(fmt.Sprintf("%s: agent role %q is ambiguous (%d matches across departments)", verb, roleSlug, count)),
			writeFailureAudit(ctx, deps, verb, args, ErrValidationFailed, 1, roleSlug)
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("%s: begin tx: %w", verb, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	agent, err := q.FindAgentByRoleSlug(ctx, roleSlug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return resourceNotFound("%s: agent role %q not found", verb, roleSlug),
				writeFailureAudit(ctx, deps, verb, args, ErrResourceNotFound, 1, roleSlug)
		}
		return Result{}, fmt.Errorf("%s: find agent: %w", verb, err)
	}
	if err := q.UpdateAgentStatus(ctx, store.UpdateAgentStatusParams{ID: agent.ID, Status: target}); err != nil {
		return Result{}, fmt.Errorf("%s: update status: %w", verb, err)
	}

	resourceID := roleSlug
	rt := "agent_role"
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 verb,
		Args:                 args,
		Outcome:              "success",
		ReversibilityClass:   1,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("%s: write audit: %w", verb, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("%s: commit: %w", verb, err)
	}

	channelSuffix := "agent.paused"
	if target == "active" {
		channelSuffix = "agent.resumed"
	}
	emitNotifyBestEffort(deps, channelSuffix, chatNotifyPayload{
		ChatSessionID:        uuidString(deps.ChatSessionID),
		ChatMessageID:        uuidString(deps.ChatMessageID),
		Verb:                 verb,
		AffectedResourceID:   resourceID,
		AffectedResourceType: rt,
		Extras:               map[string]string{"agent_role_slug": roleSlug},
	})
	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: "/agents/" + resourceID,
		Message:             fmt.Sprintf("%s: agent %s status=%s", verb, roleSlug, target),
	}, nil
}

// SpawnAgentArgs is the input shape for spawn_agent. ticket_id is
// required at M5.3 (the verb writes an agent_instances row directly,
// and that table requires ticket_id NOT NULL per the M2.x schema).
type SpawnAgentArgs struct {
	AgentRoleSlug string `json:"agent_role_slug"`
	TicketID      string `json:"ticket_id"`
}

// realSpawnAgentHandler implements garrison-mutate.spawn_agent. Tier 3
// reversibility — the agent runs, costs money, may write palace.
//
// M5.3 simplification: the verb emits work.chat.agent.spawned with
// the role+ticket; the existing M2.x spawn loop is the source of truth
// for actually starting the subprocess. Since the M2.x loop fires off
// pg_notify('work.ticket.created.<dept>...') triggers, the verb
// achieves "spawn an agent on this ticket" by inserting a
// ticket_transitions row that puts the ticket into the column the
// target role listens for, OR (simpler, M5.3 default) by emitting a
// chat-namespaced spawn notify the operator tooling can surface as a
// hint. M5.3 ships the simpler shape; M7+ may layer in a true
// dispatch path tied to agent_instances.
func realSpawnAgentHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	var args SpawnAgentArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return validationFailure("spawn_agent: parse args: " + err.Error()), nil
	}
	args.AgentRoleSlug = strings.TrimSpace(args.AgentRoleSlug)
	args.TicketID = strings.TrimSpace(args.TicketID)
	if args.AgentRoleSlug == "" {
		return validationFailure("spawn_agent: agent_role_slug is required"), nil
	}
	if args.TicketID == "" {
		return validationFailure("spawn_agent: ticket_id is required"), nil
	}

	count, err := store.New(deps.Pool).CountAgentsByRoleSlug(ctx, args.AgentRoleSlug)
	if err != nil {
		return Result{}, fmt.Errorf("spawn_agent: count agents: %w", err)
	}
	if count == 0 {
		return resourceNotFound("spawn_agent: agent role %q not found", args.AgentRoleSlug),
			writeFailureAudit(ctx, deps, "spawn_agent", args, ErrResourceNotFound, 3, args.AgentRoleSlug)
	}
	if count > 1 {
		return validationFailure(fmt.Sprintf("spawn_agent: role %q is ambiguous (%d matches)", args.AgentRoleSlug, count)),
			writeFailureAudit(ctx, deps, "spawn_agent", args, ErrValidationFailed, 3, args.AgentRoleSlug)
	}

	// Audit-only commit (M5.3 doesn't directly insert agent_instances —
	// that's M2.x's spawn-loop's job, gated by the per-department
	// concurrency cap). The audit row records the chat's intent; the
	// spawn-loop reads work.chat.agent.spawned and may dispatch.
	q := store.New(deps.Pool)
	resourceID := args.AgentRoleSlug
	rt := "agent_role"
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 "spawn_agent",
		Args:                 args,
		Outcome:              "success",
		ReversibilityClass:   3,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("spawn_agent: write audit: %w", err)
	}
	emitNotifyBestEffort(deps, "agent.spawned", chatNotifyPayload{
		ChatSessionID:        uuidString(deps.ChatSessionID),
		ChatMessageID:        uuidString(deps.ChatMessageID),
		Verb:                 "spawn_agent",
		AffectedResourceID:   resourceID,
		AffectedResourceType: rt,
		Extras: map[string]string{
			"agent_role_slug": args.AgentRoleSlug,
			"ticket_id":       args.TicketID,
		},
	})
	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: "/agents/" + resourceID,
		Message:             fmt.Sprintf("spawn_agent: requested %s on ticket %s", args.AgentRoleSlug, args.TicketID),
	}, nil
}

// EditAgentConfigArgs is the input shape for edit_agent_config. Pointer
// fields signal "set if present"; nil leaves the field unchanged.
type EditAgentConfigArgs struct {
	AgentRoleSlug string  `json:"agent_role_slug"`
	Model         *string `json:"model,omitempty"`
	AgentMD       *string `json:"agent_md,omitempty"`
	PalaceWing    *string `json:"palace_wing,omitempty"`
}

// realEditAgentConfigHandler implements garrison-mutate.edit_agent_config.
// Tier 2 reversibility: diff captured in audit. Pre-tx leak-scan rejects
// any proposed agent_md containing a verbatim secret value (M2.3 Rule 1
// carryover) with ErrLeakScanFailed; the audit row captures the rejected
// diff with values redacted to [REDACTED] (FR-421 + clarify Q3 atomic
// full reject decision).
func realEditAgentConfigHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	var args EditAgentConfigArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return validationFailure("edit_agent_config: parse args: " + err.Error()), nil
	}
	args.AgentRoleSlug = strings.TrimSpace(args.AgentRoleSlug)
	if args.AgentRoleSlug == "" {
		return validationFailure("edit_agent_config: agent_role_slug is required"), nil
	}
	if args.Model == nil && args.AgentMD == nil && args.PalaceWing == nil {
		return validationFailure("edit_agent_config: at least one of model / agent_md / palace_wing required"), nil
	}

	// Pre-tx leak scan — runs BEFORE the transaction opens (FR-421).
	// On detection: write a separate-tx audit row carrying the redacted
	// diff and return ErrLeakScanFailed; no agents row mutation lands.
	if args.AgentMD != nil && len(scanForSecrets(*args.AgentMD)) > 0 {
		redacted := "[REDACTED]"
		redactedArgs := EditAgentConfigArgs{
			AgentRoleSlug: args.AgentRoleSlug,
			Model:         args.Model,
			AgentMD:       &redacted,
			PalaceWing:    args.PalaceWing,
		}
		return leakScanFailure("edit_agent_config: proposed agent_md contains a secret-shaped value; verb rejected atomically"),
			writeFailureAudit(ctx, deps, "edit_agent_config", redactedArgs, ErrLeakScanFailed, 2, args.AgentRoleSlug)
	}

	count, err := store.New(deps.Pool).CountAgentsByRoleSlug(ctx, args.AgentRoleSlug)
	if err != nil {
		return Result{}, fmt.Errorf("edit_agent_config: count agents: %w", err)
	}
	if count == 0 {
		return resourceNotFound("edit_agent_config: agent role %q not found", args.AgentRoleSlug),
			writeFailureAudit(ctx, deps, "edit_agent_config", args, ErrResourceNotFound, 2, args.AgentRoleSlug)
	}
	if count > 1 {
		return validationFailure(fmt.Sprintf("edit_agent_config: role %q is ambiguous (%d matches)", args.AgentRoleSlug, count)),
			writeFailureAudit(ctx, deps, "edit_agent_config", args, ErrValidationFailed, 2, args.AgentRoleSlug)
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("edit_agent_config: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	agentRow, err := q.FindAgentByRoleSlug(ctx, args.AgentRoleSlug)
	if err != nil {
		return Result{}, fmt.Errorf("edit_agent_config: find agent: %w", err)
	}
	full, err := q.GetAgentByID(ctx, agentRow.ID)
	if err != nil {
		return Result{}, fmt.Errorf("edit_agent_config: load agent: %w", err)
	}

	finalModel := full.Model
	if args.Model != nil {
		finalModel = *args.Model
	}
	finalAgentMD := full.AgentMd
	if args.AgentMD != nil {
		finalAgentMD = *args.AgentMD
	}
	finalPalaceWing := full.PalaceWing
	if args.PalaceWing != nil {
		finalPalaceWing = args.PalaceWing
	}

	if err := q.UpdateAgentConfigFields(ctx, store.UpdateAgentConfigFieldsParams{
		ID:         agentRow.ID,
		Model:      finalModel,
		AgentMd:    finalAgentMD,
		PalaceWing: finalPalaceWing,
	}); err != nil {
		return Result{}, fmt.Errorf("edit_agent_config: update: %w", err)
	}

	diff := map[string]any{
		"before": map[string]any{
			"model":       full.Model,
			"agent_md":    full.AgentMd,
			"palace_wing": derefOrNil(full.PalaceWing),
		},
		"after": args,
	}

	resourceID := args.AgentRoleSlug
	rt := "agent_role"
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 "edit_agent_config",
		Args:                 diff,
		Outcome:              "success",
		ReversibilityClass:   2,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("edit_agent_config: write audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("edit_agent_config: commit: %w", err)
	}

	emitNotifyBestEffort(deps, "agent.config_edited", chatNotifyPayload{
		ChatSessionID:        uuidString(deps.ChatSessionID),
		ChatMessageID:        uuidString(deps.ChatMessageID),
		Verb:                 "edit_agent_config",
		AffectedResourceID:   resourceID,
		AffectedResourceType: rt,
		Extras:               map[string]string{"agent_role_slug": args.AgentRoleSlug},
	})
	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: "/agents/" + resourceID,
		Message:             "Edited config for agent " + args.AgentRoleSlug,
	}, nil
}

func init() {
	handlePauseAgent = realPauseAgentHandler
	handleResumeAgent = realResumeAgentHandler
	handleSpawnAgent = realSpawnAgentHandler
	handleEditAgentConfig = realEditAgentConfigHandler
}
