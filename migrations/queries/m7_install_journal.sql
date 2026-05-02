-- M7 install journal queries.
-- Used by supervisor/internal/skillinstall/journal.go (writes per step)
-- and supervisor/internal/skillinstall/recover.go (resume-or-rollback
-- on supervisor restart). Per-step audit is the M5.3 chat_mutation_audit
-- analogue scoped to install pipeline steps; see migration §"Schema
-- deviation from spec FR-214a" for the rationale.

-- name: InsertInstallStep :one
-- One row per material install step. Outcome captured at the step's
-- terminal event; pre-step rows OMIT outcome (NULL via the helper
-- pattern below) so an interrupted step can be detected by recovery
-- as "row exists with no terminal outcome".
INSERT INTO agent_install_journal (
    proposal_id, step, outcome, error_kind, payload_jsonb
) VALUES ($1, $2, $3, $4, $5)
RETURNING id, created_at;

-- name: GetLatestInstallStep :one
-- Read at supervisor restart by skillinstall.Resume to determine
-- where to pick up. Returns the most recent step regardless of
-- outcome; the resumer interprets:
--   - outcome='success' on the last step in the pipeline -> done
--   - outcome='success' on intermediate step -> resume from next step
--   - outcome='failed' -> install_failed terminal
--   - outcome='interrupted' -> recover detected supervisor crash mid-step
SELECT * FROM agent_install_journal
WHERE proposal_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: ListInstallStepsForProposal :many
-- Forensic-query view (FR-111). Returns every step row for a proposal
-- in chronological order so the operator can see the full install
-- history.
SELECT * FROM agent_install_journal
WHERE proposal_id = $1
ORDER BY created_at ASC;

-- name: MarkInterruptedSteps :execrows
-- Called once at supervisor startup, before the per-proposal Resume
-- pass. Any step row that lacks a terminal outcome AND whose proposal
-- is install_in_progress is marked 'interrupted' so subsequent reads
-- see the unambiguous state. Returns the count of rows touched.
UPDATE agent_install_journal aij
SET outcome = 'interrupted'
FROM hiring_proposals hp
WHERE aij.proposal_id = hp.id
  AND hp.status = 'install_in_progress'
  AND aij.outcome NOT IN ('success', 'failed', 'interrupted')
  AND aij.id IN (
      SELECT id FROM agent_install_journal
      WHERE proposal_id = aij.proposal_id
      ORDER BY created_at DESC
      LIMIT 1
  );

-- name: CountSuccessfulStepsForProposal :one
-- Cheap read used by Resume to short-circuit when all 6 steps are
-- already success.
SELECT COUNT(*) AS n
FROM agent_install_journal
WHERE proposal_id = $1
  AND outcome = 'success';
