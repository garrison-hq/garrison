import { sql } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';

// Hygiene table queries (FR-070 → FR-075).
//
// Returns ticket_transitions rows whose hygiene_status is non-clean,
// joined with the ticket id + department slug + the agent_instance's
// exit_reason for finalize-path triage. Filters drive the URL state.
//
// Pattern category extraction (FR-074): for `suspected_secret_emitted`
// rows, we surface the offending pattern category (e.g.
// "github_pat") but NEVER the matched value. The category is encoded
// in the hygiene_status itself today as `suspected_secret_emitted`
// without a category suffix; the M2.3 scanner's pattern label set
// is captured via a follow-up migration. For M3 we render the
// suspected_secret_emitted rows with a labelled "secret-shape"
// placeholder; T013 wires the per-pattern label set if the
// migration lands first.

export type FailureMode =
  | 'finalize_path'
  | 'sandbox_escape'
  | 'suspected_secret_emitted';

const FINALIZE_PATH_STATUSES = [
  'finalize_never_called',
  'finalize_failed',
  'finalize_partial',
  'stuck',
];
const SANDBOX_ESCAPE_STATUSES = ['sandbox_escape', 'artifact_claimed_vs_on_disk'];

export interface HygieneFilter {
  failureMode?: FailureMode;
  departmentSlug?: string;
  page?: number;
  pageSize?: number;
}

export interface HygieneRow {
  transitionId: string;
  ticketId: string;
  departmentSlug: string;
  hygieneStatus: string;
  fromColumn: string | null;
  toColumn: string;
  at: Date;
  exitReason: string | null;
  /** Derived: which failure-mode bucket the hygiene_status falls into. */
  failureMode: FailureMode;
  /** Pattern category for suspected_secret_emitted rows. The actual
   *  matched secret is NEVER returned — only the label. */
  patternCategory: string | null;
}

function classify(status: string): FailureMode | null {
  if (FINALIZE_PATH_STATUSES.includes(status)) return 'finalize_path';
  if (SANDBOX_ESCAPE_STATUSES.includes(status)) return 'sandbox_escape';
  if (status === 'suspected_secret_emitted' || status.startsWith('suspected_'))
    return 'suspected_secret_emitted';
  return null;
}

function statusesForFailureMode(mode: FailureMode): string[] {
  switch (mode) {
    case 'finalize_path':
      return FINALIZE_PATH_STATUSES;
    case 'sandbox_escape':
      return SANDBOX_ESCAPE_STATUSES;
    case 'suspected_secret_emitted':
      return ['suspected_secret_emitted'];
  }
}

export async function fetchHygieneRows(
  filter: HygieneFilter = {},
): Promise<{ rows: HygieneRow[]; total: number }> {
  const page = Math.max(1, filter.page ?? 1);
  const pageSize = Math.max(1, Math.min(100, filter.pageSize ?? 25));
  const offset = (page - 1) * pageSize;

  const statusList = filter.failureMode ? statusesForFailureMode(filter.failureMode) : null;

  // The IN-list values come exclusively from the hardcoded
  // failure-mode → status-list mapping above (statusesForFailureMode);
  // they are never user-controlled. We still escape single quotes
  // defensively so a future code edit that introduces an apostrophe
  // doesn't open a SQL injection seam.
  const rowsResult = await appDb.execute<{
    transition_id: string;
    ticket_id: string;
    department_slug: string;
    hygiene_status: string;
    from_column: string | null;
    to_column: string;
    at: Date;
    exit_reason: string | null;
  }>(sql`
    SELECT
      tt.id AS transition_id,
      tt.ticket_id,
      d.slug AS department_slug,
      tt.hygiene_status,
      tt.from_column,
      tt.to_column,
      tt.at,
      ai.exit_reason
    FROM ticket_transitions tt
    JOIN tickets t ON t.id = tt.ticket_id
    JOIN departments d ON d.id = t.department_id
    LEFT JOIN agent_instances ai ON ai.id = tt.triggered_by_agent_instance_id
    WHERE tt.hygiene_status IS NOT NULL
      AND tt.hygiene_status NOT IN ('clean', '')
      ${statusList ? sql`AND tt.hygiene_status IN ${sql.raw(`(${statusList.map((s) => `'${s.replace(/'/g, "''")}'`).join(', ')})`)}` : sql``}
      ${filter.departmentSlug ? sql`AND d.slug = ${filter.departmentSlug}` : sql``}
    ORDER BY tt.at DESC
    LIMIT ${pageSize} OFFSET ${offset}
  `);

  const totalResult = await appDb.execute<{ total: number }>(sql`
    SELECT count(*)::int AS total
    FROM ticket_transitions tt
    JOIN tickets t ON t.id = tt.ticket_id
    JOIN departments d ON d.id = t.department_id
    WHERE tt.hygiene_status IS NOT NULL
      AND tt.hygiene_status NOT IN ('clean', '')
      ${statusList ? sql`AND tt.hygiene_status IN ${sql.raw(`(${statusList.map((s) => `'${s.replace(/'/g, "''")}'`).join(', ')})`)}` : sql``}
      ${filter.departmentSlug ? sql`AND d.slug = ${filter.departmentSlug}` : sql``}
  `);

  return {
    rows: rowsResult.map((r) => ({
      transitionId: r.transition_id,
      ticketId: r.ticket_id,
      departmentSlug: r.department_slug,
      hygieneStatus: r.hygiene_status,
      fromColumn: r.from_column,
      toColumn: r.to_column,
      at: r.at,
      exitReason: r.exit_reason,
      failureMode: classify(r.hygiene_status) ?? 'finalize_path',
      patternCategory:
        classify(r.hygiene_status) === 'suspected_secret_emitted' ? 'secret-shape' : null,
    })),
    total: Number(totalResult[0]?.total ?? 0),
  };
}
