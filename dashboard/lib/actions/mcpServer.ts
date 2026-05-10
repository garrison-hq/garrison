'use server';

// M8 register_mcp_server Server Action — operator-only path to add a
// new MCP server to MCPJungle's registry. Mirrors the supervisor-side
// internal/garrisonmutate/register_mcp_server.go validations
// (customer-prefix invariant, transport whitelist, URL required for
// http/sse). The INSERT lands a status='pending' row; the M8 trigger
// fires pg_notify('work.mcp_server.registration_requested', ...) and
// the supervisor's mcpserverwork worker picks it up.
//
// FR-306 single-row invariant: NO chat_mutation_audit row is written
// here. The supervisor's reactive worker writes the canonical audit
// row anchored on the final outcome ('success' | 'failed') when the
// MCPJungle API call completes.
//
// Per AGENTS.md "Tests for Go only, never frontend": ships without
// vitest coverage; the Go-side TestRegister* suite covers shape.

import { eq } from 'drizzle-orm';
import { appDb } from '@/lib/db/appClient';
import { mcpServers, companies } from '@/drizzle/schema.supervisor';
import { getSession } from '@/lib/auth/session';
import { AuthError, AuthErrorKind } from '@/lib/auth/errors';

export type RegisterMcpServerResult =
  | { ok: true; id: string; url: string }
  | { ok: false; errorKind: 'validation_failed' | 'duplicate' | 'unauthorized'; message: string };

const VALID_TRANSPORTS = new Set(['http', 'stdio', 'sse']);

export interface RegisterMcpServerInput {
  name: string;
  transport: string;
  url?: string;
  bearerTokenPath?: string;
}

/** registerMcpServer — Server Action wrapping the mcp_servers row
 *  INSERT. Returns a typed Result the form can render without a
 *  follow-up navigation. */
export async function registerMcpServer(
  input: RegisterMcpServerInput,
): Promise<RegisterMcpServerResult> {
  const session = await getSession();
  if (!session) {
    throw new AuthError(AuthErrorKind.NoSession, 'register_mcp_server requires an operator session');
  }

  const name = (input.name ?? '').trim();
  const transport = (input.transport ?? '').trim();
  const url = (input.url ?? '').trim();
  const bearerTokenPath = (input.bearerTokenPath ?? '').trim();

  if (!name) {
    return { ok: false, errorKind: 'validation_failed', message: 'name is required' };
  }
  if (!VALID_TRANSPORTS.has(transport)) {
    return {
      ok: false,
      errorKind: 'validation_failed',
      message: 'transport must be one of http, stdio, sse',
    };
  }
  if (transport !== 'stdio' && !url) {
    return {
      ok: false,
      errorKind: 'validation_failed',
      message: 'url is required for http and sse transports',
    };
  }

  // Resolve active customer_slug + enforce FR-307 customer-prefix.
  const companyRows = await appDb
    .select({ customerSlug: companies.customerSlug })
    .from(companies)
    .limit(1);
  const customerSlug = companyRows[0]?.customerSlug ?? 'garrison';
  const prefix = `${customerSlug}.`;
  if (!name.startsWith(prefix) || name.length <= prefix.length) {
    return {
      ok: false,
      errorKind: 'validation_failed',
      message: `name must start with "${prefix}" (customer-prefix invariant FR-307)`,
    };
  }

  try {
    const inserted = await appDb
      .insert(mcpServers)
      .values({
        customerSlug,
        name,
        transport,
        url: url || null,
        bearerTokenPath: bearerTokenPath || null,
      })
      .returning({ id: mcpServers.id });
    const id = inserted[0]?.id;
    if (!id) {
      return { ok: false, errorKind: 'validation_failed', message: 'insert returned no id' };
    }
    return { ok: true, id, url: `/admin/mcp-servers/${id}` };
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    if (msg.includes('mcp_servers_customer_slug_name_key')) {
      return {
        ok: false,
        errorKind: 'duplicate',
        message: `server "${name}" is already registered for customer "${customerSlug}"`,
      };
    }
    throw err;
  }
}

/** retryFailedRegistration — operator-driven re-submit for a 'failed'
 *  row. Flips the row back to 'pending' so the INSERT trigger fires
 *  (we re-INSERT to fire the AFTER INSERT trigger — UPDATE doesn't
 *  fire it). */
export async function retryFailedRegistration(id: string): Promise<RegisterMcpServerResult> {
  const session = await getSession();
  if (!session) {
    throw new AuthError(AuthErrorKind.NoSession, 'retry requires an operator session');
  }
  const row = await appDb
    .select({
      customerSlug: mcpServers.customerSlug,
      name: mcpServers.name,
      transport: mcpServers.transport,
      url: mcpServers.url,
      bearerTokenPath: mcpServers.bearerTokenPath,
      status: mcpServers.status,
    })
    .from(mcpServers)
    .where(eq(mcpServers.id, id))
    .limit(1);
  if (row.length === 0) {
    return { ok: false, errorKind: 'validation_failed', message: 'mcp_servers row not found' };
  }
  if (row[0].status !== 'failed') {
    return {
      ok: false,
      errorKind: 'validation_failed',
      message: `cannot retry from status "${row[0].status}"; only 'failed' is retryable`,
    };
  }
  return registerMcpServer({
    name: row[0].name,
    transport: row[0].transport,
    url: row[0].url ?? undefined,
    bearerTokenPath: row[0].bearerTokenPath ?? undefined,
  });
}
