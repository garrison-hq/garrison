// vaultRoClient — the only Postgres client allowed to query vault
// tables. Bound to DASHBOARD_RO_DSN, which the operator points at
// the `garrison_dashboard_ro` role created in T001 (NOT at
// garrison_agent_ro / garrison_agent_mempalace / any other
// agent-facing role per AGENTS.md §M3 + the M2.3 vault threat
// model).
//
// `lib/queries/vault.ts` is the ONLY file allowed to import
// `vaultRoDb` from this module. T013's static-analysis test
// rejects any other importer at build time.
//
// Joinable read tables (tickets, ticket_transitions, agents,
// agent_instances) are also reachable via this client because
// FR-021 requires the audit-log filters and the role-secret
// matrix to join across them; the GRANT migration (T001) covers
// those tables for garrison_dashboard_ro.

import { drizzle } from 'drizzle-orm/postgres-js';
import postgres from 'postgres';
import * as schema from '@/drizzle/schema';

// Lazy-init so a missing DASHBOARD_RO_DSN at module-load time
// (e.g. during the Docker build's static prerender) doesn't crash
// the build. The DSN is required only when a vault query runs.

let _client: ReturnType<typeof postgres> | null = null;
let _db: ReturnType<typeof drizzle<typeof schema>> | null = null;

function getDb() {
  if (_db) return _db;
  const dsn = process.env.DASHBOARD_RO_DSN;
  if (!dsn) {
    throw new Error(
      'DASHBOARD_RO_DSN is unset. Vault read views require a connection string ' +
        'pointing at the garrison_dashboard_ro role (see T001 + ops-checklist M3 section).',
    );
  }
  // Smaller pool than appDb — vault sub-views are infrequent
  // operator triage flows, not hot-path reads.
  _client = postgres(dsn, { max: 4 });
  _db = drizzle(_client, { schema });
  return _db;
}

export const vaultRoDb = new Proxy({} as ReturnType<typeof drizzle<typeof schema>>, {
  get(_target, prop) {
    const db = getDb() as unknown as Record<string | symbol, unknown>;
    const value = db[prop];
    return typeof value === 'function' ? (value as (...args: unknown[]) => unknown).bind(db) : value;
  },
});
export type VaultRoDb = ReturnType<typeof drizzle<typeof schema>>;
