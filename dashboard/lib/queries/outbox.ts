// M11 Outbox dashboard read-side queries (plan D16 / T012).
//
// Dashboard-side drizzle queries ONLY — not sqlc. The Go-side sqlc
// queries in store/m11_action_broker.sql.go are the supervisor /
// dispatcher path; the dashboard reads directly via drizzle following
// the M9/M10 pattern (scheduledTasks.ts, connectors page).
//
// Per AGENTS.md "Tests for Go only, never frontend": ships without
// vitest coverage; the Go-side integration suite (T011) pins the row
// shapes these queries read.

import { eq, and, desc, inArray, sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import {
  pendingActions,
  pendingActionOutcomes,
  agentInstances,
  tickets,
} from '@/drizzle/schema.supervisor';

// ---------------------------------------------------------------------------
// Shared types
// ---------------------------------------------------------------------------

export type PendingActionStatus =
  | 'pending'
  | 'approved'
  | 'rejected'
  | 'executed'
  | 'failed'
  | 'done';

export type PendingActionTier = 'auto' | 'notify' | 'approve' | 'human_only';

export type PendingActionOutcomeKind =
  | 'requested'
  | 'approved'
  | 'rejected'
  | 'executed'
  | 'failed'
  | 'notified'
  | 'done'
  | 'skipped_human_only';

export interface PendingActionRow {
  id: string;
  actionType: string;
  /** Raw JSONB — caller renders as needed (e.g. owner/repo#issue). */
  target: unknown;
  renderedPayload: string;
  agentInstanceId: string;
  ticketId: string | null;
  tier: PendingActionTier;
  tierReason: string;
  status: PendingActionStatus;
  approvedBy: string | null;
  dispatchedAt: string | null;
  createdAt: string;
}

export interface PendingActionOutcomeRow {
  id: string;
  pendingActionId: string;
  agentInstanceId: string;
  outcome: PendingActionOutcomeKind;
  detail: string | null;
  structuredOutcome: unknown;
  createdAt: string;
}

// ---------------------------------------------------------------------------
// listPendingApproveActions — approve-tier rows at status 'pending'
// (the operator's Outbox approval queue, FR-025 / US1 #3).
// Newest first so newly-queued actions surface at the top.
// ---------------------------------------------------------------------------
export async function listPendingApproveActions(
  limit = 200,
): Promise<PendingActionRow[]> {
  const rows = await appDb
    .select()
    .from(pendingActions)
    .where(
      and(
        eq(pendingActions.tier, 'approve'),
        eq(pendingActions.status, 'pending'),
      ),
    )
    .orderBy(desc(pendingActions.createdAt))
    .limit(limit);
  return rows.map(toPendingActionRow);
}

// ---------------------------------------------------------------------------
// listHumanOnlyPreparedActions — human_only rows at status 'pending'
// (the agent has prepared the payload; the operator performs it by
// hand and marks it done — FR-027 / US5 #2).
// ---------------------------------------------------------------------------
export async function listHumanOnlyPreparedActions(
  limit = 200,
): Promise<PendingActionRow[]> {
  const rows = await appDb
    .select()
    .from(pendingActions)
    .where(
      and(
        eq(pendingActions.tier, 'human_only'),
        eq(pendingActions.status, 'pending'),
      ),
    )
    .orderBy(desc(pendingActions.createdAt))
    .limit(limit);
  return rows.map(toPendingActionRow);
}

// ---------------------------------------------------------------------------
// listNotifyPostHocFeedItems — notify-tier rows whose outcome history
// includes a 'notified' outcome (executed, then operator-told — FR-028,
// US4 #2). A notify row surfaces post-hoc, not as an approval gate.
// Selects pending_actions rows whose id appears in pending_action_outcomes
// with outcome='notified', limiting to the most recent 100.
// ---------------------------------------------------------------------------
export async function listNotifyPostHocFeedItems(
  limit = 100,
): Promise<PendingActionRow[]> {
  // Subquery: distinct pending_action_ids that have a 'notified' outcome.
  const notifiedIds = await appDb
    .selectDistinct({ pendingActionId: pendingActionOutcomes.pendingActionId })
    .from(pendingActionOutcomes)
    .where(eq(pendingActionOutcomes.outcome, 'notified'));

  if (notifiedIds.length === 0) return [];

  const ids = notifiedIds.map((r) => r.pendingActionId);
  const rows = await appDb
    .select()
    .from(pendingActions)
    .where(
      and(
        eq(pendingActions.tier, 'notify'),
        inArray(pendingActions.id, ids),
      ),
    )
    .orderBy(desc(pendingActions.createdAt))
    .limit(limit);
  return rows.map(toPendingActionRow);
}

// ---------------------------------------------------------------------------
// listPendingActionOutcomes — immutable outcome history for a single
// pending action, oldest first (SC-007 audit reconstruction).
// ---------------------------------------------------------------------------
export async function listPendingActionOutcomes(
  pendingActionId: string,
): Promise<PendingActionOutcomeRow[]> {
  const rows = await appDb
    .select()
    .from(pendingActionOutcomes)
    .where(eq(pendingActionOutcomes.pendingActionId, pendingActionId))
    .orderBy(pendingActionOutcomes.createdAt);
  return rows.map(toPendingActionOutcomeRow);
}

// ---------------------------------------------------------------------------
// getPendingActionById — single pending action row, or null.
// Used for the page to read the row details after a status transition.
// ---------------------------------------------------------------------------
export async function getPendingActionById(
  id: string,
): Promise<PendingActionRow | null> {
  const rows = await appDb
    .select()
    .from(pendingActions)
    .where(eq(pendingActions.id, id))
    .limit(1);
  if (rows.length === 0) return null;
  return toPendingActionRow(rows[0]);
}

// ---------------------------------------------------------------------------
// Helper: resolve agent_instance_id to a short display identifier.
// Joins to agent_instances to get the role_slug for human-readable
// agent identification in the Outbox (US1 #3 — "requesting agent").
// Returns a truncated UUID fallback when no join is available.
// ---------------------------------------------------------------------------
export interface AgentSummary {
  agentInstanceId: string;
  roleSlug: string | null;
}

export async function getAgentSummariesForActions(
  agentInstanceIds: string[],
): Promise<Map<string, AgentSummary>> {
  if (agentInstanceIds.length === 0) return new Map();
  const rows = await appDb
    .select({
      id: agentInstances.id,
      roleSlug: agentInstances.roleSlug,
    })
    .from(agentInstances)
    .where(inArray(agentInstances.id, agentInstanceIds));
  const map = new Map<string, AgentSummary>();
  for (const r of rows) {
    map.set(r.id, { agentInstanceId: r.id, roleSlug: r.roleSlug ?? null });
  }
  return map;
}

// ---------------------------------------------------------------------------
// Helper: resolve ticket_id to ticket objective summary.
// ---------------------------------------------------------------------------
export interface TicketSummary {
  ticketId: string;
  objective: string;
}

export async function getTicketSummariesForActions(
  ticketIds: string[],
): Promise<Map<string, TicketSummary>> {
  const validIds = ticketIds.filter(Boolean);
  if (validIds.length === 0) return new Map();
  const rows = await appDb
    .select({ id: tickets.id, objective: tickets.objective })
    .from(tickets)
    .where(inArray(tickets.id, validIds));
  const map = new Map<string, TicketSummary>();
  for (const r of rows) {
    map.set(r.id, { ticketId: r.id, objective: r.objective });
  }
  return map;
}

// ---------------------------------------------------------------------------
// Row mappers — narrow drizzle's inferred types to the exported shapes.
// ---------------------------------------------------------------------------

function toPendingActionRow(
  r: typeof pendingActions.$inferSelect,
): PendingActionRow {
  return {
    id: r.id,
    actionType: r.actionType,
    target: r.target,
    renderedPayload: r.renderedPayload,
    agentInstanceId: r.agentInstanceId,
    ticketId: r.ticketId ?? null,
    tier: r.tier as PendingActionTier,
    tierReason: r.tierReason,
    status: r.status as PendingActionStatus,
    approvedBy: r.approvedBy ?? null,
    dispatchedAt: r.dispatchedAt ?? null,
    createdAt: r.createdAt,
  };
}

function toPendingActionOutcomeRow(
  r: typeof pendingActionOutcomes.$inferSelect,
): PendingActionOutcomeRow {
  return {
    id: r.id,
    pendingActionId: r.pendingActionId,
    agentInstanceId: r.agentInstanceId,
    outcome: r.outcome as PendingActionOutcomeKind,
    detail: r.detail ?? null,
    structuredOutcome: r.structuredOutcome,
    createdAt: r.createdAt,
  };
}

// ---------------------------------------------------------------------------
// renderGitHubTarget — converts a github_issue_comment target JSONB to
// a human-readable "<owner>/<repo>#<issue_number>" string.
// Falls back to the raw JSON if the shape is unexpected.
// ---------------------------------------------------------------------------
export function renderGitHubTarget(target: unknown): string {
  if (
    target !== null &&
    typeof target === 'object' &&
    !Array.isArray(target)
  ) {
    const t = target as Record<string, unknown>;
    const owner = typeof t.owner === 'string' ? t.owner : null;
    const repo = typeof t.repo === 'string' ? t.repo : null;
    const issue = typeof t.issue_number === 'number' ? t.issue_number : null;
    if (owner && repo && issue !== null) {
      return `${owner}/${repo}#${issue}`;
    }
    if (owner && repo) {
      return `${owner}/${repo}`;
    }
  }
  try {
    return JSON.stringify(target);
  } catch {
    return String(target);
  }
}

// ---------------------------------------------------------------------------
// Outbox count — pending approve actions awaiting decision (for badge).
// ---------------------------------------------------------------------------
export async function countPendingApproveActions(): Promise<number> {
  const result = await appDb
    .select({ count: sql<number>`count(*)::int` })
    .from(pendingActions)
    .where(
      and(
        eq(pendingActions.tier, 'approve'),
        eq(pendingActions.status, 'pending'),
      ),
    );
  return result[0]?.count ?? 0;
}
