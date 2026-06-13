'use server';

// M11 Outbox Server Actions (plan D14 / T012, spec FR-026 / FR-027).
//
// Three operator actions on pending_actions rows:
//   approveAction  — status 'pending' → 'approved'; emits
//                    pg_notify('work.action.dispatch_requested', id)
//                    so the dispatcher picks it up reactively (D18).
//   rejectAction   — status 'pending' → 'rejected'; no dispatch.
//   markActionDone — status 'pending' → 'done' with optional free-text
//                    note; for human_only rows the dispatcher never
//                    executed this action (FR-027 / US5 #2).
//
// Audit discipline (FR-026 / spec §Server Actions):
//   - chat anchors NULL (Server-Action-only, both chat and agent paths
//     NULL — the M7 approve_hire / M9 ServerActionVerbs precedent).
//   - affected_resource_type = 'pending_action'.
//   - reversibility_class = 1 (operator actions; transitions are
//     immutable history but are low-blast-radius operator decisions).
//   - outcome rows are appended to pending_action_outcomes (append-only
//     immutable history per FR-024 / M9 scheduled_task_runs shape).
//
// Exactly-once guard: the UPDATE WHERE clause includes a status check
// ('pending') so a double-click or replayed request on an already-
// transitioned row is a no-op at the DB level.
//
// Per AGENTS.md "Tests for Go only, never frontend": no vitest here;
// the Go-side integration suite (T011) pins the row shapes these write.

import { eq, and, sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import {
  pendingActions,
  pendingActionOutcomes,
  chatMutationAudit,
} from '@/drizzle/schema.supervisor';
import { getSession } from '@/lib/auth/session';

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type OutboxActionResult =
  | { ok: true; id: string }
  | {
      ok: false;
      errorKind: 'unauthorized' | 'not_found' | 'not_pending' | 'internal_error';
      message: string;
    };

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

async function requireSession(): Promise<{ operatorEmail: string } | null> {
  const session = await getSession();
  if (!session) return null;
  return { operatorEmail: session.user.email };
}

type Tx = Parameters<Parameters<typeof appDb.transaction>[0]>[0];

/** Read the pending_actions row inside a tx and assert it is at
 *  status='pending'. Returns the row or a typed failure. */
async function readPendingAction(
  tx: Tx,
  id: string,
): Promise<
  | { ok: true; row: typeof pendingActions.$inferSelect }
  | { ok: false; result: OutboxActionResult }
> {
  const rows = await tx
    .select()
    .from(pendingActions)
    .where(eq(pendingActions.id, id))
    .limit(1);
  if (rows.length === 0) {
    return {
      ok: false,
      result: { ok: false, errorKind: 'not_found', message: `pending action ${id} not found` },
    };
  }
  const row = rows[0];
  if (row.status !== 'pending') {
    return {
      ok: false,
      result: {
        ok: false,
        errorKind: 'not_pending',
        message: `pending action is in status ${row.status}; only pending actions can transition`,
      },
    };
  }
  return { ok: true, row };
}

/** Insert one pending_action_outcomes row inside a tx. */
async function insertOutcome(
  tx: Tx,
  opts: {
    pendingActionId: string;
    agentInstanceId: string;
    outcome: string;
    detail?: string | null;
    structuredOutcome?: Record<string, unknown> | null;
  },
): Promise<void> {
  await tx.insert(pendingActionOutcomes).values({
    pendingActionId: opts.pendingActionId,
    agentInstanceId: opts.agentInstanceId,
    outcome: opts.outcome,
    detail: opts.detail ?? null,
    structuredOutcome: opts.structuredOutcome ?? null,
  });
}

/** Insert one chat_mutation_audit row inside a tx (both chat anchors
 *  NULL — Server-Action-only path, the M7/M9 ServerActionVerbs
 *  precedent). */
async function insertAudit(
  tx: Tx,
  opts: {
    verb: 'approve_action' | 'reject_action' | 'mark_action_done';
    pendingActionId: string;
    argsJsonb: Record<string, unknown>;
  },
): Promise<void> {
  await tx.insert(chatMutationAudit).values({
    chatSessionId: null,
    chatMessageId: null,
    verb: opts.verb,
    argsJsonb: opts.argsJsonb,
    outcome: 'success',
    reversibilityClass: 1,
    affectedResourceId: opts.pendingActionId,
    affectedResourceType: 'pending_action',
  });
}

/** The shared pending → terminal transition all three operator actions
 *  run: read + assert status='pending' inside a tx, UPDATE with the
 *  status re-check in the WHERE clause (the exactly-once guard), append
 *  the outcome row (FR-024), write the audit row (FR-026), and
 *  optionally emit pg_notify so the dispatcher claims the row
 *  reactively (D18). */
async function transitionPendingAction(
  id: string,
  operatorEmail: string,
  spec: {
    verb: 'approve_action' | 'reject_action' | 'mark_action_done';
    set: Partial<typeof pendingActions.$inferInsert>;
    outcome: string;
    outcomeDetail: string;
    extraAuditArgs?: Record<string, unknown>;
    notifyDispatcher?: boolean;
  },
): Promise<OutboxActionResult> {
  try {
    return await appDb.transaction(async (tx) => {
      const read = await readPendingAction(tx, id);
      if (!read.ok) return read.result;
      const { row } = read;

      await tx
        .update(pendingActions)
        .set(spec.set)
        .where(
          and(eq(pendingActions.id, id), eq(pendingActions.status, 'pending')),
        );

      await insertOutcome(tx, {
        pendingActionId: id,
        agentInstanceId: row.agentInstanceId,
        outcome: spec.outcome,
        detail: spec.outcomeDetail,
      });

      await insertAudit(tx, {
        verb: spec.verb,
        pendingActionId: id,
        argsJsonb: {
          pending_action_id: id,
          action_type: row.actionType,
          tier: row.tier,
          operator: operatorEmail,
          ...(spec.extraAuditArgs ?? {}),
        },
      });

      if (spec.notifyDispatcher) {
        // Payload = the pending_actions.id UUID string (D18 /
        // actionbroker.Channel = 'work.action.dispatch_requested').
        await tx.execute(
          sql`SELECT pg_notify('work.action.dispatch_requested', ${id})`,
        );
      }

      return { ok: true as const, id };
    });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return { ok: false, errorKind: 'internal_error', message };
  }
}

// ---------------------------------------------------------------------------
// approveAction
// ---------------------------------------------------------------------------

/**
 * Approve a pending approve-tier action. Transitions status to
 * 'approved', records the approving operator, writes an 'approved'
 * outcome and an audit row, then emits pg_notify so the dispatcher
 * can claim the row reactively (D18 — payload is the UUID string,
 * matching the actionbroker.Channel handler signature).
 *
 * FR-026 / US2 #1.
 */
export async function approveAction(id: string): Promise<OutboxActionResult> {
  const operator = await requireSession();
  if (!operator) {
    return { ok: false, errorKind: 'unauthorized', message: 'must be authenticated to approve actions' };
  }

  return transitionPendingAction(id, operator.operatorEmail, {
    verb: 'approve_action',
    set: { status: 'approved', approvedBy: operator.operatorEmail },
    outcome: 'approved',
    outcomeDetail: `approved by ${operator.operatorEmail}`,
    notifyDispatcher: true,
  });
}

// ---------------------------------------------------------------------------
// rejectAction
// ---------------------------------------------------------------------------

/**
 * Reject a pending approve-tier action. Transitions status to
 * 'rejected', writes a 'rejected' outcome and an audit row.
 * The dispatcher's claim query filters on status IN ('pending','approved'),
 * so a 'rejected' row is never claimed (FR-026 / US5 #1).
 */
export async function rejectAction(id: string): Promise<OutboxActionResult> {
  const operator = await requireSession();
  if (!operator) {
    return { ok: false, errorKind: 'unauthorized', message: 'must be authenticated to reject actions' };
  }

  return transitionPendingAction(id, operator.operatorEmail, {
    verb: 'reject_action',
    set: { status: 'rejected' },
    outcome: 'rejected',
    outcomeDetail: `rejected by ${operator.operatorEmail}`,
  });
}

// ---------------------------------------------------------------------------
// markActionDone
// ---------------------------------------------------------------------------

/**
 * Mark a human_only pending action as done by the operator. Records a
 * 'done' completion outcome with an optional free-text note of what was
 * actually performed, and transitions status to 'done'.
 *
 * The dispatcher never executes human_only actions (FR-017 / US4 #4);
 * this is the only path by which they reach a terminal state.
 * FR-027 / US5 #2.
 */
export async function markActionDone(
  id: string,
  note?: string,
): Promise<OutboxActionResult> {
  const operator = await requireSession();
  if (!operator) {
    return { ok: false, errorKind: 'unauthorized', message: 'must be authenticated to mark actions done' };
  }

  const detail = note?.trim() ?? null;

  return transitionPendingAction(id, operator.operatorEmail, {
    verb: 'mark_action_done',
    set: { status: 'done' },
    outcome: 'done',
    outcomeDetail: detail
      ? `done by ${operator.operatorEmail}: ${detail}`
      : `done by ${operator.operatorEmail}`,
    extraAuditArgs: { note: detail ?? null },
  });
}
