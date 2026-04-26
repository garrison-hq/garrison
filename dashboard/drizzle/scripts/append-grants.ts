#!/usr/bin/env bun
//
// Post-generate hook for `bun run drizzle:generate`. Drizzle-kit emits
// raw CREATE TABLE / ALTER TABLE statements; this script appends the
// GRANT block that exposes the dashboard-owned tables to the
// `garrison_dashboard_app` role created by T001 (the cross-boundary
// goose migration).
//
// Without this hook, freshly Drizzle-generated migrations would land
// the tables but the dashboard's runtime DSN (which authenticates as
// garrison_dashboard_app) would fail every query with permission-
// denied. The grant must be applied at the same migration step the
// CREATE TABLE happens or the next deploy is broken.
//
// Idempotency: each grant is `GRANT ... ON <table> TO ...`, which
// Postgres treats idempotently per (grantee, object, privilege).
// The marker comment makes re-runs of the same hook a no-op.

import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const MIGRATIONS_DIR = join(import.meta.dirname, '..', 'migrations');

const DASHBOARD_OWNED_TABLES = [
  'users',
  'sessions',
  'accounts',
  'verifications',
  'operator_invites',
] as const;

const MARKER = '-- +grant-block: garrison_dashboard_app';

const grantBlock = `
${MARKER}
-- Appended by drizzle/scripts/append-grants.ts after every
-- \`bun run drizzle:generate\`. Without this block, the
-- garrison_dashboard_app role (which the dashboard runtime
-- authenticates as) cannot read or write its own tables.
GRANT SELECT, INSERT, UPDATE, DELETE ON ${DASHBOARD_OWNED_TABLES.join(', ')}
  TO garrison_dashboard_app;
`;

function newestMigrationFile(): string | null {
  const files = readdirSync(MIGRATIONS_DIR)
    .filter((f) => f.endsWith('.sql'))
    .sort();
  return files.length > 0 ? files[files.length - 1] : null;
}

function appendGrants(file: string): void {
  const path = join(MIGRATIONS_DIR, file);
  const content = readFileSync(path, 'utf-8');
  if (content.includes(MARKER)) {
    console.log(`[append-grants] ${file}: already has grant block, skipping`);
    return;
  }
  writeFileSync(path, content.trimEnd() + '\n' + grantBlock);
  console.log(`[append-grants] ${file}: grant block appended`);
}

const newest = newestMigrationFile();
if (!newest) {
  console.log('[append-grants] no .sql migration file found, nothing to do');
  process.exit(0);
}
appendGrants(newest);
