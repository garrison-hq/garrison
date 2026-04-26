import { sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';

// Agents registry queries (FR-090 → FR-092). Per-agent stats are
// computed from agent_instances at request time; M3 doesn't add a
// denorm column for them. Refresh cadence is 60s soft-poll.

export interface AgentRow {
  id: string;
  departmentSlug: string;
  roleSlug: string;
  model: string;
  concurrencyCap: number;
  listensFor: unknown;
  lastSpawnedAt: Date | null;
  spawnsThisWeek: number;
}

export async function fetchAgents(): Promise<AgentRow[]> {
  const rows = await appDb.execute<{
    id: string;
    department_slug: string;
    role_slug: string;
    model: string;
    concurrency_cap: number;
    listens_for: unknown;
    last_spawned_at: Date | null;
    spawns_this_week: number;
  }>(sql`
    SELECT
      a.id,
      d.slug AS department_slug,
      a.role_slug,
      a.model,
      d.concurrency_cap,
      a.listens_for,
      (SELECT max(ai.started_at) FROM agent_instances ai
        WHERE ai.department_id = a.department_id
          AND ai.role_slug = a.role_slug) AS last_spawned_at,
      (SELECT count(*)::int FROM agent_instances ai
        WHERE ai.department_id = a.department_id
          AND ai.role_slug = a.role_slug
          AND ai.started_at > now() - interval '7 days') AS spawns_this_week
    FROM agents a
    JOIN departments d ON d.id = a.department_id
    ORDER BY d.slug, a.role_slug
  `);
  return rows.map((r) => ({
    id: r.id,
    departmentSlug: r.department_slug,
    roleSlug: r.role_slug,
    model: r.model,
    concurrencyCap: r.concurrency_cap,
    listensFor: r.listens_for,
    lastSpawnedAt: r.last_spawned_at,
    spawnsThisWeek: Number(r.spawns_this_week ?? 0),
  }));
}
