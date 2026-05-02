'use server';

// M4 ticket server actions per plan §"Concrete interfaces > Server
// action signatures > lib/actions/tickets.ts". T011 ships
// createTicket + moveTicket; T012 ships editTicket (inline edits).
//
// Schema reality vs spec: the M2.1 tickets table has the columns
// objective (imperative goal — used as the human-facing title),
// acceptanceCriteria (optional, multi-line), departmentId,
// columnSlug, metadata JSONB, origin. The M4 spec named "title"
// and "description" for the create-ticket form — those are
// rendered onto objective and acceptanceCriteria respectively.
// FR-031's "priority" + "assigned-agent" inline-edit fields don't
// exist on the schema; M4 inline edits operate on objective and
// acceptanceCriteria only. Future milestones may extend the
// tickets schema; this server action stays compatible with that.

import { eq, and } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { tickets, ticketTransitions, departments } from '@/drizzle/schema.supervisor';
import { getSession } from '@/lib/auth/session';
import { AuthError, AuthErrorKind } from '@/lib/auth/errors';
import { ConflictError, ConflictKind } from '@/lib/locks/conflict';
import {
  writeMutationEventToOutbox,
  type MutationTx,
} from '@/lib/audit/eventOutbox';
import { emitPgNotify } from '@/lib/audit/pgNotify';
import { buildFieldDiff } from '@/lib/audit/diff';

// ─── Types ────────────────────────────────────────────────────

export interface CreateTicketParams {
  /** Imperative description of what the ticket asks for. Mapped
   *  to tickets.objective. Required, 1-500 characters. */
  objective: string;
  /** Multi-line acceptance criteria; optional. Mapped to
   *  tickets.acceptance_criteria. */
  acceptanceCriteria?: string;
  deptSlug: string;
  /** Target column the ticket lands in. Defaults to the
   *  department's source column (typically 'todo'). */
  targetColumn?: string;
  /** M6 / T017 — optional parent ticket id for chat-driven
   *  decomposition. The /tickets/new form does NOT expose a
   *  parent-picker; this is API-only. Mirrors the supervisor-side
   *  garrison-mutate.create_ticket parent_ticket_id arg
   *  (T010). When set, the action validates same-department
   *  membership and rejects mismatches. */
  parentTicketId?: string;
}

export interface MoveTicketParams {
  ticketId: string;
  /** From-column the operator is moving FROM. Validated to
   *  match the ticket's current column_slug at write time;
   *  if the ticket has been moved by another writer, the
   *  WHERE clause matches zero rows and the move is rejected.
   *  This guards against racing the supervisor's finalize
   *  path (FR-043). */
  fromColumn: string;
  /** Target column. */
  toColumn: string;
}

export interface EditTicketParams {
  ticketId: string;
  /** Inline-edit field changes. Last-write-wins per FR-034 —
   *  no optimistic locking. */
  changes: Partial<{
    objective: string;
    acceptanceCriteria: string | null;
  }>;
}

// ─── Helpers ──────────────────────────────────────────────────

async function requireOperatorUserId(): Promise<string> {
  const session = await getSession();
  if (!session) throw new AuthError(AuthErrorKind.NoSession);
  return session.user.id;
}

async function resolveDeptId(deptSlug: string): Promise<string> {
  const rows = await appDb
    .select({ id: departments.id })
    .from(departments)
    .where(eq(departments.slug, deptSlug))
    .limit(1);
  if (rows.length === 0) {
    throw new ConflictError(ConflictKind.AlreadyExists, undefined, `unknown department slug: ${deptSlug}`);
  }
  return rows[0].id;
}

// ─── createTicket ─────────────────────────────────────────────

export async function createTicket(params: CreateTicketParams): Promise<{ ticketId: string }> {
  await requireOperatorUserId();

  if (!params.objective || params.objective.length === 0 || params.objective.length > 500) {
    throw new ConflictError(
      ConflictKind.AlreadyExists,
      undefined,
      'objective must be a non-empty string ≤ 500 characters',
    );
  }

  const deptId = await resolveDeptId(params.deptSlug);
  const targetColumn = params.targetColumn ?? 'todo';

  // M6 / T017 — parent-ticket validation mirrors the supervisor-side
  // garrison-mutate.create_ticket verb: read the parent's
  // department_id, reject if it differs from the child's. Cross-dept
  // decomposition is intentionally rejected — keep parent/child pairs
  // within a single department's workflow. Operator-driven
  // /tickets/new does NOT pass parentTicketId; this branch only fires
  // on programmatic callers.
  if (params.parentTicketId !== undefined) {
    const parentRows = await appDb
      .select({ deptId: tickets.departmentId })
      .from(tickets)
      .where(eq(tickets.id, params.parentTicketId))
      .limit(1);
    if (parentRows.length === 0) {
      throw new ConflictError(
        ConflictKind.AlreadyExists,
        undefined,
        'parent_ticket_id refers to a ticket that does not exist',
      );
    }
    if (parentRows[0].deptId !== deptId) {
      throw new ConflictError(
        ConflictKind.AlreadyExists,
        undefined,
        'parent_ticket_id is in a different department',
      );
    }
  }

  const ticketId = await appDb.transaction(async (tx) => {
    const tx2 = tx as unknown as MutationTx;

    const inserted = await tx
      .insert(tickets)
      .values({
        departmentId: deptId,
        objective: params.objective,
        acceptanceCriteria: params.acceptanceCriteria ?? null,
        columnSlug: targetColumn,
        origin: 'operator',
        parentTicketId: params.parentTicketId ?? null,
      })
      .returning({ id: tickets.id });
    const ticketId = inserted[0].id;

    const outbox = await writeMutationEventToOutbox(tx2, {
      kind: 'ticket.created',
      ticketId,
      deptSlug: params.deptSlug,
      targetColumn,
      parentTicketId: params.parentTicketId ?? null,
    });

    await emitPgNotify(tx2, 'work.ticket.created', outbox.id);

    return ticketId;
  });

  return { ticketId };
}

// ─── moveTicket (drag-to-move; T011) ──────────────────────────

export interface MoveTicketResult {
  transitionId: string;
}

export async function moveTicket(params: MoveTicketParams): Promise<MoveTicketResult> {
  await requireOperatorUserId();

  if (params.fromColumn === params.toColumn) {
    throw new ConflictError(
      ConflictKind.AlreadyExists,
      undefined,
      'no-op move (fromColumn === toColumn) — caller should not invoke moveTicket per FR-036',
    );
  }

  // Look up department slug for the channel name.
  const ticketRow = await appDb
    .select({
      ticketId: tickets.id,
      currentColumn: tickets.columnSlug,
      deptId: tickets.departmentId,
    })
    .from(tickets)
    .where(eq(tickets.id, params.ticketId))
    .limit(1);
  if (ticketRow.length === 0) {
    throw new ConflictError(ConflictKind.AlreadyExists, undefined, 'ticket not found');
  }
  if (ticketRow[0].currentColumn !== params.fromColumn) {
    // Another writer already moved this ticket. Surface as a
    // stale-version conflict — the operator's UI snaps the
    // card back per FR-042 / plan §"Drag-to-move ticket
    // transition lifecycle".
    throw new ConflictError(
      ConflictKind.StaleVersion,
      { currentColumn: ticketRow[0].currentColumn },
      'ticket has been moved by another writer; current column does not match fromColumn',
    );
  }

  const deptRows = await appDb
    .select({ slug: departments.slug })
    .from(departments)
    .where(eq(departments.id, ticketRow[0].deptId))
    .limit(1);
  const deptSlug = deptRows[0]?.slug ?? 'unknown';

  const result = await appDb.transaction(async (tx) => {
    const tx2 = tx as unknown as MutationTx;

    // Re-check current_column inside the transaction with row
    // locking to prevent a finalize-vs-drag race after the
    // initial select but before our update (FR-043).
    const updateResult = await tx
      .update(tickets)
      .set({ columnSlug: params.toColumn })
      .where(
        and(
          eq(tickets.id, params.ticketId),
          eq(tickets.columnSlug, params.fromColumn),
        ),
      )
      .returning({ id: tickets.id });

    if (updateResult.length === 0) {
      throw new ConflictError(
        ConflictKind.StaleVersion,
        undefined,
        'ticket column changed between read and write; drag rejected',
      );
    }

    // Insert ticket_transitions row with operator_initiated
    // hygiene_status (FR-027). agent_instance_id is null
    // because no agent triggered this transition.
    const inserted = await tx
      .insert(ticketTransitions)
      .values({
        ticketId: params.ticketId,
        fromColumn: params.fromColumn,
        toColumn: params.toColumn,
        triggeredByUser: true,
        hygieneStatus: 'operator_initiated',
      })
      .returning({ id: ticketTransitions.id });
    const transitionId = inserted[0].id;

    // Audit row in event_outbox. Per FR-029 (clarified), the
    // pg_notify channel matches the agent finalize path —
    // work.ticket.transitioned.<dept>.<from>.<to> — so agents
    // listening on the channel spawn on operator drags as
    // they would on agent-driven transitions.
    const outbox = await writeMutationEventToOutbox(
      tx2,
      {
        kind: 'ticket.moved',
        ticketId: params.ticketId,
        fromColumn: params.fromColumn,
        toColumn: params.toColumn,
        transitionId,
        origin: 'operator',
      },
      deptSlug,
    );

    await emitPgNotify(
      tx2,
      `work.ticket.transitioned.${deptSlug}.${params.fromColumn}.${params.toColumn}`,
      outbox.id,
    );

    return { transitionId };
  });

  return result;
}

// ─── editTicket (inline edits; T012) ──────────────────────────

export type EditTicketResult = { accepted: true } | { accepted: false; reason: string };

export async function editTicket(params: EditTicketParams): Promise<EditTicketResult> {
  await requireOperatorUserId();

  const { changes } = params;
  const editableFields: Array<keyof typeof changes> = ['objective', 'acceptanceCriteria'];
  const filteredChanges: Record<string, unknown> = {};
  for (const f of editableFields) {
    if (changes[f] !== undefined) {
      filteredChanges[f] = changes[f];
    }
  }
  if (Object.keys(filteredChanges).length === 0) {
    return { accepted: false, reason: 'no editable fields supplied' };
  }
  if (filteredChanges.objective !== undefined) {
    const obj = filteredChanges.objective;
    if (typeof obj !== 'string' || obj.length === 0 || obj.length > 500) {
      throw new ConflictError(
        ConflictKind.AlreadyExists,
        undefined,
        'objective must be a non-empty string ≤ 500 characters',
      );
    }
  }

  // Read the current row for the diff before/after pair.
  const before = await appDb
    .select({
      objective: tickets.objective,
      acceptanceCriteria: tickets.acceptanceCriteria,
    })
    .from(tickets)
    .where(eq(tickets.id, params.ticketId))
    .limit(1);
  if (before.length === 0) {
    throw new ConflictError(ConflictKind.AlreadyExists, undefined, 'ticket not found');
  }

  const after = { ...before[0], ...filteredChanges };
  const diff = buildFieldDiff(
    before[0] as Record<string, unknown>,
    after as Record<string, unknown>,
    editableFields,
  );
  if (Object.keys(diff).length === 0) {
    return { accepted: false, reason: 'no fields actually changed' };
  }

  await appDb.transaction(async (tx) => {
    const tx2 = tx as unknown as MutationTx;

    await tx
      .update(tickets)
      .set(filteredChanges)
      .where(eq(tickets.id, params.ticketId));

    const outbox = await writeMutationEventToOutbox(tx2, {
      kind: 'ticket.edited',
      ticketId: params.ticketId,
      diff,
    });
    await emitPgNotify(tx2, 'work.ticket.edited', outbox.id);
  });

  return { accepted: true };
}
