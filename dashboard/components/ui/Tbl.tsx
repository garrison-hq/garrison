import type { HTMLAttributes } from 'react';

// Thin <table> wrapper that ships with the dashboard's row density
// + border treatment. Used by the hygiene table, audit log, agents
// registry, etc. Children supply the actual <thead>/<tbody>.

export function Tbl({ children, ...rest }: HTMLAttributes<HTMLTableElement>) {
  return (
    <div className="border border-border-1 rounded overflow-hidden">
      <table {...rest} className="w-full text-sm">
        {children}
      </table>
    </div>
  );
}

export function Th({ children, ...rest }: HTMLAttributes<HTMLTableCellElement>) {
  return (
    <th
      {...rest}
      className="text-left text-text-3 font-medium text-[11px] uppercase tracking-wider px-3 py-2 bg-surface-2 border-b border-border-1"
    >
      {children}
    </th>
  );
}

export function Td({ children, ...rest }: HTMLAttributes<HTMLTableCellElement>) {
  return (
    <td {...rest} className="px-3 py-2 text-text-1 border-b border-border-1 last:border-b-0">
      {children}
    </td>
  );
}
