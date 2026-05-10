// M8 /admin/mcp-servers — operator surface for the MCPJungle registry.
//
// Lists every mcp_servers row grouped by status (pending / registered
// / failed / deregistered) + renders the RegisterForm at the top. The
// supervisor's mcpserverwork worker (T006) reactively picks up
// pending rows and flips status; the operator refreshes the page to
// see updated state (poll-based; SSE wiring is M8.1).
//
// Per AGENTS.md "Tests for Go only, never frontend": ships without
// vitest coverage; the Go-side T008 + T018 integration tests cover
// the data shape.

import { EmptyState } from '@/components/ui/EmptyState';
import { RegisterForm } from '@/components/features/mcp/RegisterForm';
import { ServerRow } from '@/components/features/mcp/ServerRow';
import {
  listMcpServersByCustomer,
  getActiveCustomerSlug,
  type McpServerRow,
  type McpServerStatus,
} from '@/lib/queries/mcpServers';

export const dynamic = 'force-dynamic';

function groupByStatus(rows: McpServerRow[]): Record<McpServerStatus, McpServerRow[]> {
  const out: Record<McpServerStatus, McpServerRow[]> = {
    pending: [],
    registered: [],
    failed: [],
    deregistered: [],
  };
  for (const r of rows) {
    out[r.status].push(r);
  }
  return out;
}

const sectionLabels: Record<McpServerStatus, string> = {
  pending: 'Pending',
  registered: 'Registered',
  failed: 'Failed',
  deregistered: 'Deregistered',
};

export default async function AdminMcpServersPage() {
  const [rows, customerSlug] = await Promise.all([
    listMcpServersByCustomer(200),
    getActiveCustomerSlug(),
  ]);
  const grouped = groupByStatus(rows);
  const customerPrefix = `${customerSlug}.`;

  return (
    <div className="px-6 py-5 space-y-6 max-w-[1400px] mx-auto">
      <header className="space-y-1">
        <h1 className="text-text-1 text-2xl font-semibold tracking-tight">MCP servers</h1>
        <p className="text-text-3 text-sm">
          MCPJungle-backed MCP server registry. Operator-approved entries land here as{' '}
          <code>pending</code>; the supervisor&apos;s reactive worker calls MCPJungle&apos;s admin
          API and flips status to <code>registered</code> or <code>failed</code>.
        </p>
      </header>

      <RegisterForm customerPrefix={customerPrefix} />

      {rows.length === 0 ? (
        <EmptyState
          description="No MCP servers registered yet"
          caption="Use the form above to register the first server. The name must start with the active customer prefix."
        />
      ) : (
        (Object.keys(sectionLabels) as McpServerStatus[]).map((status) => {
          const group = grouped[status];
          if (group.length === 0) return null;
          return (
            <section key={status} className="space-y-2">
              <h2 className="text-text-2 text-sm font-semibold tracking-tight">
                {sectionLabels[status]} ({group.length})
              </h2>
              <table
                className="w-full text-sm border border-border-1 rounded"
                data-testid={`mcp-servers-${status}-table`}
              >
                <thead className="bg-surface-2 text-text-3 text-[11px] uppercase tracking-[0.06em]">
                  <tr>
                    <th className="text-left px-3 py-2">Name</th>
                    <th className="text-left px-3 py-2">Transport</th>
                    <th className="text-left px-3 py-2">URL</th>
                    <th className="text-left px-3 py-2">Status</th>
                    <th className="text-left px-3 py-2">Created</th>
                    <th className="text-left px-3 py-2">Registered</th>
                  </tr>
                </thead>
                <tbody>
                  {group.map((row) => (
                    <ServerRow key={row.id} row={row} />
                  ))}
                </tbody>
              </table>
            </section>
          );
        })
      )}
    </div>
  );
}
