// Shared integration-test harness. Boots a Postgres testcontainer,
// applies all goose + Drizzle migrations, sets role passwords +
// LOGIN, and exposes the resulting DSNs to the Playwright tests.
//
// One container per test invocation; tests TRUNCATE the dashboard
// tables between specs via t.beforeEach (see truncateDashboardState
// below). The dashboard server itself is launched by Playwright's
// webServer config in playwright.config.ts using the env this
// harness writes to a temp file.

import { ChildProcess, spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { GenericContainer, StartedTestContainer, Wait } from 'testcontainers';

const ROOT = resolve(import.meta.dirname, '..', '..', '..');
const MIGRATIONS_DIR = join(ROOT, 'migrations');
const DASHBOARD_DIR = resolve(import.meta.dirname, '..', '..');

let started: StartedTestContainer | null = null;
let dashboardProc: ChildProcess | null = null;

export interface HarnessEnv {
  DASHBOARD_APP_DSN: string;
  DASHBOARD_RO_DSN: string;
  BETTER_AUTH_SECRET: string;
  BETTER_AUTH_URL: string;
  PORT: string;
  /**
   * Superuser DSN reserved for the test harness's own teardown
   * operations (TRUNCATE between specs). The dashboard runtime
   * should NEVER use this — it authenticates as the app role only.
   */
  TEST_SUPERUSER_DSN: string;
}

/**
 * Boot the Postgres testcontainer + apply goose + Drizzle migrations
 * + set role passwords. Returns the env vars the dashboard process
 * (and the unit tests that talk to the DB directly) need.
 *
 * Idempotent across same-process re-imports via the `started`
 * module-level handle, and across test-worker forks via the env
 * file written to tests/integration/.harness/env.json.
 */
export async function bootDb(): Promise<HarnessEnv> {
  // Test workers re-import this module in their own process;
  // globalSetup's container handle is not visible to them. If the
  // env file already exists, trust it — the container is alive
  // (testcontainers + Ryuk pin its lifetime to the parent process).
  if (started) {
    return readEnv();
  }
  if (existsSync(envFilePath())) {
    return readEnv();
  }

  const container = await new GenericContainer('postgres:17')
    .withEnvironment({
      POSTGRES_USER: 'test',
      POSTGRES_PASSWORD: 'test',
      POSTGRES_DB: 'testdb',
    })
    .withExposedPorts(5432)
    .withWaitStrategy(Wait.forLogMessage(/database system is ready to accept connections/, 2))
    .start();
  started = container;

  const host = container.getHost();
  const port = container.getMappedPort(5432);
  const superUserDsn = `postgres://test:test@${host}:${port}/testdb?sslmode=disable`;
  const gooseDsn = `host=${host} port=${port} user=test password=test dbname=testdb sslmode=disable`;

  // Apply goose migrations (the host's `goose` binary).
  const gooseResult = spawnSync('goose', ['-dir', MIGRATIONS_DIR, 'postgres', gooseDsn, 'up'], {
    stdio: 'inherit',
  });
  if (gooseResult.status !== 0) {
    throw new Error('goose up failed');
  }

  // Apply Drizzle migrations.
  const drizzleResult = spawnSync('bunx', ['drizzle-kit', 'migrate'], {
    stdio: 'inherit',
    cwd: DASHBOARD_DIR,
    env: { ...process.env, DASHBOARD_APP_DSN: superUserDsn },
  });
  if (drizzleResult.status !== 0) {
    throw new Error('drizzle migrate failed');
  }

  // Promote both M3 roles to LOGIN with passwords mirroring the
  // ops-checklist M3 procedure.
  await container.exec([
    'psql',
    '-U',
    'test',
    '-d',
    'testdb',
    '-c',
    `ALTER ROLE garrison_dashboard_app WITH LOGIN PASSWORD 'apppass';
     ALTER ROLE garrison_dashboard_ro  WITH LOGIN PASSWORD 'ropass';`,
  ]);

  const env: HarnessEnv = {
    DASHBOARD_APP_DSN: `postgres://garrison_dashboard_app:apppass@${host}:${port}/testdb?sslmode=disable`,
    DASHBOARD_RO_DSN: `postgres://garrison_dashboard_ro:ropass@${host}:${port}/testdb?sslmode=disable`,
    BETTER_AUTH_SECRET: 'integration_test_secret_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx',
    BETTER_AUTH_URL: 'http://localhost:3010',
    PORT: '3010',
    TEST_SUPERUSER_DSN: superUserDsn,
  };
  writeEnvFile(env);
  return env;
}

/**
 * Full Playwright-flavored boot: Postgres + migrations + dashboard.
 * Vitest unit tests use bootDb only.
 */
export async function bootHarness(): Promise<HarnessEnv> {
  const env = await bootDb();
  await startDashboard(env);
  return env;
}

async function startDashboard(env: HarnessEnv): Promise<void> {
  if (dashboardProc) return;

  // Cross-process idempotency: if the dashboard is already
  // listening on the configured port (started by globalSetup, which
  // ran in a different process from this test worker), reuse it.
  try {
    const probe = await fetch(`${env.BETTER_AUTH_URL}/login`, { redirect: 'manual' });
    if (probe.status < 500) {
      return;
    }
  } catch {
    // not running yet — fall through and start one
  }

  // Make sure the test-mode `zz` catalog exists before the build:
  // GARRISON_TEST_MODE=1 expands the locale list to ['en', 'zz']
  // (lib/i18n/config.ts) and next-intl's loader fails the build if
  // messages/zz.json is missing. The fixture under
  // tests/fixtures/i18n/zz.json is the canonical source.
  const fixture = join(DASHBOARD_DIR, 'tests', 'fixtures', 'i18n', 'zz.json');
  const target = join(DASHBOARD_DIR, 'messages', 'zz.json');
  if (existsSync(fixture) && !existsSync(target)) {
    const { copyFileSync } = await import('node:fs');
    copyFileSync(fixture, target);
  }

  // Build once if .next/standalone isn't already present. We use
  // `next start` rather than `next dev` for tests because Turbopack's
  // dev-mode root detection has been flaky on this layout (see M3
  // retro notes for T006). `next start` runs the prebuilt server and
  // is closer to production behavior anyway.
  //
  // GARRISON_TEST_MODE=1 is set here AND in the runtime spawn below
  // because the locale list in lib/i18n/config.ts is read at both
  // build time (next-intl bakes the catalog list into the bundle)
  // and request time.
  if (!existsSync(join(DASHBOARD_DIR, '.next', 'BUILD_ID'))) {
    const buildResult = spawnSync('bun', ['run', 'build'], {
      cwd: DASHBOARD_DIR,
      env: { ...process.env, ...env, GARRISON_TEST_MODE: '1' },
      stdio: 'inherit',
    });
    if (buildResult.status !== 0) {
      throw new Error('next build failed (T006 harness pre-build)');
    }
  }

  const proc = spawn('bun', ['run', 'start'], {
    cwd: DASHBOARD_DIR,
    env: { ...process.env, ...env, GARRISON_TEST_MODE: '1' },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  dashboardProc = proc;
  proc.stdout?.on('data', (chunk) => process.stdout.write(`[dashboard] ${chunk}`));
  proc.stderr?.on('data', (chunk) => process.stderr.write(`[dashboard] ${chunk}`));

  // Poll the port until it answers HTTP, with a 90s ceiling.
  const deadline = Date.now() + 90_000;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${env.BETTER_AUTH_URL}/login`, { redirect: 'manual' });
      if (res.status < 500) return;
    } catch {
      // not ready yet
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(`dashboard did not become ready within 90s on ${env.BETTER_AUTH_URL}`);
}

export async function stopHarness(): Promise<void> {
  if (dashboardProc) {
    dashboardProc.kill('SIGTERM');
    // Give next a moment to shut down child workers.
    await new Promise((r) => setTimeout(r, 1500));
    if (!dashboardProc.killed) dashboardProc.kill('SIGKILL');
    dashboardProc = null;
  }
  if (started) {
    await started.stop();
    started = null;
  }
}

function envFilePath(): string {
  const dir = join(DASHBOARD_DIR, 'tests', 'integration', '.harness');
  if (!existsSync(dir)) mkdirSync(dir, { recursive: true });
  return join(dir, 'env.json');
}

function writeEnvFile(env: HarnessEnv): void {
  writeFileSync(envFilePath(), JSON.stringify(env, null, 2));
}

function readEnv(): HarnessEnv {
  const path = envFilePath();
  if (!existsSync(path)) {
    throw new Error(`harness env file not found at ${path}`);
  }
  return JSON.parse(readFileSync(path, 'utf-8')) as HarnessEnv;
}

export async function truncateDashboardState(env: HarnessEnv): Promise<void> {
  // Connect as the test superuser — TRUNCATE is not in the
  // app role's grant set, and we don't want test infrastructure to
  // weaken the production grant matrix.
  const sql = (await import('postgres')).default;
  const client = sql(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    await client`TRUNCATE users, sessions, accounts, verifications, operator_invites RESTART IDENTITY CASCADE`;
  } finally {
    await client.end();
  }
}
