'use server';

// M9 scheduled-task Server Actions (plan §8, decision 11): five
// audit-writing CRUD actions for /admin/recurring-jobs. Each action:
//
//   1. session check (operator-only surface);
//   2. validation via the supervisor's POST /schedule/validate
//      (create / edit / resume — decision 10: grammar + next-fire
//      computation single-source in Go, no TS date-math mirror).
//      Review #4: create/edit send the FULL body so the supervisor runs
//      schedule.ValidateTask (role existence, duplicate live name,
//      department existence, templates, mode) and returns typed field
//      errors; resume keeps the expression-only shape;
//   3. one drizzle tx writing the row change + the chat_mutation_audit
//      row (verb from the Go-side ServerActionVerbs registry, chat
//      anchors NULL, affected_resource_type='scheduled_task').
//
// deleteScheduledTask is a SOFT delete (`SET deleted_at = now()`,
// FR-502): run history and audit rows survive; the name becomes
// reusable via the idx_scheduled_tasks_name_live partial unique index.
// Tier-3 verbs (create / delete) snapshot full args / pre-state into
// args_jsonb per the chat-threat-model §5 table.
//
// resumeScheduledTask recomputes next_fire_at through the validate
// endpoint — advance-only: the returned slot is strictly future, so a
// resume never back-fires missed slots (FR-50x pause semantics).
//
// Typed result returns, no throws (M8 mcpServer.ts shape). Per AGENTS.md
// "Tests for Go only, never frontend": no vitest lands here; the Go
// integration suites (T016/T017) pin the row shapes these write/read.

import { eq, and, isNull, sql } from 'drizzle-orm';
import { cookies } from 'next/headers';
import { appDb } from '@/lib/db/appClient';
import {
  scheduledTasks,
  chatMutationAudit,
} from '@/drizzle/schema.supervisor';
import { getSession } from '@/lib/auth/session';

export type ScheduledTaskActionErrorKind =
  | 'unauthorized'
  | 'validation_failed'
  | 'not_found'
  | 'network_error'
  | 'internal_error';

export type ScheduledTaskActionResult =
  | { ok: true; id: string; auditId: string }
  | {
      ok: false;
      errorKind: ScheduledTaskActionErrorKind;
      message: string;
      /** Offending field on validation_failed (review #4: the supervisor's
       *  full-body /schedule/validate returns typed field errors —
       *  name / department_id / role_slug / mode / schedule_expr /
       *  objective_template / acceptance_criteria_template). */
      field?: string;
    };

export interface CreateScheduledTaskInput {
  name: string;
  departmentId: string;
  roleSlug: string;
  mode: string;
  scheduleExpr: string;
  objectiveTemplate: string;
  acceptanceCriteriaTemplate: string;
}

export interface EditScheduledTaskInput {
  name?: string;
  roleSlug?: string;
  scheduleExpr?: string;
  objectiveTemplate?: string;
  acceptanceCriteriaTemplate?: string;
}

const VALID_MODES = new Set(['ticket', 'oneshot']);
const SESSION_COOKIE = 'better-auth.session_token';

// ---------------------------------------------------------------------------
// POST /schedule/validate plumbing (companyMD.ts cookie-forward pattern:
// the dashboardapi auth middleware validates the forwarded better-auth
// session cookie against the sessions table).
// ---------------------------------------------------------------------------

type ValidateResult =
  | { ok: true; nextFireAt: string }
  | {
      ok: false;
      errorKind: 'validation_failed' | 'network_error' | 'internal_error';
      message: string;
      field?: string;
    };

/** Full-body request shape for POST /schedule/validate (review #4).
 *  Expression-only bodies (resume) keep the original grammar +
 *  min-interval contract; bodies carrying any task-identity field run
 *  the supervisor's full schedule.ValidateTask — the single FR-105
 *  validator shared with the chat verb — so create/edit catch unknown
 *  roles and duplicate live names before the write tx. */
interface ValidateRequestBody {
  schedule_expr: string;
  mode: string;
  name?: string;
  department_id?: string;
  role_slug?: string;
  objective_template?: string;
  acceptance_criteria_template?: string;
}

async function validateSchedule(reqBody: ValidateRequestBody): Promise<ValidateResult> {
  const base = process.env.DASHBOARD_SUPERVISOR_API_URL;
  if (!base) {
    return {
      ok: false,
      errorKind: 'internal_error',
      message:
        'DASHBOARD_SUPERVISOR_API_URL is not set. Configure it in docker-compose.yml or .env (see dashboard/.env.example).',
    };
  }
  const store = await cookies();
  const cookie = store.get(SESSION_COOKIE);
  const cookieHeader = cookie ? `${SESSION_COOKIE}=${cookie.value}` : '';

  let res: Response;
  try {
    res = await fetch(`${base}/schedule/validate`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...(cookieHeader ? { Cookie: cookieHeader } : {}),
      },
      body: JSON.stringify(reqBody),
      cache: 'no-store',
    });
  } catch {
    return {
      ok: false,
      errorKind: 'network_error',
      message: 'Could not reach the supervisor API to validate the schedule expression.',
    };
  }

  let body: unknown = null;
  try {
    body = await res.json();
  } catch {
    body = null;
  }

  if (res.ok) {
    const nextFireAt = (body as { next_fire_at?: string } | null)?.next_fire_at;
    if (!nextFireAt) {
      return {
        ok: false,
        errorKind: 'internal_error',
        message: 'supervisor /schedule/validate returned no next_fire_at',
      };
    }
    return { ok: true, nextFireAt };
  }

  const errBody = (body as { error?: string; message?: string; field?: string } | null) ?? {};
  if (res.status === 422 && errBody.error === 'validation_failed') {
    // Friendly mapping for unique-name violations (review #4 / U2):
    // the operator-facing wording beats the Go validator's detail.
    const message =
      errBody.field === 'name' && reqBody.name
        ? `A scheduled task named "${reqBody.name}" already exists. Choose a different name.`
        : (errBody.message ?? 'schedule expression rejected');
    return {
      ok: false,
      errorKind: 'validation_failed',
      message,
      ...(errBody.field ? { field: errBody.field } : {}),
    };
  }
  return {
    ok: false,
    errorKind: 'internal_error',
    message: errBody.message ?? `supervisor /schedule/validate returned ${res.status}`,
  };
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

function fail(
  errorKind: ScheduledTaskActionErrorKind,
  message: string,
  field?: string,
): ScheduledTaskActionResult {
  return { ok: false, errorKind, message, ...(field ? { field } : {}) };
}

/** failFromValidate maps a ValidateResult failure (typed field error
 *  included) onto the action result shape. */
function failFromValidate(
  v: Extract<ValidateResult, { ok: false }>,
): ScheduledTaskActionResult {
  return fail(v.errorKind, v.message, v.field);
}

async function requireSession(): Promise<ScheduledTaskActionResult | null> {
  const session = await getSession();
  if (!session) {
    return fail('unauthorized', 'scheduled-task actions require an operator session');
  }
  return null;
}

type Tx = Parameters<Parameters<typeof appDb.transaction>[0]>[0];

// writeAudit inserts the chat_mutation_audit row inside the action's tx.
// Chat anchors are NULL (dashboard path); verb/tier values mirror the
// Go-side ServerActionVerbs registry (edit=2, pause=1, resume=1,
// delete=3) and the Verbs entry for create (Tier 3).
async function writeAudit(
  tx: Tx,
  verb:
    | 'create_scheduled_task'
    | 'edit_scheduled_task'
    | 'pause_scheduled_task'
    | 'resume_scheduled_task'
    | 'delete_scheduled_task',
  reversibilityClass: 1 | 2 | 3,
  taskId: string,
  argsJsonb: Record<string, unknown>,
): Promise<string> {
  const rows = await tx
    .insert(chatMutationAudit)
    .values({
      chatSessionId: null,
      chatMessageId: null,
      verb,
      argsJsonb,
      outcome: 'success',
      reversibilityClass,
      affectedResourceId: taskId,
      affectedResourceType: 'scheduled_task',
    })
    .returning({ id: chatMutationAudit.id });
  return rows[0].id;
}

// selectLiveTask reads the live (non-deleted) row inside the tx; null
// when missing or already soft-deleted.
async function selectLiveTask(tx: Tx, id: string) {
  const rows = await tx
    .select()
    .from(scheduledTasks)
    .where(and(eq(scheduledTasks.id, id), isNull(scheduledTasks.deletedAt)))
    .limit(1);
  return rows.length === 0 ? null : rows[0];
}

function isLiveNameCollision(err: unknown): boolean {
  const msg = err instanceof Error ? err.message : String(err);
  return msg.includes('idx_scheduled_tasks_name_live');
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

/** createScheduledTask — INSERT a live scheduled_tasks row with the
 *  supervisor-computed next_fire_at + Tier-3 audit row (full args). */
export async function createScheduledTask(
  input: CreateScheduledTaskInput,
): Promise<ScheduledTaskActionResult> {
  const denied = await requireSession();
  if (denied) return denied;

  const name = (input.name ?? '').trim();
  const roleSlug = (input.roleSlug ?? '').trim();
  const mode = (input.mode ?? '').trim();
  const scheduleExpr = (input.scheduleExpr ?? '').trim();
  const objectiveTemplate = input.objectiveTemplate ?? '';
  const acceptanceCriteriaTemplate = input.acceptanceCriteriaTemplate ?? '';

  if (!name) return fail('validation_failed', 'name is required');
  if (!roleSlug) return fail('validation_failed', 'role_slug is required');
  if (!VALID_MODES.has(mode)) {
    return fail('validation_failed', "mode must be 'ticket' or 'oneshot'");
  }
  if (objectiveTemplate.trim().length === 0) {
    return fail('validation_failed', 'objective_template must be non-empty');
  }
  if (acceptanceCriteriaTemplate.trim().length === 0) {
    return fail('validation_failed', 'acceptance_criteria_template must be non-empty');
  }

  // Review #4: full-body validation — the supervisor runs the complete
  // FR-105 set (grammar, min-interval, mode, templates, name uniqueness
  // among live tasks, department + role existence) and returns typed
  // field errors. This replaces the earlier ad-hoc departments SELECT:
  // department existence is part of the shared validator now.
  const validated = await validateSchedule({
    schedule_expr: scheduleExpr,
    mode,
    name,
    department_id: input.departmentId,
    role_slug: roleSlug,
    objective_template: objectiveTemplate,
    acceptance_criteria_template: acceptanceCriteriaTemplate,
  });
  if (!validated.ok) return failFromValidate(validated);

  try {
    return await appDb.transaction(async (tx) => {
      const inserted = await tx
        .insert(scheduledTasks)
        .values({
          name,
          departmentId: input.departmentId,
          roleSlug,
          mode,
          scheduleExpr,
          nextFireAt: validated.nextFireAt,
          objectiveTemplate,
          acceptanceCriteriaTemplate,
        })
        .returning({ id: scheduledTasks.id });
      const taskId = inserted[0].id;
      const auditId = await writeAudit(tx, 'create_scheduled_task', 3, taskId, {
        name,
        department_id: input.departmentId,
        role_slug: roleSlug,
        mode,
        schedule_expr: scheduleExpr,
        next_fire_at: validated.nextFireAt,
        objective_template: objectiveTemplate,
        acceptance_criteria_template: acceptanceCriteriaTemplate,
      });
      return { ok: true as const, id: taskId, auditId };
    });
  } catch (err) {
    if (isLiveNameCollision(err)) {
      return fail('validation_failed', `a live scheduled task named "${name}" already exists`);
    }
    const msg = err instanceof Error ? err.message : String(err);
    return fail('internal_error', msg);
  }
}

/** editScheduledTask — UPDATE the editable fields of a live row; when
 *  schedule_expr changes the validate endpoint recomputes next_fire_at.
 *  Tier-2 audit captures the per-field diff in args_jsonb. */
export async function editScheduledTask(
  id: string,
  input: EditScheduledTaskInput,
): Promise<ScheduledTaskActionResult> {
  const denied = await requireSession();
  if (denied) return denied;

  const patch: Partial<{
    name: string;
    roleSlug: string;
    scheduleExpr: string;
    objectiveTemplate: string;
    acceptanceCriteriaTemplate: string;
  }> = {};
  if (input.name !== undefined) {
    const v = input.name.trim();
    if (!v) return fail('validation_failed', 'name must be non-empty');
    patch.name = v;
  }
  if (input.roleSlug !== undefined) {
    const v = input.roleSlug.trim();
    if (!v) return fail('validation_failed', 'role_slug must be non-empty');
    patch.roleSlug = v;
  }
  if (input.objectiveTemplate !== undefined) {
    if (input.objectiveTemplate.trim().length === 0) {
      return fail('validation_failed', 'objective_template must be non-empty');
    }
    patch.objectiveTemplate = input.objectiveTemplate;
  }
  if (input.acceptanceCriteriaTemplate !== undefined) {
    if (input.acceptanceCriteriaTemplate.trim().length === 0) {
      return fail('validation_failed', 'acceptance_criteria_template must be non-empty');
    }
    patch.acceptanceCriteriaTemplate = input.acceptanceCriteriaTemplate;
  }
  if (input.scheduleExpr !== undefined) {
    patch.scheduleExpr = input.scheduleExpr.trim();
  }
  if (Object.keys(patch).length === 0) {
    return fail('validation_failed', 'no editable fields supplied');
  }

  try {
    // Pre-read outside the validate call so the merged body carries the
    // task's mode/department and the unchanged fields; the tx re-reads
    // for the diff snapshot. Review #4: the full-body validate runs on
    // EVERY edit (not just expression changes) so a role change to a
    // non-existent role or a rename onto a live name rejects with the
    // typed field error before the write tx. The name rides the body
    // only when it actually changes — the live-name uniqueness check
    // would otherwise collide with the task's own row.
    const current = await appDb
      .select()
      .from(scheduledTasks)
      .where(and(eq(scheduledTasks.id, id), isNull(scheduledTasks.deletedAt)))
      .limit(1);
    if (current.length === 0) {
      return fail('not_found', `scheduled task ${id} not found`);
    }
    const cur = current[0];

    const validated = await validateSchedule({
      schedule_expr: patch.scheduleExpr ?? cur.scheduleExpr,
      mode: cur.mode,
      ...(patch.name !== undefined && patch.name !== cur.name ? { name: patch.name } : {}),
      department_id: cur.departmentId,
      role_slug: patch.roleSlug ?? cur.roleSlug,
      objective_template: patch.objectiveTemplate ?? cur.objectiveTemplate,
      acceptance_criteria_template:
        patch.acceptanceCriteriaTemplate ?? cur.acceptanceCriteriaTemplate,
    });
    if (!validated.ok) return failFromValidate(validated);
    const nextFireAt: string | null =
      patch.scheduleExpr !== undefined ? validated.nextFireAt : null;

    return await appDb.transaction(async (tx) => {
      const prior = await selectLiveTask(tx, id);
      if (!prior) {
        return fail('not_found', `scheduled task ${id} not found`);
      }

      const before: Record<string, unknown> = {};
      const after: Record<string, unknown> = {};
      const priorByField: Record<string, string> = {
        name: prior.name,
        roleSlug: prior.roleSlug,
        scheduleExpr: prior.scheduleExpr,
        objectiveTemplate: prior.objectiveTemplate,
        acceptanceCriteriaTemplate: prior.acceptanceCriteriaTemplate,
      };
      const snakeByField: Record<string, string> = {
        name: 'name',
        roleSlug: 'role_slug',
        scheduleExpr: 'schedule_expr',
        objectiveTemplate: 'objective_template',
        acceptanceCriteriaTemplate: 'acceptance_criteria_template',
      };
      for (const [field, value] of Object.entries(patch)) {
        if (priorByField[field] !== value) {
          before[snakeByField[field]] = priorByField[field];
          after[snakeByField[field]] = value;
        }
      }
      if (nextFireAt !== null && patch.scheduleExpr !== prior.scheduleExpr) {
        before.next_fire_at = prior.nextFireAt;
        after.next_fire_at = nextFireAt;
      }

      await tx
        .update(scheduledTasks)
        .set({
          ...patch,
          ...(nextFireAt !== null && patch.scheduleExpr !== prior.scheduleExpr
            ? { nextFireAt }
            : {}),
          updatedAt: sql`NOW()`,
        })
        .where(and(eq(scheduledTasks.id, id), isNull(scheduledTasks.deletedAt)));

      const auditId = await writeAudit(tx, 'edit_scheduled_task', 2, id, {
        task_id: id,
        before,
        after,
      });
      return { ok: true as const, id, auditId };
    });
  } catch (err) {
    if (isLiveNameCollision(err)) {
      return fail('validation_failed', 'a live scheduled task with that name already exists');
    }
    const msg = err instanceof Error ? err.message : String(err);
    return fail('internal_error', msg);
  }
}

/** pauseScheduledTask — SET paused=true; the tick loop's claim query
 *  stops seeing the row immediately (idx_scheduled_tasks_due predicate). */
export async function pauseScheduledTask(id: string): Promise<ScheduledTaskActionResult> {
  const denied = await requireSession();
  if (denied) return denied;

  try {
    return await appDb.transaction(async (tx) => {
      const prior = await selectLiveTask(tx, id);
      if (!prior) return fail('not_found', `scheduled task ${id} not found`);
      if (prior.paused) {
        return fail('validation_failed', 'task is already paused');
      }
      await tx
        .update(scheduledTasks)
        .set({ paused: true, updatedAt: sql`NOW()` })
        .where(and(eq(scheduledTasks.id, id), isNull(scheduledTasks.deletedAt)));
      const auditId = await writeAudit(tx, 'pause_scheduled_task', 1, id, {
        task_id: id,
        name: prior.name,
      });
      return { ok: true as const, id, auditId };
    });
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    return fail('internal_error', msg);
  }
}

/** resumeScheduledTask — SET paused=false with next_fire_at recomputed
 *  through POST /schedule/validate (advance-only: the endpoint returns
 *  the next strictly-future slot, so missed slots never back-fire). */
export async function resumeScheduledTask(id: string): Promise<ScheduledTaskActionResult> {
  const denied = await requireSession();
  if (denied) return denied;

  try {
    const current = await appDb
      .select({ mode: scheduledTasks.mode, scheduleExpr: scheduledTasks.scheduleExpr, paused: scheduledTasks.paused })
      .from(scheduledTasks)
      .where(and(eq(scheduledTasks.id, id), isNull(scheduledTasks.deletedAt)))
      .limit(1);
    if (current.length === 0) return fail('not_found', `scheduled task ${id} not found`);
    if (!current[0].paused) {
      return fail('validation_failed', 'task is not paused');
    }

    const validated = await validateSchedule({
      schedule_expr: current[0].scheduleExpr,
      mode: current[0].mode,
    });
    if (!validated.ok) return failFromValidate(validated);

    return await appDb.transaction(async (tx) => {
      const prior = await selectLiveTask(tx, id);
      if (!prior) return fail('not_found', `scheduled task ${id} not found`);
      await tx
        .update(scheduledTasks)
        .set({ paused: false, nextFireAt: validated.nextFireAt, updatedAt: sql`NOW()` })
        .where(and(eq(scheduledTasks.id, id), isNull(scheduledTasks.deletedAt)));
      const auditId = await writeAudit(tx, 'resume_scheduled_task', 1, id, {
        task_id: id,
        name: prior.name,
        next_fire_at: validated.nextFireAt,
      });
      return { ok: true as const, id, auditId };
    });
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    return fail('internal_error', msg);
  }
}

/** deleteScheduledTask — SOFT delete (`SET deleted_at = now()`, FR-502).
 *  Run history and audit rows survive; the Tier-3 audit row snapshots
 *  the full pre-state into args_jsonb. */
export async function deleteScheduledTask(id: string): Promise<ScheduledTaskActionResult> {
  const denied = await requireSession();
  if (denied) return denied;

  try {
    return await appDb.transaction(async (tx) => {
      const prior = await selectLiveTask(tx, id);
      if (!prior) return fail('not_found', `scheduled task ${id} not found`);
      await tx
        .update(scheduledTasks)
        .set({ deletedAt: sql`NOW()`, updatedAt: sql`NOW()` })
        .where(and(eq(scheduledTasks.id, id), isNull(scheduledTasks.deletedAt)));
      const auditId = await writeAudit(tx, 'delete_scheduled_task', 3, id, {
        task_id: id,
        pre_state: {
          name: prior.name,
          department_id: prior.departmentId,
          role_slug: prior.roleSlug,
          mode: prior.mode,
          schedule_expr: prior.scheduleExpr,
          next_fire_at: prior.nextFireAt,
          objective_template: prior.objectiveTemplate,
          acceptance_criteria_template: prior.acceptanceCriteriaTemplate,
          paused: prior.paused,
          last_fired_at: prior.lastFiredAt,
          created_at: prior.createdAt,
        },
      });
      return { ok: true as const, id, auditId };
    });
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    return fail('internal_error', msg);
  }
}
