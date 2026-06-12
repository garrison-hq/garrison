import { sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { getScheduledOriginForTickets, type ScheduledOrigin } from './scheduledTasks';

/** M10 / T015 — ingress provenance for the kanban TicketCard's ingress-origin
 *  chip (FR-702). Present only when the ticket was created via an external
 *  connector (origin = 'ingress'). The chip renders `gh: <connectorId>` and
 *  links to the external URL (GitHub issue / PR). */
export interface IngressOrigin {
  connectorId: string;
  externalUrl: string;
}

// Queries for the per-department Kanban surface.
//
// Per spec FR-040 → FR-043, the surface renders four columns of
// ticket cards. M3 is read-only — the query never touches anything
// in tickets / ticket_transitions / agents / agent_instances.

export interface DeptInfo {
  id: string;
  slug: string;
  name: string;
  columns: { slug: string; label: string }[];
}

export interface TicketCardRow {
  id: string;
  objective: string;
  columnSlug: string;
  createdAt: Date;
  assignedAgentRoleSlug: string | null;
  /** M6 / T017 — parent ticket id when this ticket was decomposed
   *  from a larger objective via the chat-driven create_ticket
   *  verb. Null for top-level tickets. The kanban TicketCard
   *  surfaces a small parent chip when this is non-null. */
  parentTicketId: string | null;
  /** M9 / T015 — originating scheduled task when this ticket was
   *  fired by the supervisor's tick loop (FR-201 / US1-AS2). Null
   *  (or absent) for operator-/chat-/agent-created tickets. The
   *  TicketCard surfaces a scheduled-origin chip linking to the
   *  task's detail page. Optional so pre-M9 callers and fixtures
   *  remain valid. */
  scheduledOrigin?: ScheduledOrigin | null;
  /** M10 / T015 — ingress provenance when this ticket was created by
   *  an external connector (origin = 'ingress', FR-702). Null for
   *  operator-/chat-/agent-/schedule-created tickets. The TicketCard
   *  renders `gh: <connectorId>` linking to the external URL.
   *  Optional so pre-M10 callers and fixtures remain valid. */
  ingressOrigin?: IngressOrigin | null;
}

export async function fetchDepartmentBySlug(slug: string): Promise<DeptInfo | null> {
  // postgres-js returns jsonb columns as raw text in some versions;
  // extract the columns array via the JSONB path operator and cast
  // to text so we can parse it on the JS side regardless of driver
  // behaviour.
  const rows = await appDb.execute<{
    id: string;
    slug: string;
    name: string;
    columns_json: string | null;
  }>(sql`
    SELECT id, slug, name, (workflow -> 'columns')::text AS columns_json
      FROM departments
     WHERE slug = ${slug}
     LIMIT 1
  `);
  const row = rows[0];
  if (!row) return null;
  const columns: { slug: string; label: string }[] = row.columns_json
    ? (JSON.parse(row.columns_json) as { slug: string; label: string }[])
    : [];
  return {
    id: row.id,
    slug: row.slug,
    name: row.name,
    columns: columns.map((c) => ({ slug: c.slug, label: c.label })),
  };
}

export async function fetchKanban(slug: string): Promise<TicketCardRow[]> {
  const rows = await appDb.execute<{
    id: string;
    objective: string;
    column_slug: string;
    created_at: Date;
    assigned_agent_role_slug: string | null;
    parent_ticket_id: string | null;
    origin: string;
    ingress_connector: string | null;
    external_url: string | null;
  }>(sql`
    SELECT
      t.id,
      t.objective,
      t.column_slug,
      t.created_at,
      t.parent_ticket_id,
      t.origin,
      t.metadata->>'ingress_connector' AS ingress_connector,
      t.metadata->>'external_url'      AS external_url,
      (SELECT ai.role_slug
         FROM agent_instances ai
        WHERE ai.ticket_id = t.id AND ai.status = 'running'
        ORDER BY ai.started_at DESC LIMIT 1) AS assigned_agent_role_slug
    FROM tickets t
    JOIN departments d ON d.id = t.department_id
    WHERE d.slug = ${slug}
    ORDER BY t.created_at DESC
  `);
  // M9 / T015 — scheduled-origin lookup for the TicketCard chip
  // (ticket → run → task name; one IN-list query for the board).
  const origins = await getScheduledOriginForTickets(rows.map((r) => r.id));
  return rows.map((r) => {
    // M10 / T015 — ingress origin chip: set only when origin='ingress' and
    // provenance keys are present in tickets.metadata (FR-702).
    const ingressOrigin: IngressOrigin | null =
      r.origin === 'ingress' && r.ingress_connector && r.external_url
        ? { connectorId: r.ingress_connector, externalUrl: r.external_url }
        : null;
    return {
      id: r.id,
      objective: r.objective,
      columnSlug: r.column_slug,
      createdAt: r.created_at,
      assignedAgentRoleSlug: r.assigned_agent_role_slug,
      parentTicketId: r.parent_ticket_id,
      scheduledOrigin: origins[r.id] ?? null,
      ingressOrigin,
    };
  });
}
