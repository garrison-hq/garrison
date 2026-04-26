import type { MatrixCell } from '@/lib/queries/vault';

export function RoleSecretMatrix({
  roles,
  secrets,
  cells,
}: {
  roles: string[];
  secrets: string[];
  cells: MatrixCell[];
}) {
  const grants = new Set(cells.map((c) => `${c.roleSlug}:${c.secretPath}`));
  return (
    <div className="overflow-x-auto border border-border-1 rounded">
      <table className="text-xs">
        <thead>
          <tr>
            <th className="bg-surface-2 border-b border-border-1 px-3 py-2 text-text-3 text-left font-medium uppercase tracking-wider">
              role / secret
            </th>
            {secrets.map((s) => (
              <th
                key={s}
                className="bg-surface-2 border-b border-l border-border-1 px-3 py-2 text-text-3 font-mono text-left"
              >
                <code>{s}</code>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {roles.map((r) => (
            <tr key={r}>
              <td className="border-b border-border-1 px-3 py-2 text-text-1 font-mono whitespace-nowrap">
                {r}
              </td>
              {secrets.map((s) => {
                const granted = grants.has(`${r}:${s}`);
                return (
                  <td
                    key={s}
                    className={`border-b border-l border-border-1 px-3 py-2 text-center font-mono ${
                      granted ? 'bg-accent/15 text-accent' : 'text-text-3'
                    }`}
                    data-testid={granted ? 'matrix-cell-granted' : 'matrix-cell-empty'}
                  >
                    {granted ? '●' : '·'}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
