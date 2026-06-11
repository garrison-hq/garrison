-- M9 scheduled wake-up queries (plan §sqlc).
-- Used by:
--   - supervisor/internal/schedule/ (tick loop, firing transaction,
--     validation).
--   - supervisor/internal/spawn/oneshot.go (SpawnOneshot +
--     WriteFinalizeOneshot).
--   - supervisor/internal/finalize/ (oneshot double-commit guard).
--   - supervisor/internal/garrisonmutate/verbs_scheduled.go
--     (create_scheduled_task chat verb).
--   - supervisor/internal/dashboardapi/ (POST /schedule/validate).
--
-- All parameters use sqlc.arg(name) exclusively (M7 retro gotcha).

-- name: ClaimDueScheduledTasks :many
-- Tick-loop claim (FR-101). FOR UPDATE SKIP LOCKED gives each due slot
-- at most one firing regardless of concurrent claim attempts. Paused
-- and soft-deleted tasks are never claimed; the partial index
-- idx_scheduled_tasks_due serves exactly this predicate. Claim limit
-- (Deps.ClaimLimit, default 20) bounds tx size — remaining due tasks
-- claim on the next tick.
SELECT id, name, department_id, role_slug, mode, schedule_expr,
       next_fire_at, objective_template, acceptance_criteria_template,
       paused, deleted_at, last_fired_at, created_at, updated_at
  FROM scheduled_tasks
 WHERE NOT paused
   AND deleted_at IS NULL
   AND next_fire_at <= now()
 ORDER BY next_fire_at
   FOR UPDATE SKIP LOCKED
 LIMIT sqlc.arg(claim_limit)::int;

-- name: AdvanceScheduledTask :exec
-- Always advances exactly one future slot regardless of outcome
-- (collapse + skip + defer all consume the slot; FR-104). fired is
-- "this slot actually fired": last_fired_at updates ONLY then (FR-107:
-- {{last_fired_at}} means the previous *firing*, not the previous
-- claim).
UPDATE scheduled_tasks
   SET next_fire_at  = sqlc.arg(next_fire_at),
       last_fired_at = CASE WHEN sqlc.arg(fired)::bool
                            THEN sqlc.arg(fired_at)::timestamptz
                            ELSE last_fired_at END,
       updated_at    = now()
 WHERE id = sqlc.arg(id);

-- name: InsertScheduledTaskRun :one
-- One row per firing attempt (FR-108). detail carries the
-- human-readable reason for non-fired outcomes; ticket_id is set for
-- ticket-mode 'fired' rows only (oneshot anchors arrive later via
-- SetRunAgentInstance).
INSERT INTO scheduled_task_runs (scheduled_task_id, slot_at, outcome, detail, ticket_id)
VALUES (
    sqlc.arg(scheduled_task_id),
    sqlc.arg(slot_at),
    sqlc.arg(outcome),
    sqlc.arg(detail),
    sqlc.arg(ticket_id)
)
RETURNING id, fired_at;

-- name: UpdateRunOutcome :exec
-- Oneshot gate-defer / pre-pipeline spawn failure. gate_deferred is
-- NON-terminal for oneshot runs: a successful poll re-dispatch clears
-- the run back to 'fired' via this same query before the instance
-- insert (FR-401).
UPDATE scheduled_task_runs
   SET outcome = sqlc.arg(outcome),
       detail  = sqlc.arg(detail)
 WHERE id = sqlc.arg(id);

-- name: UpdateRunStructuredOutcome :exec
-- WriteFinalizeOneshot commit: full finalize payload + the
-- verification sub-object, in the same tx as the palace writes and the
-- terminal agent_instances row.
UPDATE scheduled_task_runs
   SET structured_outcome = sqlc.arg(structured_outcome)
 WHERE id = sqlc.arg(id);

-- name: SetRunAgentInstance :exec
-- Backfills the oneshot anchor at spawn (SpawnOneshot step 3): run row
-- gains the agent_instances FK so completion state reads through the
-- joined instance status (plan decision 5).
UPDATE scheduled_task_runs
   SET agent_instance_id = sqlc.arg(agent_instance_id)
 WHERE id = sqlc.arg(id);

-- name: InsertScheduledTicket :one
-- M5.3 InsertChatTicket mirror for ticket-mode firings: dept, role,
-- rendered objective + acceptance criteria, column_slug='todo'. The
-- task's role_slug rides metadata (tickets carry no role column; the
-- dispatcher routes by the dept/column channel as for any ticket).
-- The tickets INSERT trigger emits the outbox row + the existing
-- work.ticket.created.<dept>.todo notify in this same tx.
INSERT INTO tickets (
    department_id, objective, acceptance_criteria, column_slug,
    metadata, origin
) VALUES (
    sqlc.arg(department_id),
    sqlc.arg(objective),
    sqlc.arg(acceptance_criteria),
    'todo',
    jsonb_build_object('role_slug', sqlc.arg(role_slug)::text),
    'scheduled'
)
RETURNING id, created_at;

-- name: HasOpenTicketForTask :one
-- Overlap predicate, ticket mode (FR-202): a slot arriving while a
-- previously fired ticket is still open (not yet 'done', per spec
-- US1-AS4) skips-and-advances. Any run of this task whose ticket has
-- not reached 'done' counts — the overlap discipline itself guarantees
-- at most one such ticket exists, so "any open" and "latest run's
-- ticket open" coincide.
SELECT EXISTS (
    SELECT 1
      FROM scheduled_task_runs r
      JOIN tickets t ON t.id = r.ticket_id
     WHERE r.scheduled_task_id = sqlc.arg(scheduled_task_id)
       AND t.column_slug <> 'done'
) AS has_open_ticket;

-- name: HasRunningOneshotForTask :one
-- Overlap predicate, oneshot mode (FR-303): the latest run counts as
-- in-flight when its outcome is 'fired' or 'gate_deferred' AND either
-- no instance has been backfilled yet (fired-but-not-yet-dispatched —
-- closes the tick→dispatch window) or the joined instance is still
-- running. gate_deferred is awaiting-poll-retry, hence in-flight.
SELECT EXISTS (
    SELECT 1
      FROM (SELECT r.outcome, r.agent_instance_id
              FROM scheduled_task_runs r
             WHERE r.scheduled_task_id = sqlc.arg(scheduled_task_id)
             ORDER BY r.fired_at DESC
             LIMIT 1) latest
      LEFT JOIN agent_instances ai ON ai.id = latest.agent_instance_id
     WHERE latest.outcome IN ('fired', 'gate_deferred')
       AND (latest.agent_instance_id IS NULL OR ai.status = 'running')
) AS has_running_oneshot;

-- name: SelectScheduledTaskByRunID :one
-- Run → task join for SpawnOneshot prep (plan §2 step 1): the
-- dispatcher hands over a scheduled_task_run_id; this resolves the
-- task identity, templates, and last-fired state in one read.
SELECT r.id AS run_id,
       r.slot_at,
       r.outcome,
       r.detail,
       r.agent_instance_id,
       st.id AS scheduled_task_id,
       st.name,
       st.department_id,
       st.role_slug,
       st.mode,
       st.schedule_expr,
       st.next_fire_at,
       st.objective_template,
       st.acceptance_criteria_template,
       st.paused,
       st.deleted_at,
       st.last_fired_at
  FROM scheduled_task_runs r
  JOIN scheduled_tasks st ON st.id = r.scheduled_task_id
 WHERE r.id = sqlc.arg(run_id);

-- name: SelectScheduledTaskRunFinalizedState :one
-- Oneshot double-commit guard (FR-260 analog, keyed by run id): the
-- finalize MCP server and WriteFinalizeOneshot both consult this
-- before committing. A non-NULL structured_outcome means the payload
-- already landed.
SELECT (r.structured_outcome IS NOT NULL) AS finalized,
       r.outcome,
       r.agent_instance_id
  FROM scheduled_task_runs r
 WHERE r.id = sqlc.arg(run_id);

-- name: InsertScheduledTask :one
-- Verb + dashboard support. Caller (schedule.ValidateTask consumers)
-- has already validated grammar, min-interval, future first slot,
-- name uniqueness, department + role existence, non-empty templates,
-- and mode before this INSERT; next_fire_at arrives precomputed.
INSERT INTO scheduled_tasks (
    name, department_id, role_slug, mode, schedule_expr,
    next_fire_at, objective_template, acceptance_criteria_template
) VALUES (
    sqlc.arg(name),
    sqlc.arg(department_id),
    sqlc.arg(role_slug),
    sqlc.arg(mode),
    sqlc.arg(schedule_expr),
    sqlc.arg(next_fire_at),
    sqlc.arg(objective_template),
    sqlc.arg(acceptance_criteria_template)
)
RETURNING id, name, department_id, role_slug, mode, schedule_expr,
          next_fire_at, objective_template, acceptance_criteria_template,
          paused, deleted_at, last_fired_at, created_at, updated_at;

-- name: SelectScheduledTaskByName :one
-- Name-uniqueness probe for validation (live rows only — a deleted
-- task's name is reusable, matching idx_scheduled_tasks_name_live).
SELECT id, name, department_id, role_slug, mode, schedule_expr,
       next_fire_at, objective_template, acceptance_criteria_template,
       paused, deleted_at, last_fired_at, created_at, updated_at
  FROM scheduled_tasks
 WHERE name = sqlc.arg(name)
   AND deleted_at IS NULL;

-- name: ListScheduledTasks :many
-- Live tasks only; soft-deleted rows are history, not listings.
SELECT id, name, department_id, role_slug, mode, schedule_expr,
       next_fire_at, objective_template, acceptance_criteria_template,
       paused, deleted_at, last_fired_at, created_at, updated_at
  FROM scheduled_tasks
 WHERE deleted_at IS NULL
 ORDER BY name;

-- name: NotifyOneshotDue :exec
-- In-tx notify for oneshot firings. The channel literal is baked into
-- the query (M6 retro gotcha 3: 'work.scheduled.oneshot_due' contains
-- dots, so call sites must never assemble it — and every LISTEN
-- statement must double-quote it). Composes with the outbox row on the
-- tick tx so the dispatcher sees the notify only if the row committed.
SELECT pg_notify('work.scheduled.oneshot_due', sqlc.arg(payload)::text);
