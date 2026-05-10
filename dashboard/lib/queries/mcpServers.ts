// M8 /admin/mcp-servers — read-side queries for the MCP-server registry.
//
// Per AGENTS.md "Tests for Go only, never frontend": this module ships
// without vitest coverage; the Go-side T008 + T018 integration tests
// cover the data shape.

import { eq, desc } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { mcpServers, companies } from '@/drizzle/schema.supervisor';

export type McpServerStatus = 'pending' | 'registered' | 'failed' | 'deregistered';

export interface McpServerRow {
  id: string;
  customerSlug: string;
  name: string;
  transport: string;
  url: string | null;
  bearerTokenPath: string | null;
  status: McpServerStatus;
  failureReason: string | null;
  registeredAt: string | null;
  createdAt: string;
  updatedAt: string;
}

/** ListMcpServersByCustomer — returns every row for the operator's
 *  active customer, newest first. M8 alpha is single-tenant so the
 *  customer_slug is resolved from the single companies row; beta will
 *  pivot to the operator's session-bound customer. */
export async function listMcpServersByCustomer(limit = 200): Promise<McpServerRow[]> {
  const rows = await appDb
    .select({
      id: mcpServers.id,
      customerSlug: mcpServers.customerSlug,
      name: mcpServers.name,
      transport: mcpServers.transport,
      url: mcpServers.url,
      bearerTokenPath: mcpServers.bearerTokenPath,
      status: mcpServers.status,
      failureReason: mcpServers.failureReason,
      registeredAt: mcpServers.registeredAt,
      createdAt: mcpServers.createdAt,
      updatedAt: mcpServers.updatedAt,
    })
    .from(mcpServers)
    .orderBy(desc(mcpServers.createdAt))
    .limit(limit);
  return rows.map((r) => ({
    ...r,
    status: r.status as McpServerStatus,
  }));
}

/** getMcpServerByID — single-row lookup for the detail page. */
export async function getMcpServerByID(id: string): Promise<McpServerRow | null> {
  const rows = await appDb
    .select({
      id: mcpServers.id,
      customerSlug: mcpServers.customerSlug,
      name: mcpServers.name,
      transport: mcpServers.transport,
      url: mcpServers.url,
      bearerTokenPath: mcpServers.bearerTokenPath,
      status: mcpServers.status,
      failureReason: mcpServers.failureReason,
      registeredAt: mcpServers.registeredAt,
      createdAt: mcpServers.createdAt,
      updatedAt: mcpServers.updatedAt,
    })
    .from(mcpServers)
    .where(eq(mcpServers.id, id))
    .limit(1);
  if (rows.length === 0) return null;
  return { ...rows[0], status: rows[0].status as McpServerStatus };
}

/** getActiveCustomerSlug — M8 alpha helper: returns the single
 *  customers row's slug (FR-307 customer-prefix invariant). Beta will
 *  resolve from the operator's session. */
export async function getActiveCustomerSlug(): Promise<string> {
  const rows = await appDb
    .select({ customerSlug: companies.customerSlug })
    .from(companies)
    .limit(1);
  return rows[0]?.customerSlug ?? 'garrison';
}
