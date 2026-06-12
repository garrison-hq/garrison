// M9 /admin/recurring-jobs — read-side queries for scheduled tasks
// (plan §8). Live rows only: every task-listing query filters
// `deleted_at IS NULL` (FR-502 soft delete — run history and audit
// rows survive; the deleted task itself disappears from the UI).
//
// Run history reads through the join to `agent_instances.status` so
// oneshot rows can show terminal instance state next to the run's
// outcome (plan decision 5).
//
// Per AGENTS.md "Tests for Go only, never frontend": this module ships
// without vitest coverage; the Go-side T016/T017 integration suites pin
// the row shapes these read.

import { eq, desc, asc, isNull, inArray, and, sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import {
  scheduledTasks,
  scheduledTaskRuns,
  agentInstances,
  departments,
} from '@/drizzle/schema.supervisor';

export type ScheduledTaskMode = 'ticket' | 'oneshot';

export type ScheduledRunOutcome = 'fired' | 'skipped_overlap' | 'gate_deferred' | 'failed';

export interface ScheduledTaskRow {
  id: string;
  name: string;
  departmentId: string;
  departmentSlug: string;
  departmentName: string;
  roleSlug: string;
  mode: ScheduledTaskMode;
  scheduleExpr: string;
  nextFireAt: string;
  objectiveTemplate: string;
  acceptanceCriteriaTemplate: string;
  paused: boolean;
  lastFiredAt: string | null;
  createdAt: string;
  updatedAt: string;
  /** Outcome of the most recent run, null when the task never claimed
   *  a slot. Drives the list page's "last outcome" column (T015). */
  lastOutcome: ScheduledRunOutcome | null;
}

export interface ScheduledTaskRunRow {
  id: string;
  scheduledTaskId: string;
  slotAt: string;
  firedAt: string;
  outcome: ScheduledRunOutcome;
  detail: string | null;
  ticketId: string | null;
  agentInstanceId: string | null;
  structuredOutcome: unknown;
  /** Joined `agent_instances.status` for oneshot firings — null for
   *  ticket-mode runs and for fired-but-not-yet-dispatched oneshots. */
  instanceStatus: string | null;
  instanceExitReason: string | null;
}

/** Ticket → originating schedule, for the kanban TicketCard's
 *  scheduled-origin chip (FR-201 / US1-AS2, M6 T017 parent-chip
 *  pattern). Keyed by ticket id. */
export interface ScheduledOrigin {
  ticketId: string;
  taskId: string;
  taskName: string;
}

// Correlated subquery: the latest run's outcome for a task. Uses the
// idx_scheduled_task_runs_task (scheduled_task_id, fired_at DESC) index.
const lastOutcomeExpr = sql<string | null>`(
  SELECT r.outcome FROM scheduled_task_runs r
  WHERE r.scheduled_task_id = ${scheduledTasks.id}
  ORDER BY r.fired_at DESC
  LIMIT 1
)`;

const taskSelection = {
  id: scheduledTasks.id,
  name: scheduledTasks.name,
  departmentId: scheduledTasks.departmentId,
  departmentSlug: departments.slug,
  departmentName: departments.name,
  roleSlug: scheduledTasks.roleSlug,
  mode: scheduledTasks.mode,
  scheduleExpr: scheduledTasks.scheduleExpr,
  nextFireAt: scheduledTasks.nextFireAt,
  objectiveTemplate: scheduledTasks.objectiveTemplate,
  acceptanceCriteriaTemplate: scheduledTasks.acceptanceCriteriaTemplate,
  paused: scheduledTasks.paused,
  lastFiredAt: scheduledTasks.lastFiredAt,
  createdAt: scheduledTasks.createdAt,
  updatedAt: scheduledTasks.updatedAt,
  lastOutcome: lastOutcomeExpr,
};

function toTaskRow(r: {
  id: string;
  name: string;
  departmentId: string;
  departmentSlug: string;
  departmentName: string;
  roleSlug: string;
  mode: string;
  scheduleExpr: string;
  nextFireAt: string;
  objectiveTemplate: string;
  acceptanceCriteriaTemplate: string;
  paused: boolean;
  lastFiredAt: string | null;
  createdAt: string;
  updatedAt: string;
  lastOutcome: string | null;
}): ScheduledTaskRow {
  return {
    ...r,
    mode: r.mode as ScheduledTaskMode,
    lastOutcome: (r.lastOutcome as ScheduledRunOutcome | null) ?? null,
  };
}

/** listScheduledTasks — every live (non-deleted) task, newest first,
 *  with department names and the latest run outcome joined in. */
export async function listScheduledTasks(limit = 200): Promise<ScheduledTaskRow[]> {
  const rows = await appDb
    .select(taskSelection)
    .from(scheduledTasks)
    .innerJoin(departments, eq(scheduledTasks.departmentId, departments.id))
    .where(isNull(scheduledTasks.deletedAt))
    .orderBy(desc(scheduledTasks.createdAt))
    .limit(limit);
  return rows.map(toTaskRow);
}

/** getScheduledTaskById — single live row for the detail page; null
 *  when missing or soft-deleted. */
export async function getScheduledTaskById(id: string): Promise<ScheduledTaskRow | null> {
  const rows = await appDb
    .select(taskSelection)
    .from(scheduledTasks)
    .innerJoin(departments, eq(scheduledTasks.departmentId, departments.id))
    .where(and(eq(scheduledTasks.id, id), isNull(scheduledTasks.deletedAt)))
    .limit(1);
  if (rows.length === 0) return null;
  return toTaskRow(rows[0]);
}

/** getTaskRunHistory — the task's run rows newest-first, left-joined
 *  to agent_instances.status so oneshot terminal state is readable
 *  (decision 5). Runs are immutable history: this intentionally does
 *  NOT filter on the task's deleted_at — history survives soft delete
 *  (FR-502). */
export async function getTaskRunHistory(
  taskId: string,
  limit = 50,
): Promise<ScheduledTaskRunRow[]> {
  const rows = await appDb
    .select({
      id: scheduledTaskRuns.id,
      scheduledTaskId: scheduledTaskRuns.scheduledTaskId,
      slotAt: scheduledTaskRuns.slotAt,
      firedAt: scheduledTaskRuns.firedAt,
      outcome: scheduledTaskRuns.outcome,
      detail: scheduledTaskRuns.detail,
      ticketId: scheduledTaskRuns.ticketId,
      agentInstanceId: scheduledTaskRuns.agentInstanceId,
      structuredOutcome: scheduledTaskRuns.structuredOutcome,
      instanceStatus: agentInstances.status,
      instanceExitReason: agentInstances.exitReason,
    })
    .from(scheduledTaskRuns)
    .leftJoin(agentInstances, eq(scheduledTaskRuns.agentInstanceId, agentInstances.id))
    .where(eq(scheduledTaskRuns.scheduledTaskId, taskId))
    .orderBy(desc(scheduledTaskRuns.firedAt))
    .limit(limit);
  return rows.map((r) => ({
    ...r,
    outcome: r.outcome as ScheduledRunOutcome,
  }));
}

/** getScheduledOriginForTickets — ticket → run → task name, for the
 *  kanban scheduled-origin chip (plan §8). Returns a map keyed by
 *  ticket id; tickets with no scheduled origin are simply absent.
 *  Includes soft-deleted tasks: an existing ticket's provenance does
 *  not vanish when the schedule is deleted. */
export async function getScheduledOriginForTickets(
  ticketIds: string[],
): Promise<Record<string, ScheduledOrigin>> {
  if (ticketIds.length === 0) return {};
  const rows = await appDb
    .select({
      ticketId: scheduledTaskRuns.ticketId,
      taskId: scheduledTasks.id,
      taskName: scheduledTasks.name,
    })
    .from(scheduledTaskRuns)
    .innerJoin(scheduledTasks, eq(scheduledTaskRuns.scheduledTaskId, scheduledTasks.id))
    .where(inArray(scheduledTaskRuns.ticketId, ticketIds))
    .orderBy(asc(scheduledTaskRuns.firedAt));
  const out: Record<string, ScheduledOrigin> = {};
  for (const r of rows) {
    if (!r.ticketId) continue;
    out[r.ticketId] = { ticketId: r.ticketId, taskId: r.taskId, taskName: r.taskName };
  }
  return out;
}
