import type { Config } from 'drizzle-kit';

// Pull-only Drizzle config used by scripts/pull-supervisor-schema.ts.
//
// Why a separate config: drizzle-kit pull and drizzle-kit generate share
// the `tablesFilter` allow-list. The main config (drizzle.config.ts)
// allow-lists only the dashboard-owned tables so generate never targets
// supervisor-owned ones. Pull, by contrast, needs to see ALL the tables
// goose has applied so it can introspect them into schema.ts.
//
// This config writes to a scratch path; the script copies the relevant
// section back into drizzle/schema.ts under the introspection header.
export default {
  schema: './drizzle/_introspected/schema.ts',
  out: './drizzle/_introspected',
  dialect: 'postgresql',
  dbCredentials: {
    url: process.env.DASHBOARD_APP_DSN ?? '',
  },
  // No tablesFilter: introspect everything goose has applied.
} satisfies Config;
