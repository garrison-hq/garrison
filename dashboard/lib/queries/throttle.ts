import { sql, eq, desc, and } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { departments, throttleEvents, tickets } from '@/drizzle/schema.supervisor';

// M6 — throttle_events read query (plan §"Phase 9 — Dashboard").
//
// Returns recent throttle_events rows joined with the company name
// for the dashboard's hygiene-page sub-table. Read-only; the
// dashboard never writes to throttle_events — that table is
// supervisor-side audit (FR-040 / FR-041 in spec.md).
//
// Truncation note: the payload preview is computed client-side so
// the cell can keep the full JSON in a tooltip if a future polish
// pass wants it. Here we stringify the JSONB row verbatim.

export interface ThrottleEventRow {
  eventId: string;
  companyId: string;
  companyName: string;
  kind: string;
  firedAt: Date;
  payload: unknown;
}

export async function listThrottleEvents(limit = 50): Promise<ThrottleEventRow[]> {
  const safeLimit = Math.max(1, Math.min(200, limit));
  const rows = await appDb.execute<{
    event_id: string;
    company_id: string;
    company_name: string;
    kind: string;
    fired_at: Date;
    payload: unknown;
  }>(sql`
    SELECT
      te.id              AS event_id,
      te.company_id,
      c.name             AS company_name,
      te.kind,
      te.fired_at,
      te.payload
    FROM throttle_events te
    JOIN companies c ON c.id = te.company_id
    ORDER BY te.fired_at DESC
    LIMIT ${safeLimit}
  `);
  return rows.map((r) => ({
    eventId: r.event_id,
    companyId: r.company_id,
    companyName: r.company_name,
    kind: r.kind,
    firedAt: r.fired_at,
    payload: r.payload,
  }));
}

// M8 — runaway control read-side. Per FR-501 the /hygiene surface
// gains a per-department panel showing current rolling-7d ticket
// count vs configured weekly_ticket_budget + the last
// dept_weekly_ticket_budget_exceeded fired_at if any.

export interface DeptWeeklyState {
  deptId: string;
  deptSlug: string;
  deptName: string;
  weeklyTicketBudget: number | null;
  currentCount: number;
  lastFiredAt: string | null;
}

export async function listDeptWeeklyState(): Promise<DeptWeeklyState[]> {
  const ticketCounts = appDb
    .select({
      deptId: tickets.departmentId,
      count: sql<number>`count(*)::int`.as('count'),
    })
    .from(tickets)
    .where(sql`${tickets.createdAt} >= NOW() - INTERVAL '7 days'`)
    .groupBy(tickets.departmentId)
    .as('ticket_counts');

  const rows = await appDb
    .select({
      deptId: departments.id,
      deptSlug: departments.slug,
      deptName: departments.name,
      weeklyTicketBudget: departments.weeklyTicketBudget,
      currentCount: sql<number>`COALESCE(${ticketCounts.count}, 0)`,
    })
    .from(departments)
    .leftJoin(ticketCounts, eq(ticketCounts.deptId, departments.id))
    .orderBy(departments.slug);

  const events = await appDb
    .select({
      payload: throttleEvents.payload,
      firedAt: throttleEvents.firedAt,
    })
    .from(throttleEvents)
    .where(
      and(
        eq(throttleEvents.kind, 'dept_weekly_ticket_budget_exceeded'),
        sql`${throttleEvents.firedAt} >= NOW() - INTERVAL '7 days'`,
      ),
    )
    .orderBy(desc(throttleEvents.firedAt));

  const lastFiredBySlug = new Map<string, string>();
  for (const evt of events) {
    const payload = evt.payload as { dept_slug?: string } | null;
    const slug = payload?.dept_slug;
    if (slug && !lastFiredBySlug.has(slug)) {
      lastFiredBySlug.set(slug, evt.firedAt as string);
    }
  }

  return rows.map((r) => ({
    deptId: r.deptId,
    deptSlug: r.deptSlug,
    deptName: r.deptName,
    weeklyTicketBudget: r.weeklyTicketBudget,
    currentCount: Number(r.currentCount ?? 0),
    lastFiredAt: lastFiredBySlug.get(r.deptSlug) ?? null,
  }));
}
