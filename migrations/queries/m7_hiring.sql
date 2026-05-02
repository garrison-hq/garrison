-- M7 hiring queries.
-- Extends the M5.3 hiring_proposals surface with M7 columns (target_agent_id,
-- proposal_type, skill_diff_jsonb, proposal_snapshot_jsonb, skill_digest_at_propose,
-- approval/rejection metadata, install lifecycle states). Used by:
--   - supervisor/internal/garrisonmutate/verbs_hiring.go (propose_skill_change,
--     bump_skill_version chat verbs)
--   - supervisor/internal/garrisonmutate/approve.go (Server-Action helpers)
--   - dashboard /admin/hires reads via Drizzle directly.

-- name: InsertHiringProposalM7 :one
-- Variant of InsertHiringProposal carrying the M7 fields. Used by
-- propose_skill_change + bump_skill_version. proposal_type is
-- explicit; target_agent_id required for skill_change/version_bump.
INSERT INTO hiring_proposals (
    role_title, department_slug, justification_md, skills_summary_md,
    proposed_via, proposed_by_chat_session_id,
    target_agent_id, proposal_type,
    skill_diff_jsonb, proposal_snapshot_jsonb, skill_digest_at_propose
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id, created_at, status, proposal_type;

-- name: ListPendingProposalsByTargetAgent :many
-- FR-109 sibling-proposal display: pending proposals targeting the
-- same agent (so the operator can pick which one they meant).
SELECT * FROM hiring_proposals
WHERE status = 'pending'
  AND target_agent_id = $1
ORDER BY created_at DESC;

-- name: ApproveProposal :exec
-- Server-Action approve write. Status moves pending -> approved (or
-- pending -> install_in_progress for proposals that queue an install
-- atomically; second UPDATE is the caller's responsibility).
UPDATE hiring_proposals
SET status = 'approved', approved_at = NOW(), approved_by = $1
WHERE id = $2 AND status = 'pending';

-- name: RejectProposal :exec
UPDATE hiring_proposals
SET status = 'rejected', rejected_at = NOW(), rejected_reason = $1
WHERE id = $2 AND status = 'pending';

-- name: TransitionToInstallInProgress :exec
-- Called inside the same Server-Action tx as ApproveProposal so the
-- skillinstall.Actuator can pick up the proposal from a single
-- consistent state.
UPDATE hiring_proposals
SET status = 'install_in_progress'
WHERE id = $1 AND status = 'approved';

-- name: MarkInstalled :exec
UPDATE hiring_proposals
SET status = 'installed'
WHERE id = $1 AND status = 'install_in_progress';

-- name: MarkInstallFailed :exec
UPDATE hiring_proposals
SET status = 'install_failed', rejected_reason = $1
WHERE id = $2 AND status IN ('install_in_progress', 'approved');

-- name: SupersedeSiblingProposals :execrows
-- FR-110a: when one proposal is approved, sibling pending proposals
-- targeting the same (agent, package, type) auto-reject. Returns
-- the count of rows superseded so the audit row can record it.
UPDATE hiring_proposals
SET status = 'superseded',
    rejected_at = NOW(),
    rejected_reason = 'superseded_by:' || $1::text
WHERE status = 'pending'
  AND target_agent_id = $2
  AND proposal_type = $3
  AND id != $4;

-- name: ListInstallInProgressProposals :many
-- Read at supervisor restart by skillinstall.recover so the install
-- pipeline can resume each in-flight install from its journal.
SELECT * FROM hiring_proposals
WHERE status = 'install_in_progress'
ORDER BY approved_at ASC;

-- name: InsertAgentForHire :one
-- Used by garrisonmutate.ApproveHire (Server Action). Creates a new
-- agents row from an approved proposal. The agent's model + status
-- + listens_for default at insert time; agent_md is the operator-
-- approved snapshot, skills come from the proposal_snapshot_jsonb.
INSERT INTO agents (
    department_id, role_slug, agent_md, model,
    skills, listens_for, palace_wing, status,
    image_digest, mcp_servers_jsonb
) VALUES (
    sqlc.arg(department_id),
    sqlc.arg(role_slug),
    sqlc.arg(agent_md),
    sqlc.arg(model),
    sqlc.arg(skills_jsonb),
    sqlc.arg(listens_for_jsonb),
    sqlc.arg(palace_wing),
    'active',
    '',
    sqlc.arg(mcp_servers_jsonb)
)
RETURNING id;

-- name: UpdateAgentSkills :exec
-- Used by garrisonmutate.ApproveSkillChange and ApproveVersionBump
-- post-install. Replaces the agents.skills JSONB with the
-- operator-approved skill set captured in the proposal snapshot.
UPDATE agents SET skills = sqlc.arg(skills_jsonb) WHERE id = sqlc.arg(id);
