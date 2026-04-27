import { sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';

// Agents registry queries (FR-090 → FR-092). Per-agent stats are
// computed from agent_instances at request time; M3 doesn't add a
// denorm column for them. Refresh cadence is 60s soft-poll.

export interface AgentRow {
  id: string;
  departmentSlug: string;
  departmentName: string;
  roleSlug: string;
  model: string;
  concurrencyCap: number;
  listensFor: string[];
  skills: string[];
  lastSpawnedAt: Date | null;
  spawnsThisWeek: number;
  /** Currently-running agent_instances for this (dept, role). */
  liveInstances: number;
  /** 7-element array, count of spawns per day for the last 7 days
   *  (oldest first → newest last). Drives the inline sparkline. */
  spawnsByDay: number[];
}

function asStringArray(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value.filter((v): v is string => typeof v === 'string');
  }
  if (typeof value === 'string' && value.length > 0) return [value];
  return [];
}

// M4 / T013 — single-agent fetch for the agent settings editor.
// Returns the editable fields (agentMd, model, listensFor, skills) +
// updatedAt for the optimistic-lock version token + current
// running-instance count for the banner copy. Looked up by
// (departmentSlug, roleSlug) since the M2.1 schema scopes
// role_slug uniqueness per department.

export interface AgentEditSnapshot {
  id: string;
  departmentSlug: string;
  departmentName: string;
  roleSlug: string;
  agentMd: string;
  model: string;
  listensFor: string[];
  skills: string[];
  updatedAt: string;
  liveInstances: number;
}

export async function fetchAgentForEdit(
  departmentSlug: string,
  roleSlug: string,
): Promise<AgentEditSnapshot | null> {
  const rows = await appDb.execute<{
    id: string;
    department_slug: string;
    department_name: string;
    role_slug: string;
    agent_md: string;
    model: string;
    listens_for: unknown;
    skills: unknown;
    updated_at: string;
    live_instances: number;
  }>(sql`
    SELECT
      a.id,
      d.slug AS department_slug,
      d.name AS department_name,
      a.role_slug,
      a.agent_md,
      a.model,
      a.listens_for,
      a.skills,
      a.updated_at,
      (SELECT count(*)::int FROM agent_instances ai
        WHERE ai.department_id = a.department_id
          AND ai.role_slug = a.role_slug
          AND ai.status = 'running') AS live_instances
    FROM agents a
    JOIN departments d ON d.id = a.department_id
    WHERE d.slug = ${departmentSlug}
      AND a.role_slug = ${roleSlug}
    LIMIT 1
  `);
  if (rows.length === 0) return null;
  const r = rows[0];
  return {
    id: r.id,
    departmentSlug: r.department_slug,
    departmentName: r.department_name,
    roleSlug: r.role_slug,
    agentMd: r.agent_md,
    model: r.model,
    listensFor: asStringArray(r.listens_for),
    skills: asStringArray(r.skills),
    updatedAt: r.updated_at,
    liveInstances: r.live_instances ?? 0,
  };
}

export async function fetchAgents(): Promise<AgentRow[]> {
  const rows = await appDb.execute<{
    id: string;
    department_slug: string;
    department_name: string;
    role_slug: string;
    model: string;
    concurrency_cap: number;
    listens_for: unknown;
    skills: unknown;
    last_spawned_at: Date | null;
    spawns_this_week: number;
    live_instances: number;
    spawns_by_day: number[] | null;
  }>(sql`
    SELECT
      a.id,
      d.slug AS department_slug,
      d.name AS department_name,
      a.role_slug,
      a.model,
      d.concurrency_cap,
      a.listens_for,
      a.skills,
      (SELECT max(ai.started_at) FROM agent_instances ai
        WHERE ai.department_id = a.department_id
          AND ai.role_slug = a.role_slug) AS last_spawned_at,
      (SELECT count(*)::int FROM agent_instances ai
        WHERE ai.department_id = a.department_id
          AND ai.role_slug = a.role_slug
          AND ai.started_at > now() - interval '7 days') AS spawns_this_week,
      (SELECT count(*)::int FROM agent_instances ai
        WHERE ai.department_id = a.department_id
          AND ai.role_slug = a.role_slug
          AND ai.status = 'running') AS live_instances,
      -- 7-day spawn histogram. generate_series produces one row per
      -- day (offset 6 → today=0); LEFT JOIN on agent_instances
      -- bucketed by date_trunc. Aggregated as an int[] ordered
      -- oldest-first → newest-last so the sparkline component
      -- receives a left-to-right time series.
      (SELECT array_agg(coalesce(spawn_cnt, 0) ORDER BY day_offset DESC)
         FROM (
           SELECT
             gs.day_offset,
             count(ai.id)::int AS spawn_cnt
           FROM generate_series(0, 6) AS gs(day_offset)
           LEFT JOIN agent_instances ai
             ON ai.department_id = a.department_id
            AND ai.role_slug = a.role_slug
            AND ai.started_at >=
                  date_trunc('day', now() AT TIME ZONE 'UTC')
                  - (gs.day_offset || ' days')::interval
            AND ai.started_at <
                  date_trunc('day', now() AT TIME ZONE 'UTC')
                  + interval '1 day'
                  - (gs.day_offset || ' days')::interval
           GROUP BY gs.day_offset
         ) buckets) AS spawns_by_day
    FROM agents a
    JOIN departments d ON d.id = a.department_id
    ORDER BY d.slug, a.role_slug
  `);
  return rows.map((r) => ({
    id: r.id,
    departmentSlug: r.department_slug,
    departmentName: r.department_name,
    roleSlug: r.role_slug,
    model: r.model,
    concurrencyCap: r.concurrency_cap,
    listensFor: asStringArray(r.listens_for),
    skills: asStringArray(r.skills),
    lastSpawnedAt: r.last_spawned_at,
    spawnsThisWeek: Number(r.spawns_this_week ?? 0),
    liveInstances: Number(r.live_instances ?? 0),
    spawnsByDay: r.spawns_by_day ?? [0, 0, 0, 0, 0, 0, 0],
  }));
}

export interface AgentRegistryStats {
  totalAgents: number;
  liveAgents: number;
  totalCap: number;
  totalLive: number;
  spawns24h: number;
  idleAgents: number;
}

export async function fetchAgentRegistryStats(): Promise<AgentRegistryStats> {
  const rows = await appDb.execute<{
    total_agents: number;
    live_agents: number;
    total_cap: number;
    total_live: number;
    spawns_24h: number;
    idle_agents: number;
  }>(sql`
    WITH per_agent AS (
      SELECT
        a.id,
        a.department_id,
        a.role_slug,
        d.concurrency_cap,
        (SELECT count(*)::int FROM agent_instances ai
          WHERE ai.department_id = a.department_id
            AND ai.role_slug = a.role_slug
            AND ai.status = 'running') AS live
      FROM agents a
      JOIN departments d ON d.id = a.department_id
    )
    SELECT
      (SELECT count(*)::int FROM per_agent)                                     AS total_agents,
      (SELECT count(*)::int FROM per_agent WHERE live > 0)                      AS live_agents,
      (SELECT coalesce(sum(concurrency_cap), 0)::int FROM departments)          AS total_cap,
      (SELECT count(*)::int FROM agent_instances WHERE status = 'running')      AS total_live,
      (SELECT count(*)::int FROM agent_instances
         WHERE started_at > now() - interval '24 hours')                        AS spawns_24h,
      (SELECT count(*)::int FROM per_agent WHERE live = 0)                      AS idle_agents
  `);
  const r = rows[0];
  return {
    totalAgents: Number(r?.total_agents ?? 0),
    liveAgents: Number(r?.live_agents ?? 0),
    totalCap: Number(r?.total_cap ?? 0),
    totalLive: Number(r?.total_live ?? 0),
    spawns24h: Number(r?.spawns_24h ?? 0),
    idleAgents: Number(r?.idle_agents ?? 0),
  };
}
