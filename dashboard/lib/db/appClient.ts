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

// Lazy-init so a missing DASHBOARD_APP_DSN at module-load time
// (e.g. during the Docker build's static prerender) doesn't crash
// the build. The DSN is required only when a query actually runs.

let _client: ReturnType<typeof postgres> | null = null;
let _db: ReturnType<typeof drizzle<typeof schema>> | null = null;

function getDb() {
  if (_db) return _db;
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
  _client = postgres(dsn, { max: 10 });
  _db = drizzle(_client, { schema });
  return _db;
}

// Proxy the drizzle methods through getDb() so `appDb.select()`
// etc. lazy-initialise on first use. The Proxy preserves the
// original `db` object's identity for any consumer that compares
// it; in practice nobody does, but it's the safest shape.
export const appDb = new Proxy({} as ReturnType<typeof drizzle<typeof schema>>, {
  get(_target, prop) {
    const db = getDb() as unknown as Record<string | symbol, unknown>;
    const value = db[prop];
    return typeof value === 'function' ? (value as (...args: unknown[]) => unknown).bind(db) : value;
  },
});
export type AppDb = ReturnType<typeof drizzle<typeof schema>>;
