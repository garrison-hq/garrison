import Link from 'next/link';
import { StatusChip } from './StatusChip';
import type { McpServerRow } from '@/lib/queries/mcpServers';

function formatTimestamp(value: string | null): string {
  if (!value) return '—';
  const d = new Date(value);
  return d.toISOString().slice(0, 19).replace('T', ' ');
}

export function ServerRow({ row }: Readonly<{ row: McpServerRow }>) {
  return (
    <tr className="border-t border-border-1">
      <td className="px-3 py-2 font-mono text-text-1">
        <Link href={`/admin/mcp-servers/${row.id}`} className="hover:underline">
          {row.name}
        </Link>
      </td>
      <td className="px-3 py-2 text-text-2">{row.transport}</td>
      <td className="px-3 py-2 text-text-3 truncate max-w-[280px]">{row.url ?? '—'}</td>
      <td className="px-3 py-2">
        <StatusChip status={row.status} />
      </td>
      <td className="px-3 py-2 text-text-3 text-xs">{formatTimestamp(row.createdAt)}</td>
      <td className="px-3 py-2 text-text-3 text-xs">{formatTimestamp(row.registeredAt)}</td>
    </tr>
  );
}
