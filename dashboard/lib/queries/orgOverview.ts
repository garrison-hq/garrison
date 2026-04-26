import { sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';

// Queries powering the org-overview surface.
//
// All reads go through appDb (DASHBOARD_APP_DSN →
// garrison_dashboard_app). The vault tables are not touched here.
//
// Schema reference (from drizzle/schema.supervisor.ts, introspected
// from the M2-arc migrations):
//   tickets             id, department_id, objective, column_slug,
//                       created_at, acceptance_criteria, metadata,
//                       origin
//   ticket_transitions  id, ticket_id, from_column, to_column,
//                       triggered_by_agent_instance_id,
//                       triggered_by_user, at, hygiene_status
//   agents              id, department_id, role_slug, agent_md,
//                       model, skills, mcp_tools, listens_for,
//                       palace_wing, status, created_at, mcp_config
// "open" is encoded as column_slug != 'done'.

export interface OrgKPIs {
  openTickets: number;
  activeAgents: number;
  transitions24h: number;
  hygieneWarnings: number;
}

export interface DeptRow {
  slug: string;
  name: string;
  ticketCounts: Record<string, number>;
  agentCount: number;
  agentCap: number;
  lastTransitionAt: Date | null;
  hygieneWarnings: number;
}

export async function fetchOrgKPIs(): Promise<OrgKPIs> {
  const result = await appDb.execute<{
    open_tickets: number;
    active_agents: number;
    transitions_24h: number;
    hygiene_warnings: number;
  }>(sql`
    SELECT
      (SELECT count(*)::int FROM tickets WHERE column_slug <> 'done') AS open_tickets,
      (SELECT count(*)::int FROM agents WHERE status = 'active') AS active_agents,
      (SELECT count(*)::int FROM ticket_transitions
         WHERE at > now() - interval '24 hours') AS transitions_24h,
      (SELECT count(*)::int FROM ticket_transitions
         WHERE hygiene_status IS NOT NULL
           AND hygiene_status NOT IN ('clean', '')) AS hygiene_warnings
  `);
  const row = result[0];
  return {
    openTickets: Number(row.open_tickets ?? 0),
    activeAgents: Number(row.active_agents ?? 0),
    transitions24h: Number(row.transitions_24h ?? 0),
    hygieneWarnings: Number(row.hygiene_warnings ?? 0),
  };
}

export async function fetchDepartmentRows(): Promise<DeptRow[]> {
  const result = await appDb.execute<{
    slug: string;
    name: string;
    concurrency_cap: number;
    ticket_counts: Record<string, number> | null;
    agent_count: number;
    last_transition_at: Date | null;
    hygiene_warnings: number;
  }>(sql`
    SELECT
      d.slug,
      d.name,
      d.concurrency_cap,
      (SELECT jsonb_object_agg(t.column_slug, t.cnt) FROM (
         SELECT column_slug, count(*)::int AS cnt
           FROM tickets
          WHERE department_id = d.id AND column_slug <> 'done'
          GROUP BY column_slug
       ) t) AS ticket_counts,
      (SELECT count(*)::int FROM agents WHERE department_id = d.id AND status = 'active') AS agent_count,
      (SELECT max(tt.at) FROM ticket_transitions tt
         JOIN tickets t ON t.id = tt.ticket_id
         WHERE t.department_id = d.id) AS last_transition_at,
      (SELECT count(*)::int FROM ticket_transitions tt
         JOIN tickets t ON t.id = tt.ticket_id
        WHERE t.department_id = d.id
          AND tt.hygiene_status IS NOT NULL
          AND tt.hygiene_status NOT IN ('clean', '')) AS hygiene_warnings
    FROM departments d
    ORDER BY d.slug
  `);
  return result.map((row) => ({
    slug: row.slug,
    name: row.name,
    ticketCounts: row.ticket_counts ?? {},
    agentCount: Number(row.agent_count ?? 0),
    agentCap: row.concurrency_cap,
    lastTransitionAt: row.last_transition_at,
    hygieneWarnings: Number(row.hygiene_warnings ?? 0),
  }));
}
