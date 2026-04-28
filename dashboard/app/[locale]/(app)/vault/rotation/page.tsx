import { fetchSecretsList } from '@/lib/queries/vault';
import { RotationButton } from '@/components/features/vault-rotation/RotationButton';
import { isVaultConfigured } from '@/lib/vault/infisicalClient';
import { relativeTime } from '@/lib/format/relativeTime';

// /vault/rotation — operator-facing rotation dashboard per
// FR-065 / FR-066. Lists secrets in two visually-distinct
// sections: stale (last_rotated_at + rotation_cadence < now())
// and soon-stale (the same predicate offset by 7 days).
//
// Each row carries an inline RotationButton. The
// fetchSecretsList query already classifies rotationStatus into
// fresh / aging / overdue / never; we use that classification
// directly rather than recomputing here.

export const dynamic = 'force-dynamic';

export default async function VaultRotationPage() {
  const rows = await fetchSecretsList();
  const overdue = rows.filter((r) => r.rotationStatus === 'overdue' || r.rotationStatus === 'never');
  const aging = rows.filter((r) => r.rotationStatus === 'aging');

  return (
    <div className="px-6 py-5 space-y-5 max-w-[1200px] mx-auto">
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">
          Rotation
        </h1>
        <p className="text-text-3 text-xs">
          Stale = past rotation cadence; soon-stale = within 7 days. Operator-
          driven via the dashboard ML.
        </p>
      </header>

      {!isVaultConfigured() && (
        <div className="rounded border border-warn/40 bg-warn/5 px-4 py-3 text-[12.5px] text-warn">
          Infisical credentials not configured — rotation actions will fail.
          See <code className="font-mono">docs/ops-checklist.md</code> M4 section.
        </div>
      )}

      <section className="bg-surface-1 border border-border-1 rounded overflow-hidden">
        <header className="px-4 py-2.5 border-b border-border-1 flex items-center gap-3">
          <span className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
            stale
          </span>
          <span className="text-err text-[11px] font-mono font-tabular">
            {overdue.length}
          </span>
        </header>
        {overdue.length === 0 ? (
          <p className="px-4 py-6 text-[12.5px] text-text-3">No stale secrets.</p>
        ) : (
          <RotationTable rows={overdue} />
        )}
      </section>

      <section className="bg-surface-1 border border-border-1 rounded overflow-hidden">
        <header className="px-4 py-2.5 border-b border-border-1 flex items-center gap-3">
          <span className="text-text-3 text-[10.5px] uppercase tracking-[0.08em] font-medium">
            soon-stale
          </span>
          <span className="text-warn text-[11px] font-mono font-tabular">
            {aging.length}
          </span>
        </header>
        {aging.length === 0 ? (
          <p className="px-4 py-6 text-[12.5px] text-text-3">No soon-stale secrets.</p>
        ) : (
          <RotationTable rows={aging} />
        )}
      </section>
    </div>
  );
}

function RotationTable({
  rows,
}: Readonly<{
  rows: Array<{
    secretPath: string;
    rotationCadence: string;
    lastRotatedAt: Date | null;
    rotationStatus: string;
    rotationProvider: 'infisical_native' | 'manual_paste' | 'not_rotatable';
  }>;
}>) {
  return (
    <table className="w-full text-[12.5px]">
      <thead className="text-[10.5px] uppercase tracking-wide text-text-3">
        <tr className="border-b border-border-1">
          <th className="text-left px-3 py-2 font-normal">secret path</th>
          <th className="text-left px-3 py-2 font-normal">cadence</th>
          <th className="text-left px-3 py-2 font-normal">last rotated</th>
          <th className="text-left px-3 py-2 font-normal">provider</th>
          <th className="text-right px-3 py-2 font-normal"></th>
        </tr>
      </thead>
      <tbody className="divide-y divide-border-1">
        {rows.map((row) => (
          <tr key={row.secretPath}>
            <td className="px-3 py-2 font-mono text-text-1 truncate max-w-[400px]" title={row.secretPath}>
              {row.secretPath}
            </td>
            <td className="px-3 py-2 text-text-2 font-mono tabular-nums">
              {row.rotationCadence}
            </td>
            <td className="px-3 py-2 text-text-3">
              {row.lastRotatedAt ? relativeTime(row.lastRotatedAt) : 'never'}
            </td>
            <td className="px-3 py-2 text-text-3 font-mono">{row.rotationProvider}</td>
            <td className="px-3 py-2 text-right">
              <RotationButton
                secretPath={row.secretPath}
                rotationProvider={row.rotationProvider}
              />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
