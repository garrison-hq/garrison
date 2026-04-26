#!/usr/bin/env bun
//
// pull-supervisor-schema — boots an ephemeral postgres:17 container,
// applies every goose migration from ../migrations/*.sql via the
// host's `goose` binary, then runs `drizzle-kit pull` against it to
// regenerate the introspected section of drizzle/schema.ts.
//
// Run when supervisor-owned migrations land or change. The script
// keeps the supervisor-owned tables out of drizzle-kit's diff path:
// `drizzle.config.ts` allow-lists only the dashboard-owned tables for
// generate, and the introspected section ships under a "do not edit"
// header in schema.ts.
//
// Hard requirements on the host:
//   - docker (the M2.2 deployment topology already requires it)
//   - goose CLI (the supervisor toolchain; same binary used by ops)
//
// Idempotent — running twice produces the same schema.ts.

import { spawnSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

const REPO_ROOT = resolve(import.meta.dirname, '..', '..');
const MIGRATIONS_DIR = join(REPO_ROOT, 'migrations');
const CONTAINER_NAME = `garrison-pull-supervisor-${Date.now()}`;
const PG_PORT = 25433;
const DSN = `postgres://test:test@localhost:${PG_PORT}/testdb?sslmode=disable`;
const GOOSE_DSN = `host=localhost port=${PG_PORT} user=test password=test dbname=testdb sslmode=disable`;

function run(cmd: string, args: string[], opts: { env?: Record<string, string> } = {}): void {
  const result = spawnSync(cmd, args, {
    stdio: 'inherit',
    env: { ...process.env, ...(opts.env ?? {}) },
  });
  if (result.status !== 0) {
    throw new Error(`${cmd} ${args.join(' ')} failed with status ${result.status}`);
  }
}

function bootPostgres(): void {
  console.log('[1/4] booting ephemeral postgres:17 container...');
  run('docker', [
    'run', '-d',
    '--name', CONTAINER_NAME,
    '-e', 'POSTGRES_PASSWORD=test',
    '-e', 'POSTGRES_USER=test',
    '-e', 'POSTGRES_DB=testdb',
    '-p', `${PG_PORT}:5432`,
    'postgres:17',
  ]);

  // Wait for pg_isready (up to 30s).
  for (let i = 0; i < 30; i++) {
    const probe = spawnSync('docker', ['exec', CONTAINER_NAME, 'pg_isready', '-U', 'test'], {
      stdio: 'ignore',
    });
    if (probe.status === 0) return;
    spawnSync('sleep', ['1']);
  }
  throw new Error('postgres did not become ready in 30s');
}

function applyMigrations(): void {
  console.log('[2/4] applying goose migrations...');
  run('goose', ['-dir', MIGRATIONS_DIR, 'postgres', GOOSE_DSN, 'up']);
}

function pullSchema(): void {
  console.log('[3/4] running drizzle-kit pull (introspect-everything config)...');
  run('bunx', ['drizzle-kit', 'pull', '--config', 'drizzle.pull.config.ts'], {
    env: { DASHBOARD_APP_DSN: DSN },
  });
  // Compose the canonical schema.ts:
  //  - introspected section copied from drizzle/_introspected/schema.ts under
  //    a "do not edit" header
  //  - dashboard-owned section seeded with a placeholder header (T004 fills
  //    it in)
  composeSchema();
}

function composeSchema(): void {
  const introspectedPath = join(import.meta.dirname, '..', 'drizzle', '_introspected', 'schema.ts');
  const introspected = readFileSync(introspectedPath, 'utf-8');
  // Write the introspected output to schema.supervisor.ts only.
  // schema.dashboard.ts is hand-written and untouched here, and
  // schema.ts re-exports from both. This avoids the "pull clobbers
  // dashboard-owned tables" failure mode plan §"Phase 0 research
  // item 4" predicted as the fallback.
  const composed =
    `// ─── generated via drizzle-kit pull — do not edit ───\n` +
    `// Run \`bun run drizzle:pull\` to regenerate.\n` +
    `// Source: goose-managed migrations under ../../migrations/.\n\n` +
    introspected;
  const targetPath = join(import.meta.dirname, '..', 'drizzle', 'schema.supervisor.ts');
  writeFileSync(targetPath, composed);
}

function teardown(): void {
  console.log('[4/4] tearing down container...');
  spawnSync('docker', ['rm', '-f', CONTAINER_NAME], { stdio: 'inherit' });
}

async function main() {
  try {
    bootPostgres();
    applyMigrations();
    pullSchema();
    console.log('done.  drizzle/schema.ts regenerated.');
  } finally {
    teardown();
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
