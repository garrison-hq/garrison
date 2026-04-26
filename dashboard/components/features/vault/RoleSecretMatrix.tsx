import { getTranslations } from 'next-intl/server';
import type { MatrixCell } from '@/lib/queries/vault';

// Role × secret grant matrix. Layout decisions from the polish round:
//
//  - Sticky first column for the role name (so secrets can scroll
//    horizontally while the role label stays visible).
//  - Header secret-path column reads horizontally up to ~6 columns;
//    rotates 270° (writing-mode vertical-rl) at higher counts so a
//    long path doesn't blow the row width out.
//  - Granted cells: bg-accent/12 background + centered ✓ rendered
//    in the accent color. Not a flat green block — the dim variable
//    sits on top of the surface so the grid lines still read.
//  - Empty cells: just the grid line + a faint center-dot in
//    text-4 so the grid scans as a grid, not a void.
//  - Row + column totals as a final Σ row/column in muted mono.
//  - Legend strip above the matrix (granted swatch · not-granted
//    swatch) so the visual code is explicit.

const ROTATE_HEADER_AT = 7;

export async function RoleSecretMatrix({
  roles,
  secrets,
  cells,
}: Readonly<{
  roles: string[];
  secrets: string[];
  cells: MatrixCell[];
}>) {
  const t = await getTranslations('vault.legend');
  const grants = new Set(cells.map((c) => `${c.roleSlug}:${c.secretPath}`));
  const rotate = secrets.length >= ROTATE_HEADER_AT;

  // Per-row + per-column totals.
  const rowTotals = new Map<string, number>();
  const colTotals = new Map<string, number>();
  for (const c of cells) {
    rowTotals.set(c.roleSlug, (rowTotals.get(c.roleSlug) ?? 0) + 1);
    colTotals.set(c.secretPath, (colTotals.get(c.secretPath) ?? 0) + 1);
  }

  return (
    <section className="space-y-3">
      <div className="flex items-center gap-4 text-[11px] text-text-3">
        <Swatch label={t('granted')} className="bg-accent/30 border border-accent/50" mark="✓" />
        <Swatch label={t('notGranted')} className="bg-surface-2 border border-border-1" mark="·" />
      </div>
      <div className="overflow-x-auto bg-surface-1 border border-border-1 rounded">
        <table className="border-separate border-spacing-0 text-[12px] w-full">
          <thead>
            <tr>
              <th
                scope="col"
                className="sticky left-0 z-10 bg-surface-2 border-b border-r border-border-1 px-3 py-2 text-left text-[10.5px] font-medium uppercase tracking-[0.08em] text-text-3"
                style={{ minWidth: 160 }}
              >
                role
              </th>
              {secrets.map((s) => (
                <th
                  key={s}
                  scope="col"
                  className="bg-surface-2 border-b border-r border-border-1 px-3 py-2 text-text-3 font-mono align-bottom"
                  style={{
                    width: 140,
                    minWidth: 140,
                    height: rotate ? 140 : undefined,
                    writingMode: rotate ? 'vertical-rl' : undefined,
                    transform: rotate ? 'rotate(180deg)' : undefined,
                  }}
                >
                  <code className="text-[11px]">{s}</code>
                </th>
              ))}
              <th
                scope="col"
                className="bg-surface-2 border-b border-border-1 px-3 py-2 text-right text-[10.5px] uppercase tracking-[0.08em] text-text-3 font-medium"
                style={{ minWidth: 56 }}
              >
                Σ
              </th>
            </tr>
          </thead>
          <tbody>
            {roles.map((r) => (
              <tr key={r}>
                <th
                  scope="row"
                  className="sticky left-0 z-10 bg-surface-1 border-b border-r border-border-1 px-3 py-2 text-text-1 font-mono text-left whitespace-nowrap"
                >
                  {r}
                </th>
                {secrets.map((s) => {
                  const granted = grants.has(`${r}:${s}`);
                  return (
                    <td
                      key={s}
                      className={`border-b border-r border-border-1 px-3 py-2 text-center font-mono ${
                        granted
                          ? 'bg-accent/12 text-accent'
                          : 'text-text-4'
                      }`}
                      data-testid={granted ? 'matrix-cell-granted' : 'matrix-cell-empty'}
                      title={granted ? `granted: ${r} → ${s}` : `not granted: ${r} → ${s}`}
                    >
                      {granted ? '✓' : '·'}
                    </td>
                  );
                })}
                <td className="border-b border-border-1 px-3 py-2 text-right text-text-3 font-mono font-tabular">
                  {rowTotals.get(r) ?? 0}
                </td>
              </tr>
            ))}
            <tr>
              <th
                scope="row"
                className="sticky left-0 z-10 bg-surface-2 border-r border-border-1 px-3 py-2 text-right text-[10.5px] uppercase tracking-[0.08em] text-text-3 font-medium"
              >
                Σ
              </th>
              {secrets.map((s) => (
                <td
                  key={s}
                  className="bg-surface-2 border-r border-border-1 px-3 py-2 text-center text-text-3 font-mono font-tabular"
                >
                  {colTotals.get(s) ?? 0}
                </td>
              ))}
              <td className="bg-surface-2 px-3 py-2 text-right text-text-1 font-mono font-tabular font-semibold">
                {cells.length}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  );
}

function Swatch({
  label,
  className,
  mark,
}: Readonly<{ label: string; className: string; mark: string }>) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span
        className={`inline-grid place-items-center w-4 h-4 rounded text-[10px] font-mono ${className}`}
      >
        <span className={mark === '✓' ? 'text-accent' : 'text-text-4'}>{mark}</span>
      </span>
      <span>{label}</span>
    </span>
  );
}
