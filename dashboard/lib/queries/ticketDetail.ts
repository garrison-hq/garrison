import { sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';

// Queries for the ticket-detail surface (FR-050 → FR-055).
//
// One round-trip per block: metadata (1 row from tickets), history
// (n rows from ticket_transitions), and agent-instances (n rows
// from agent_instances). Could be folded into a single query with
// jsonb aggregation but the readability tradeoff isn't worth it
// for ≤50 transitions per ticket (spec implies that's the cap).
//
// Palace links (diary entry + KG triples) are NOT queried here —
// per A-004, M3 doesn't query MemPalace directly. The ticket-detail
// surface renders a placeholder block referring the operator to
// MemPalace; M5+ wires the read references.

export interface TicketDetailMetadata {
  id: string;
  departmentSlug: string;
  departmentName: string;
  objective: string;
  columnSlug: string;
  createdAt: Date;
  acceptanceCriteria: string | null;
  origin: string;
}

export interface TransitionRow {
  id: string;
  fromColumn: string | null;
  toColumn: string;
  hygieneStatus: string | null;
  triggeredByAgentInstanceId: string | null;
  at: Date;
  /** Surface flag: derived from hygieneStatus naming convention. */
  sandboxEscape: boolean;
  /** Optional artifact-claimed-vs-on-disk evidence pulled from
   *  ticket_transitions metadata when sandboxEscape is true.
   *  M3 reads it from ticket_transitions hygiene_status as a typed
   *  flag; the artifact paths live alongside as evidence — for
   *  shipped data they may be encoded in transition metadata
   *  added in a later migration. For now we surface a placeholder
   *  pair so the UI primitive is exercised. */
  sandboxEscapeDetail: { claimedPath: string; onDiskPath: string } | null;
}

export interface AgentInstanceRow {
  id: string;
  roleSlug: string;
  startedAt: Date;
  finishedAt: Date | null;
  status: string;
  exitReason: string | null;
  totalCostUsd: number | null;
  /** True when the cost-blind-spot trigger predicate matches:
   *  exit_reason = 'finalize_committed' AND total_cost_usd = 0.
   *  See FR-053 + docs/issues/cost-telemetry-blind-spot.md. */
  costBlindSpot: boolean;
}

export interface TicketDetail {
  metadata: TicketDetailMetadata;
  history: TransitionRow[];
  instances: AgentInstanceRow[];
}

const SANDBOX_ESCAPE_STATUSES = new Set([
  'sandbox_escape',
  'artifact_claimed_vs_on_disk',
]);

export async function fetchTicketDetail(ticketId: string): Promise<TicketDetail | null> {
  const metaRows = await appDb.execute<{
    id: string;
    objective: string;
    column_slug: string;
    created_at: Date;
    acceptance_criteria: string | null;
    origin: string;
    department_slug: string;
    department_name: string;
  }>(sql`
    SELECT
      t.id,
      t.objective,
      t.column_slug,
      t.created_at,
      t.acceptance_criteria,
      t.origin,
      d.slug AS department_slug,
      d.name AS department_name
    FROM tickets t
    JOIN departments d ON d.id = t.department_id
    WHERE t.id = ${ticketId}
    LIMIT 1
  `);
  if (metaRows.length === 0) return null;

  const historyRows = await appDb.execute<{
    id: string;
    from_column: string | null;
    to_column: string;
    hygiene_status: string | null;
    triggered_by_agent_instance_id: string | null;
    at: Date;
  }>(sql`
    SELECT id, from_column, to_column, hygiene_status, triggered_by_agent_instance_id, at
      FROM ticket_transitions
     WHERE ticket_id = ${ticketId}
     ORDER BY at ASC
  `);

  const instanceRows = await appDb.execute<{
    id: string;
    role_slug: string;
    started_at: Date;
    finished_at: Date | null;
    status: string;
    exit_reason: string | null;
    total_cost_usd: string | null;
  }>(sql`
    SELECT id, role_slug, started_at, finished_at, status, exit_reason, total_cost_usd
      FROM agent_instances
     WHERE ticket_id = ${ticketId}
     ORDER BY started_at ASC
  `);

  const meta = metaRows[0];
  const history: TransitionRow[] = historyRows.map((r) => {
    const sandboxEscape = r.hygiene_status
      ? SANDBOX_ESCAPE_STATUSES.has(r.hygiene_status)
      : false;
    return {
      id: r.id,
      fromColumn: r.from_column,
      toColumn: r.to_column,
      hygieneStatus: r.hygiene_status,
      triggeredByAgentInstanceId: r.triggered_by_agent_instance_id,
      at: r.at,
      sandboxEscape,
      sandboxEscapeDetail: sandboxEscape
        ? { claimedPath: '<not yet recorded>', onDiskPath: '<not yet recorded>' }
        : null,
    };
  });
  const instances: AgentInstanceRow[] = instanceRows.map((r) => {
    const cost = r.total_cost_usd === null ? null : Number(r.total_cost_usd);
    const costBlindSpot = r.exit_reason === 'finalize_committed' && cost === 0;
    return {
      id: r.id,
      roleSlug: r.role_slug,
      startedAt: r.started_at,
      finishedAt: r.finished_at,
      status: r.status,
      exitReason: r.exit_reason,
      totalCostUsd: cost,
      costBlindSpot,
    };
  });

  return {
    metadata: {
      id: meta.id,
      departmentSlug: meta.department_slug,
      departmentName: meta.department_name,
      objective: meta.objective,
      columnSlug: meta.column_slug,
      createdAt: meta.created_at,
      acceptanceCriteria: meta.acceptance_criteria,
      origin: meta.origin,
    },
    history,
    instances,
  };
}
