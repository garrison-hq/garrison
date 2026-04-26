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

const dsn = process.env.DASHBOARD_RO_DSN;
if (!dsn) {
  throw new Error(
    'DASHBOARD_RO_DSN is unset. Vault read views require a connection string ' +
      'pointing at the garrison_dashboard_ro role (see T001 + ops-checklist M3 section).',
  );
}

// Smaller pool than appDb — vault sub-views are infrequent operator
// triage flows, not hot-path reads.
const sql = postgres(dsn, { max: 4 });

export const vaultRoDb = drizzle(sql, { schema });
export type VaultRoDb = typeof vaultRoDb;
