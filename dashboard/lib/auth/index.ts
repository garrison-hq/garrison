// better-auth instance for the Garrison dashboard.
//
// Persistence runs through the dashboard's `appDb` Drizzle client
// (DASHBOARD_APP_DSN → garrison_dashboard_app role; T001 created the
// role and granted it CRUD on the dashboard-owned tables; T004
// landed the table schema). better-auth's drizzle adapter wraps the
// same client so login, sign-up, and session writes share one
// connection pool with the rest of the dashboard.
//
// `usePlural: true` because the dashboard's tables are pluralised
// (users / sessions / accounts / verifications / operator_invites)
// to align with the M2-arc schema vocabulary; better-auth's default
// singular naming would otherwise miss them.
//
// `additionalFields.user.theme_preference` makes the column visible
// in session reads (T009 reads it server-side to pick the
// data-theme attribute on <html>).
//
// Email/password is the only enabled provider in M3 (per spec
// A-008 — no SSO/OAuth/MFA).

import { betterAuth } from 'better-auth';
import { drizzleAdapter } from 'better-auth/adapters/drizzle';
import { appDb } from '@/lib/db/appClient';
import * as dashboardSchema from '@/drizzle/schema.dashboard';

// Build-time tolerant: if BETTER_AUTH_SECRET isn't set during the
// Docker build's static prerender, fall back to a deterministic
// placeholder. Production deploys MUST set this env var (the
// startup health check in app/api/auth/[...all]/route.ts logs a
// warning when the placeholder is detected).
const PLACEHOLDER_SECRET = '__BETTER_AUTH_SECRET_NOT_SET_AT_BUILD_TIME__';
const secret = process.env.BETTER_AUTH_SECRET ?? PLACEHOLDER_SECRET;
if (
  process.env.NODE_ENV === 'production' &&
  process.env.GARRISON_TEST_MODE !== '1' &&
  secret === PLACEHOLDER_SECRET &&
  globalThis.process !== undefined &&
  process.env.NEXT_PHASE !== 'phase-production-build'
) {
  throw new Error(
    'BETTER_AUTH_SECRET is unset at runtime. Generate one with `openssl rand -hex 32` ' +
      'and persist it via your secret store (see ops-checklist M3 section, T020).',
  );
}

export const auth = betterAuth({
  secret,
  baseURL: process.env.BETTER_AUTH_URL,
  // Disable trusted-origin checking in test mode: integration tests
  // call /api/auth and /api/invites/redeem from a Node fetch without
  // an Origin header, which better-auth otherwise treats as a CSRF
  // signal and rejects with 403. Production deploys keep the default.
  trustedOrigins: process.env.GARRISON_TEST_MODE === '1' ? ['http://localhost:3010'] : undefined,
  // Disable rate limiting in test mode. Better-auth's default
  // bucket is small enough that the test suite (multiple sign-up
  // attempts seeded between specs) trips it within seconds.
  rateLimit: process.env.GARRISON_TEST_MODE === '1' ? { enabled: false } : undefined,
  // The dashboard tables use `id uuid DEFAULT gen_random_uuid()` per
  // plan §"data-model.md"; let Postgres generate IDs rather than have
  // better-auth pass nanoid-shaped strings into a uuid column.
  advanced: {
    database: {
      generateId: false,
    },
  },
  database: drizzleAdapter(appDb, {
    provider: 'pg',
    schema: {
      users: dashboardSchema.users,
      sessions: dashboardSchema.sessions,
      accounts: dashboardSchema.accounts,
      verifications: dashboardSchema.verifications,
    },
    usePlural: true,
  }),
  emailAndPassword: {
    enabled: true,
  },
  user: {
    additionalFields: {
      // Per-operator theme persistence (FR-010a). Surfaced on every
      // session read so T009's server-component shell can render the
      // saved preference without a second DB round-trip.
      //
      // Key is the TS field name (themePreference) on the Drizzle
      // schema, which the drizzle adapter uses for column lookup.
      // The underlying DB column is `theme_preference` per FR-010a /
      // plan §"data-model.md" (Drizzle handles the casing translation).
      themePreference: {
        type: 'string',
        required: false,
        defaultValue: 'system',
        input: false,
      },
    },
  },
});

export type Auth = typeof auth;
