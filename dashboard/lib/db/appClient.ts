// appClient — the dashboard's primary Postgres client. Bound to
// DASHBOARD_APP_DSN, which the operator points at the
// `garrison_dashboard_app` role created in T001.
//
// Used by every operational read (org overview, Kanban, ticket
// detail, hygiene table, agents registry, activity-feed catch-up)
// and by better-auth for its own dashboard-side tables (users,
// sessions, accounts, verifications, operator_invites).
//
// vault sub-views MUST NOT import this client — they use
// vaultRoClient. The grep-enforcement test in T013 fails the build
// if any export under lib/queries/vault.ts pulls `appDb` in.

import { drizzle } from 'drizzle-orm/postgres-js';
import postgres from 'postgres';
import * as schema from '@/drizzle/schema';

const dsn = process.env.DASHBOARD_APP_DSN;
if (!dsn) {
  throw new Error(
    'DASHBOARD_APP_DSN is unset. The dashboard requires a connection string ' +
      'pointing at the garrison_dashboard_app role (see T001 + ops-checklist M3 section).',
  );
}

// Pool sizing accounts for the operator workload (1–2 concurrent
// operators, ≤2 concurrent tabs each). The persistent LISTEN slot
// is owned by listenClient.ts and is not counted against this pool.
const sql = postgres(dsn, { max: 10 });

export const appDb = drizzle(sql, { schema });
export type AppDb = typeof appDb;
