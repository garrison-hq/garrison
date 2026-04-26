import Link from 'next/link';
import { getTranslations } from 'next-intl/server';
import { Chip } from '@/components/ui/Chip';
import { Tbl, Th, Td } from '@/components/ui/Tbl';
import { formatIsoFull, relativeTime } from '@/lib/format/relativeTime';
import type { VaultAuditRow } from '@/lib/queries/vault';

function outcomeTone(outcome: string): 'ok' | 'err' | 'warn' | 'neutral' {
  if (outcome === 'granted' || outcome === 'allowed') return 'ok';
  if (outcome === 'denied' || outcome === 'fail_closed') return 'err';
  if (outcome === 'error') return 'warn';
  return 'neutral';
}

// Group audit rows by UTC date so the table reads as
//   ─ 2026-04-26 ─
//   13:18  …
//   12:04  …
//   ─ 2026-04-25 ─
//   ...
// instead of an unbroken column of timestamps. Same shape the
// operator-supplied design notes called out.
function groupByDay(rows: VaultAuditRow[]): { day: string; rows: VaultAuditRow[] }[] {
  const out: { day: string; rows: VaultAuditRow[] }[] = [];
  for (const r of rows) {
    const day = new Date(r.timestamp).toISOString().slice(0, 10);
    const last = out.at(-1);
    if (last?.day === day) {
      last.rows.push(r);
    } else {
      out.push({ day, rows: [r] });
    }
  }
  return out;
}

export async function AuditLog({
  rows,
}: Readonly<{ rows: VaultAuditRow[] }>) {
  const t = await getTranslations('vault.headers');
  const groups = groupByDay(rows);
  return (
    <Tbl>
      <thead>
        <tr>
          <Th>{t('timestamp')}</Th>
          <Th>{t('role')}</Th>
          <Th>{t('secretPath')}</Th>
          <Th>{t('outcome')}</Th>
          <Th>{t('ticket')}</Th>
        </tr>
      </thead>
      <tbody>
        {groups.map((g) => (
          <Group key={g.day} day={g.day} rows={g.rows} />
        ))}
      </tbody>
    </Tbl>
  );
}

function Group({ day, rows }: Readonly<{ day: string; rows: VaultAuditRow[] }>) {
  return (
    <>
      <tr>
        <th
          scope="rowgroup"
          colSpan={5}
          className="text-left px-3 py-1.5 bg-surface-2/50 border-t border-border-1 border-b text-text-3 font-mono text-[10.5px] tracking-tight font-normal"
        >
          {day}
        </th>
      </tr>
      {rows.map((r) => (
        <tr key={r.id} data-testid="audit-row">
          <Td>
            <span
              className="font-mono font-tabular text-[12px] text-text-2"
              title={`${formatIsoFull(r.timestamp)} (${relativeTime(r.timestamp)})`}
            >
              {new Date(r.timestamp).toISOString().slice(11, 19)}
            </span>
          </Td>
          <Td>
            <Chip tone="neutral">{r.roleSlug}</Chip>
          </Td>
          <Td>
            <code className="font-mono text-[12px] text-text-1">
              {r.secretPath}
            </code>
          </Td>
          <Td>
            <Chip tone={outcomeTone(r.outcome)}>{r.outcome}</Chip>
          </Td>
          <Td>
            {r.ticketId ? (
              <Link
                href={`/tickets/${r.ticketId}`}
                className="font-mono text-[12px] text-info hover:underline"
              >
                {r.ticketId.slice(0, 8)}
              </Link>
            ) : (
              <span className="text-text-3">—</span>
            )}
          </Td>
        </tr>
      ))}
    </>
  );
}
