import { sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';

// Sidebar status box ("X agents live"). Returns the count of
// agent_instances currently in `status='running'` plus the sum of
// per-department concurrency caps so the operator can see headroom
// at a glance. The cost/throughput line in the M3 mock is held
// behind the cost-telemetry blind spot (docs/issues/cost-telemetry-
// blind-spot.md) — we render the live count + capacity only.

export interface SidebarStats {
  liveAgents: number;
  totalCapacity: number;
}

export async function fetchSidebarStats(): Promise<SidebarStats> {
  const rows = await appDb.execute<{ live_agents: number; total_capacity: number }>(sql`
    SELECT
      (SELECT count(*)::int FROM agent_instances WHERE status = 'running') AS live_agents,
      (SELECT coalesce(sum(concurrency_cap), 0)::int FROM departments)     AS total_capacity
  `);
  const r = rows[0];
  return {
    liveAgents: Number(r?.live_agents ?? 0),
    totalCapacity: Number(r?.total_capacity ?? 0),
  };
}
