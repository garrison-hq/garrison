// Optimistic locking helpers for M4 mutation server actions.
//
// FR-101 / FR-084 / FR-019: agent.md edits and secret edits use
// optimistic locking via the `updated_at` column as the version
// token. The mutation server action snapshots `updated_at` at
// load time, sends it back as the version token on save, and
// the helper rejects the save if the row's current `updated_at`
// no longer matches — meaning another operator (or another tab)
// edited the row in the interim.
//
// Conflict resolution UI lives in components/ui/
// ConflictResolutionModal.tsx (T005); the operator picks
// overwrite / merge-manually / discard. This helper just
// reports the conflict + carries the latest server state
// alongside.
//
// Inline ticket edits intentionally don't use this (FR-034:
// last-write-wins on ticket inline edits). Operator drag-to-move
// is also last-write-wins; the supervisor's existing transition
// machinery handles concurrent finalize-vs-drag at the Postgres
// row-lock layer (FR-043).

import { sql, eq, and, type SQL } from 'drizzle-orm';
import type { PgTable, PgColumn } from 'drizzle-orm/pg-core';
import type { MutationTx } from '@/lib/audit/eventOutbox';

/**
 * Compare-and-swap update against an `updated_at` version token.
 *
 * Returns `{ accepted: true, newVersionToken }` on success, or
 * `{ accepted: false, serverState }` if the row's current
 * `updated_at` does not match the expected token. Caller surfaces
 * the serverState through the conflict resolution modal.
 *
 * Args bundled into a single `params` object to keep the
 * signature compact (S107 — function-arg cap).
 */
export interface CheckAndUpdateParams {
  table: PgTable;
  pkColumn: PgColumn;
  updatedAtColumn: PgColumn;
  /** JS field name of the updated_at column (Drizzle's `set()`
   *  expects JS field names, not SQL column names — pass
   *  `'updatedAt'` for the canonical Drizzle naming). */
  updatedAtFieldName: string;
  idValue: string;
  expectedVersionToken: string;
  /** Caller MUST NOT include the updated_at field; the helper
   *  bumps it automatically. */
  changes: Record<string, unknown>;
}

export async function checkAndUpdate<TRow extends Record<string, unknown>>(
  tx: MutationTx,
  params: CheckAndUpdateParams,
): Promise<
  | { accepted: true; newVersionToken: string; row: TRow }
  | { accepted: false; serverState: TRow | null }
> {
  const { table, pkColumn, updatedAtColumn, updatedAtFieldName, idValue, expectedVersionToken, changes } = params;
  if (Object.hasOwn(changes, updatedAtFieldName) ||
      Object.hasOwn(changes, 'updated_at')) {
    throw new Error(
      `checkAndUpdate: changes must not include ${updatedAtFieldName}; the helper bumps it automatically`,
    );
  }

  const updateSet: Record<string, unknown> = {
    ...changes,
    [updatedAtFieldName]: sql`now()`,
  };

  const condition = and(
    eq(pkColumn, idValue),
    eq(updatedAtColumn, expectedVersionToken),
  ) as SQL<unknown>;

  const updated = await tx
    .update(table)
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    .set(updateSet as any)
    .where(condition)
    .returning();

  if (updated.length === 0) {
    // Either the row doesn't exist or the version doesn't match.
    // Fetch the current row to surface as serverState; if it
    // doesn't exist either, return null.
    const current = await tx
      .select()
      .from(table)
      .where(eq(pkColumn, idValue))
      .limit(1);
    return { accepted: false, serverState: (current[0] as TRow) ?? null };
  }

  const row = updated[0] as TRow;
  const newToken = (row[updatedAtFieldName as keyof TRow] ?? '') as string;
  return { accepted: true, newVersionToken: newToken, row };
}
