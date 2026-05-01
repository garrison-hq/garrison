-- M5.3 hiring_proposals queries.
-- Used by supervisor/internal/garrisonmutate/verbs_hiring.go for the
-- propose_hire verb's INSERT path, and by the dashboard's stopgap
-- /hiring/proposals page for the read-side. Dashboard-side reads via
-- Drizzle directly; supervisor-side writes via these sqlc queries.

-- name: InsertHiringProposal :one
-- Used by garrison-mutate.propose_hire. Always sets proposed_via to
-- 'ceo_chat' and proposed_by_chat_session_id when invoked from the
-- chat verb path. M7's dashboard-side proposal action will use this
-- same query with proposed_via='dashboard' and a NULL chat_session_id.
INSERT INTO hiring_proposals (
    role_title, department_slug, justification_md, skills_summary_md,
    proposed_via, proposed_by_chat_session_id
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, created_at, status;

-- name: GetHiringProposalByID :one
SELECT * FROM hiring_proposals WHERE id = $1;

-- name: ListHiringProposals :many
SELECT * FROM hiring_proposals
ORDER BY created_at DESC
LIMIT $1;

-- name: ListHiringProposalsByStatus :many
SELECT * FROM hiring_proposals
WHERE status = $1
ORDER BY created_at DESC
LIMIT $2;
