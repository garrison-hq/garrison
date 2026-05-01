// M5.3 hiring proposals read-side queries. Backs the read-only
// stopgap page at /hiring/proposals (FR-490..FR-494). M7 extends with
// review/approve/spawn flow; M5.3 reads only.

import { eq, desc } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { hiringProposals } from '@/drizzle/schema.supervisor';

export type HiringProposalRow = typeof hiringProposals.$inferSelect;

// getProposalsForCurrentUser returns hiring proposals visible to the
// authenticated operator. Single-tenant single-operator (Constitution
// X): all rows are visible to the operator. M7 may add per-operator
// scoping if multi-operator lands; until then, no row-level filtering.
export async function getProposalsForCurrentUser(limit = 100): Promise<HiringProposalRow[]> {
  return appDb
    .select()
    .from(hiringProposals)
    .orderBy(desc(hiringProposals.createdAt))
    .limit(limit);
}

// getProposalById returns the single proposal row, or null if not
// found. Used by the M7 detail-page handoff (M5.3 stopgap doesn't
// link to detail pages — chip clicks open /hiring/proposals/<id>
// which falls back to a 404 until M7 implements it).
export async function getProposalById(id: string): Promise<HiringProposalRow | null> {
  const rows = await appDb
    .select()
    .from(hiringProposals)
    .where(eq(hiringProposals.id, id))
    .limit(1);
  return rows[0] ?? null;
}
