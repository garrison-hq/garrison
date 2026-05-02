import { sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';

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
