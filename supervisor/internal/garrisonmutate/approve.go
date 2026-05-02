package garrisonmutate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Approve-side error sentinels. Server Actions surface these as 4xx
// vs 5xx so the operator's dashboard can render the right message.
var (
	ErrProposalNotFound       = errors.New("garrisonmutate: proposal not found")
	ErrProposalNotPending     = errors.New("garrisonmutate: proposal is not in pending state")
	ErrProposalTypeMismatch   = errors.New("garrisonmutate: proposal type does not match approve verb")
	ErrTargetAgentMissing     = errors.New("garrisonmutate: target_agent_id is missing on the proposal")
	ErrInvalidSnapshotPayload = errors.New("garrisonmutate: proposal_snapshot_jsonb is malformed")
)

// ApproveHireResult carries the resource IDs the Server Action surfaces
// back to the dashboard so the redirect / activity feed link work.
// AuditID identifies the chat_mutation_audit row written for this
// approval; SupersededCount is always 0 for new-agent hires (only
// skill_change / version_bump approvals can supersede siblings).
type ApproveHireResult struct {
	AgentID         pgtype.UUID
	AuditID         pgtype.UUID
	SupersededCount int64
}

// ApproveSkillResult is the shared return shape for ApproveSkillChange
// and ApproveVersionBump. AgentID names the existing agent the skills
// landed against; SupersededCount carries the FR-110a sibling-rejection
// count so the audit row can include it in its args_jsonb.
type ApproveSkillResult struct {
	AgentID         pgtype.UUID
	AuditID         pgtype.UUID
	SupersededCount int64
}

// ApproveHire walks the new-agent approval flow inside the supplied
// transaction. Reads the proposal, INSERTs an `agents` row, marks the
// proposal approved + transitions it to install_in_progress, and writes
// the chat_mutation_audit row with a snapshot of the proposal. The
// caller (the dashboard's Server Action) then commits the transaction
// and queues `skillinstall.Actuator.Install` post-commit so the
// operator's UI gets a snappy response while the actuator does its
// per-step journaling.
func ApproveHire(ctx context.Context, tx pgx.Tx, proposalID, operatorID pgtype.UUID) (ApproveHireResult, error) {
	q := store.New(tx)

	prop, err := readPendingProposal(ctx, q, proposalID, "new_agent")
	if err != nil {
		return ApproveHireResult{}, err
	}
	dept, err := q.GetDepartmentBySlug(ctx, prop.DepartmentSlug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ApproveHireResult{}, fmt.Errorf("approve_hire: department %q not found", prop.DepartmentSlug)
		}
		return ApproveHireResult{}, fmt.Errorf("approve_hire: get department: %w", err)
	}

	skillsJSON, _ := skillsJSONFromProposal(prop)
	agentMD := composeApproveHireAgentMD(prop)
	listensFor := defaultListensForJSON(dept.Slug)

	agentID, err := q.InsertAgentForHire(ctx, store.InsertAgentForHireParams{
		DepartmentID:    dept.ID,
		RoleSlug:        prop.RoleTitle,
		AgentMd:         agentMD,
		Model:           defaultAgentModel,
		SkillsJsonb:     skillsJSON,
		ListensForJsonb: listensFor,
		PalaceWing:      stringPtrOrNil("wing_" + prop.RoleTitle),
		McpServersJsonb: []byte("[]"),
	})
	if err != nil {
		return ApproveHireResult{}, fmt.Errorf("approve_hire: insert agent: %w", err)
	}

	if err := q.ApproveProposal(ctx, store.ApproveProposalParams{ID: proposalID, ApprovedBy: operatorID}); err != nil {
		return ApproveHireResult{}, fmt.Errorf("approve_hire: mark approved: %w", err)
	}
	if err := q.TransitionToInstallInProgress(ctx, proposalID); err != nil {
		return ApproveHireResult{}, fmt.Errorf("approve_hire: transition to install_in_progress: %w", err)
	}

	auditID, err := writeApproveAudit(ctx, q, "approve_hire", prop.ProposedByChatSessionID,
		approveAuditArgs(prop, agentID, 0))
	if err != nil {
		return ApproveHireResult{}, err
	}
	return ApproveHireResult{AgentID: agentID, AuditID: auditID}, nil
}

// ApproveSkillChange handles `proposal_type='skill_change'` approvals.
// Marks the proposal approved + transitions to install_in_progress;
// FR-110a-supersedes sibling pending skill_change proposals for the
// same target_agent_id. The actual skills column update happens after
// the install actuator finishes — this helper writes the audit row +
// supersedes siblings; T015's Server Action queues the actuator.
func ApproveSkillChange(ctx context.Context, tx pgx.Tx, proposalID, operatorID pgtype.UUID) (ApproveSkillResult, error) {
	return approveSkillFlow(ctx, tx, proposalID, operatorID, "skill_change", "approve_skill_change")
}

// ApproveVersionBump handles `proposal_type='version_bump'` approvals.
// Identical FR-110a supersession shape as ApproveSkillChange; behaviour
// only differs in the verb name written to the audit row.
func ApproveVersionBump(ctx context.Context, tx pgx.Tx, proposalID, operatorID pgtype.UUID) (ApproveSkillResult, error) {
	return approveSkillFlow(ctx, tx, proposalID, operatorID, "version_bump", "approve_version_bump")
}

func approveSkillFlow(
	ctx context.Context, tx pgx.Tx, proposalID, operatorID pgtype.UUID,
	expectedType, verb string,
) (ApproveSkillResult, error) {
	q := store.New(tx)
	prop, err := readPendingProposal(ctx, q, proposalID, expectedType)
	if err != nil {
		return ApproveSkillResult{}, err
	}
	if !prop.TargetAgentID.Valid {
		return ApproveSkillResult{}, ErrTargetAgentMissing
	}
	if err := q.ApproveProposal(ctx, store.ApproveProposalParams{ID: proposalID, ApprovedBy: operatorID}); err != nil {
		return ApproveSkillResult{}, fmt.Errorf("%s: mark approved: %w", verb, err)
	}
	if err := q.TransitionToInstallInProgress(ctx, proposalID); err != nil {
		return ApproveSkillResult{}, fmt.Errorf("%s: transition: %w", verb, err)
	}

	superseded, err := q.SupersedeSiblingProposals(ctx, store.SupersedeSiblingProposalsParams{
		Column1:       uuidString(proposalID),
		TargetAgentID: prop.TargetAgentID,
		ProposalType:  expectedType,
		ID:            proposalID,
	})
	if err != nil {
		return ApproveSkillResult{}, fmt.Errorf("%s: supersede siblings: %w", verb, err)
	}

	auditID, err := writeApproveAudit(ctx, q, verb, prop.ProposedByChatSessionID,
		approveAuditArgs(prop, prop.TargetAgentID, superseded))
	if err != nil {
		return ApproveSkillResult{}, err
	}
	return ApproveSkillResult{
		AgentID:         prop.TargetAgentID,
		AuditID:         auditID,
		SupersededCount: superseded,
	}, nil
}

// RejectProposal preserves the row + records the operator's reason.
// Same shape as M5.3's audit-row pattern; reason is required (the
// dashboard Server Action enforces non-empty before calling).
func RejectProposal(ctx context.Context, tx pgx.Tx, proposalID, operatorID pgtype.UUID, reason string) (pgtype.UUID, error) {
	q := store.New(tx)
	prop, err := q.GetHiringProposalByID(ctx, proposalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, ErrProposalNotFound
		}
		return pgtype.UUID{}, fmt.Errorf("reject_proposal: get: %w", err)
	}
	if prop.Status != "pending" {
		return pgtype.UUID{}, ErrProposalNotPending
	}
	if err := q.RejectProposal(ctx, store.RejectProposalParams{
		ID:             proposalID,
		RejectedReason: stringPtrOrNil(reason),
	}); err != nil {
		return pgtype.UUID{}, fmt.Errorf("reject_proposal: update: %w", err)
	}
	verb := rejectVerbForProposalType(prop.ProposalType)
	body, _ := json.Marshal(map[string]any{
		"proposal_id":     uuidString(proposalID),
		"reason":          reason,
		"operator_id":     uuidString(operatorID),
		"proposal_type":   prop.ProposalType,
		"target_agent_id": uuidString(prop.TargetAgentID),
	})
	return writeApproveAuditRaw(ctx, q, verb, prop.ProposedByChatSessionID, body)
}

// UpdateAgentMD writes the operator-typed agent.md content + a
// chat_mutation_audit snapshot capturing both the prior + new content.
// F3 lean (decision #1): chat verbs cannot reach this — only Server
// Actions. Verb name `update_agent_md` is on the audit CHECK list but
// NOT in `Verbs` (TestUpdateAgentMDIsNotChatVerb pins this rule).
func UpdateAgentMD(ctx context.Context, tx pgx.Tx, agentID pgtype.UUID, newMD string, operatorID pgtype.UUID) (pgtype.UUID, error) {
	q := store.New(tx)
	prior, err := q.GetAgentByID(ctx, agentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, fmt.Errorf("update_agent_md: agent %q not found", uuidString(agentID))
		}
		return pgtype.UUID{}, fmt.Errorf("update_agent_md: get agent: %w", err)
	}
	if err := q.UpdateAgentMD(ctx, store.UpdateAgentMDParams{ID: agentID, AgentMd: newMD}); err != nil {
		return pgtype.UUID{}, fmt.Errorf("update_agent_md: update: %w", err)
	}
	body, _ := json.Marshal(map[string]any{
		"agent_id":       uuidString(agentID),
		"operator_id":    uuidString(operatorID),
		"prior_agent_md": prior.AgentMd,
		"new_agent_md":   newMD,
	})
	return writeApproveAuditRaw(ctx, q, "update_agent_md", pgtype.UUID{Valid: false}, body)
}

// readPendingProposal returns a proposal that's both present and in
// the expected (pending, type) state, else a typed sentinel error.
func readPendingProposal(ctx context.Context, q *store.Queries, proposalID pgtype.UUID, expectedType string) (store.HiringProposal, error) {
	prop, err := q.GetHiringProposalByID(ctx, proposalID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.HiringProposal{}, ErrProposalNotFound
		}
		return store.HiringProposal{}, fmt.Errorf("read proposal: %w", err)
	}
	if prop.Status != "pending" {
		return store.HiringProposal{}, ErrProposalNotPending
	}
	if prop.ProposalType != expectedType {
		return store.HiringProposal{}, ErrProposalTypeMismatch
	}
	return prop, nil
}

func writeApproveAudit(ctx context.Context, q *store.Queries, verb string, sessionID pgtype.UUID, body []byte) (pgtype.UUID, error) {
	return writeApproveAuditRaw(ctx, q, verb, sessionID, body)
}

func writeApproveAuditRaw(ctx context.Context, q *store.Queries, verb string, sessionID pgtype.UUID, body []byte) (pgtype.UUID, error) {
	row, err := q.InsertChatMutationAudit(ctx, store.InsertChatMutationAuditParams{
		ChatSessionID:        sessionID,
		ChatMessageID:        pgtype.UUID{Valid: false}, // Server-Action-driven; no chat message
		Verb:                 verb,
		ArgsJsonb:            body,
		Outcome:              "success",
		ReversibilityClass:   3,
		AffectedResourceID:   nil,
		AffectedResourceType: stringPtrOrNil("hiring_proposal"),
	})
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("%s: write audit: %w", verb, err)
	}
	return row.ID, nil
}

func approveAuditArgs(prop store.HiringProposal, agentID pgtype.UUID, superseded int64) []byte {
	body, _ := json.Marshal(map[string]any{
		"proposal_id":      uuidString(prop.ID),
		"agent_id":         uuidString(agentID),
		"role_title":       prop.RoleTitle,
		"department_slug":  prop.DepartmentSlug,
		"proposal_type":    prop.ProposalType,
		"superseded_count": superseded,
	})
	return body
}

func rejectVerbForProposalType(t string) string {
	switch t {
	case "skill_change":
		return "reject_skill_change"
	case "version_bump":
		return "reject_version_bump"
	default:
		return "reject_hire"
	}
}

// composeApproveHireAgentMD synthesises the new agent's agent.md from
// the proposal payload. The plan calls for the operator-approved
// snapshot; until the operator-side authoring UX lands (T015 / post-
// M7 polish), the synthesis below produces a valid baseline doc that
// later edits flow through `update_agent_md` to refine.
func composeApproveHireAgentMD(prop store.HiringProposal) string {
	skills := ""
	if prop.SkillsSummaryMd != nil && *prop.SkillsSummaryMd != "" {
		skills = "\n\n## Skills\n\n" + *prop.SkillsSummaryMd
	}
	return fmt.Sprintf("# %s\n\n%s%s\n", prop.RoleTitle, prop.JustificationMd, skills)
}

// skillsJSONFromProposal extracts the `add` array from a skill_change
// proposal's snapshot, falling back to an empty array. New-agent
// proposals won't carry one — both shapes return a valid JSONB.
func skillsJSONFromProposal(prop store.HiringProposal) ([]byte, error) {
	if len(prop.ProposalSnapshotJsonb) == 0 {
		return []byte("[]"), nil
	}
	var snap struct {
		Add []map[string]any `json:"add"`
	}
	if err := json.Unmarshal(prop.ProposalSnapshotJsonb, &snap); err != nil {
		return []byte("[]"), nil
	}
	if len(snap.Add) == 0 {
		return []byte("[]"), nil
	}
	body, err := json.Marshal(snap.Add)
	if err != nil {
		return []byte("[]"), ErrInvalidSnapshotPayload
	}
	return body, nil
}

// defaultListensForJSON gives a fresh hire a department-scoped
// listens_for shape. Operator can edit via update_agent_md or future
// M7 dashboard surfaces; this is the always-safe baseline.
func defaultListensForJSON(deptSlug string) []byte {
	return []byte(fmt.Sprintf(`["work.ticket.created.%s.todo"]`, deptSlug))
}

// defaultAgentModel is the model new hires inherit by default. Override
// via update_agent_md or a future M7 dashboard `model` field. Picks
// the cheapest 4-family model so a freshly-hired agent doesn't blow
// the company throttle gate on its first spawn.
const defaultAgentModel = "claude-haiku-4-5-20251001"
