import { sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';

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
  }>(sql`
    SELECT
      t.id,
      t.objective,
      t.column_slug,
      t.created_at,
      (SELECT ai.role_slug
         FROM agent_instances ai
        WHERE ai.ticket_id = t.id AND ai.status = 'running'
        ORDER BY ai.started_at DESC LIMIT 1) AS assigned_agent_role_slug
    FROM tickets t
    JOIN departments d ON d.id = t.department_id
    WHERE d.slug = ${slug}
    ORDER BY t.created_at DESC
  `);
  return rows.map((r) => ({
    id: r.id,
    objective: r.objective,
    columnSlug: r.column_slug,
    createdAt: r.created_at,
    assignedAgentRoleSlug: r.assigned_agent_role_slug,
  }));
}
