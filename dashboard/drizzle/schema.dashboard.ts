// ─── dashboard-owned schema — hand-written, edit here ───
//
// These five tables are owned by the dashboard and managed by Drizzle
// migrations (FR-020). The goose-managed cross-boundary migration
// (T001) creates the role + grants on supervisor-owned tables; the
// generated Drizzle SQL appends GRANTs on these dashboard-owned
// tables for garrison_dashboard_app via the post-generate hook
// (drizzle/scripts/append-grants.ts).
//
// Schema mirrors plan.md §"Data model" verbatim. The four
// better-auth core tables (users/sessions/accounts/verifications)
// follow better-auth's expected shapes; T005 wires better-auth
// against them via the postgres-js adapter.
//
// Why a separate file: drizzle-kit pull regenerates schema.supervisor.ts
// on every introspection run; isolating the dashboard-owned tables here
// prevents accidental loss when the supervisor side gains a new table.
// schema.ts re-exports from both files.

import {
  pgTable,
  uuid,
  text,
  boolean,
  timestamp,
  unique,
  index,
  check,
} from 'drizzle-orm/pg-core';
import { sql } from 'drizzle-orm';

export const users = pgTable(
  'users',
  {
    id: uuid().defaultRandom().primaryKey().notNull(),
    email: text().notNull(),
    emailVerified: boolean('email_verified').notNull().default(false),
    name: text(),
    image: text(),
    createdAt: timestamp('created_at', { withTimezone: true, mode: 'date' })
      .defaultNow()
      .notNull(),
    updatedAt: timestamp('updated_at', { withTimezone: true, mode: 'date' })
      .defaultNow()
      .notNull(),
    // Per-operator theme persistence (FR-010a). 'system' means the
    // browser's prefers-color-scheme decides; 'dark'/'light' are
    // explicit operator overrides.
    themePreference: text('theme_preference').notNull().default('system'),
  },
  (t) => [
    unique('users_email_unique').on(t.email),
    check(
      'users_theme_preference_valid',
      sql`${t.themePreference} IN ('dark', 'light', 'system')`,
    ),
  ],
);

export const sessions = pgTable(
  'sessions',
  {
    id: uuid().defaultRandom().primaryKey().notNull(),
    userId: uuid('user_id')
      .notNull()
      .references(() => users.id, { onDelete: 'cascade' }),
    expiresAt: timestamp('expires_at', { withTimezone: true, mode: 'date' }).notNull(),
    token: text().notNull(),
    createdAt: timestamp('created_at', { withTimezone: true, mode: 'date' })
      .defaultNow()
      .notNull(),
    // better-auth touches updated_at on every session refresh; the
    // .$onUpdate hook in better-auth's adapter writes the new value,
    // so the column is NOT NULL with a defaultNow seed.
    updatedAt: timestamp('updated_at', { withTimezone: true, mode: 'date' })
      .defaultNow()
      .notNull(),
    // text rather than inet: better-auth passes an empty string when
    // it cannot parse a request IP, and inet rejects '' with
    // "invalid input syntax". text matches better-auth's reference
    // schema and is forgiving of empty/invalid values.
    ipAddress: text('ip_address'),
    userAgent: text('user_agent'),
  },
  (t) => [
    unique('sessions_token_unique').on(t.token),
    index('sessions_user_id_idx').on(t.userId),
  ],
);

export const accounts = pgTable('accounts', {
  id: uuid().defaultRandom().primaryKey().notNull(),
  userId: uuid('user_id')
    .notNull()
    .references(() => users.id, { onDelete: 'cascade' }),
  accountId: text('account_id').notNull(),
  providerId: text('provider_id').notNull(),
  // Hashed; null when provider != 'email'.
  password: text(),
  accessToken: text('access_token'),
  refreshToken: text('refresh_token'),
  idToken: text('id_token'),
  accessTokenExpiresAt: timestamp('access_token_expires_at', {
    withTimezone: true,
    mode: 'date',
  }),
  refreshTokenExpiresAt: timestamp('refresh_token_expires_at', {
    withTimezone: true,
    mode: 'date',
  }),
  scope: text(),
  createdAt: timestamp('created_at', { withTimezone: true, mode: 'date' })
    .defaultNow()
    .notNull(),
  updatedAt: timestamp('updated_at', { withTimezone: true, mode: 'date' })
    .defaultNow()
    .notNull(),
}, (t) => [index('accounts_user_id_idx').on(t.userId)]);

export const verifications = pgTable(
  'verifications',
  {
    id: uuid().defaultRandom().primaryKey().notNull(),
    identifier: text().notNull(),
    value: text().notNull(),
    expiresAt: timestamp('expires_at', { withTimezone: true, mode: 'date' }).notNull(),
    createdAt: timestamp('created_at', { withTimezone: true, mode: 'date' })
      .defaultNow()
      .notNull(),
    updatedAt: timestamp('updated_at', { withTimezone: true, mode: 'date' })
      .defaultNow()
      .notNull(),
  },
  (t) => [index('verifications_identifier_idx').on(t.identifier)],
);

export const operatorInvites = pgTable(
  'operator_invites',
  {
    id: uuid().defaultRandom().primaryKey().notNull(),
    token: text().notNull(),
    // Nullable + ON DELETE SET NULL per spec edge case "inviter
    // account deletion before redemption keeps invite valid until
    // expiry or revocation by another operator." A NOT NULL or
    // ON DELETE RESTRICT FK would block the SQL delete entirely
    // and the spec edge case requires deletion to succeed.
    createdByUserId: uuid('created_by_user_id').references(() => users.id, {
      onDelete: 'set null',
    }),
    createdAt: timestamp('created_at', { withTimezone: true, mode: 'date' })
      .defaultNow()
      .notNull(),
    expiresAt: timestamp('expires_at', { withTimezone: true, mode: 'date' }).notNull(),
    revokedAt: timestamp('revoked_at', { withTimezone: true, mode: 'date' }),
    redeemedAt: timestamp('redeemed_at', { withTimezone: true, mode: 'date' }),
    redeemedByUserId: uuid('redeemed_by_user_id').references(() => users.id, {
      onDelete: 'set null',
    }),
  },
  (t) => [
    unique('operator_invites_token_unique').on(t.token),
    // Lookups by token are the hot path (every redemption attempt).
    index('operator_invites_token_idx').on(t.token),
    // Pending-invites view: indexes only invites that are neither
    // revoked nor redeemed; bounded by the operator workload size.
    index('operator_invites_pending_idx')
      .on(t.expiresAt)
      .where(sql`revoked_at IS NULL AND redeemed_at IS NULL`),
    // No row may be both revoked and redeemed simultaneously; the
    // atomic UPDATE in lib/auth/invites.ts (T007) prevents this in
    // app code, but the constraint is the second line of defence.
    check(
      'operator_invites_exactly_one_terminal_state',
      sql`NOT (${t.revokedAt} IS NOT NULL AND ${t.redeemedAt} IS NOT NULL)`,
    ),
  ],
);
