// M8 /admin/mcp-servers/[id] — detail view for a single MCP server.
//
// Surfaces the row's status, failure reason (if 'failed'), transport,
// URL, bearer-token vault path, and the per-customer AllowList
// derived from agents.mcp_servers_jsonb membership. M8 alpha doesn't
// show per-agent AllowList yet (M8.1 polish per FR-501); the page
// instead links back to the agent registry where the AllowList is
// administered.
//
// Per AGENTS.md "Tests for Go only, never frontend": ships without
// vitest coverage.

import Link from 'next/link';
import { notFound } from 'next/navigation';
import { Chip } from '@/components/ui/Chip';
import { StatusChip } from '@/components/features/mcp/StatusChip';
import { getMcpServerByID } from '@/lib/queries/mcpServers';

export const dynamic = 'force-dynamic';

function formatTimestamp(value: string | null): string {
  if (!value) return '—';
  const d = new Date(value);
  return d.toISOString().slice(0, 19).replace('T', ' ');
}

export default async function McpServerDetailPage({
  params,
}: Readonly<{ params: Promise<{ id: string }> }>) {
  const { id } = await params;
  const row = await getMcpServerByID(id);
  if (!row) {
    notFound();
  }

  return (
    <div className="px-6 py-5 space-y-5 max-w-[900px] mx-auto">
      <nav className="text-text-3 text-xs">
        <Link href="/admin/mcp-servers" className="hover:underline">
          ← MCP servers
        </Link>
      </nav>

      <header className="space-y-2">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight font-mono">
          {row.name}
        </h1>
        <div className="flex items-center gap-2 text-sm">
          <StatusChip status={row.status} />
          <Chip tone="neutral">{row.transport}</Chip>
          <Chip tone="neutral">{row.customerSlug}</Chip>
        </div>
      </header>

      <dl className="grid grid-cols-[180px_1fr] gap-y-2 text-sm">
        <dt className="text-text-3">URL</dt>
        <dd className="text-text-1 font-mono">{row.url ?? '—'}</dd>

        <dt className="text-text-3">Bearer-token vault path</dt>
        <dd className="text-text-1 font-mono">{row.bearerTokenPath ?? '—'}</dd>

        <dt className="text-text-3">Created</dt>
        <dd className="text-text-1">{formatTimestamp(row.createdAt)}</dd>

        <dt className="text-text-3">Registered</dt>
        <dd className="text-text-1">{formatTimestamp(row.registeredAt)}</dd>

        <dt className="text-text-3">Last update</dt>
        <dd className="text-text-1">{formatTimestamp(row.updatedAt)}</dd>

        {row.status === 'failed' && row.failureReason && (
          <>
            <dt className="text-warn">Failure reason</dt>
            <dd className="text-warn break-words">{row.failureReason}</dd>
          </>
        )}
      </dl>

      <section className="space-y-1 text-xs text-text-3 border-t border-border-1 pt-3">
        <p>
          Per-agent AllowList membership is currently administered through{' '}
          <Link href="/agents" className="hover:underline">
            /agents
          </Link>{' '}
          (agent <code>mcp_servers</code> JSONB). M8.1 will land a dedicated
          AllowList editor here.
        </p>
      </section>
    </div>
  );
}
