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
import { InfisicalTestHarness } from './_infisical';

const ROOT = resolve(import.meta.dirname, '..', '..', '..');
const MIGRATIONS_DIR = join(ROOT, 'migrations');
const DASHBOARD_DIR = resolve(import.meta.dirname, '..', '..');

let started: StartedTestContainer | null = null;
let infisical: InfisicalTestHarness | null = null;
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
  /**
   * Infisical machine-identity credentials for the dashboard's
   * vault server actions. Set when an Infisical testcontainer is
   * provisioned alongside the Postgres container; absent (empty
   * string) when GARRISON_TEST_NO_INFISICAL=1 is set so legacy
   * specs can still run against the soft-default
   * "configure vault" prompt path.
   */
  INFISICAL_DASHBOARD_ML_CLIENT_ID: string;
  INFISICAL_DASHBOARD_ML_CLIENT_SECRET: string;
  INFISICAL_DASHBOARD_PROJECT_ID: string;
  INFISICAL_DASHBOARD_ENVIRONMENT: string;
  INFISICAL_SITE_URL: string;
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

  // Boot Infisical alongside Postgres (unless explicitly disabled
  // via GARRISON_TEST_NO_INFISICAL=1 for fast unit-only iterations).
  // Mirrors supervisor's testutil.go three-container topology.
  let infisicalCreds = {
    INFISICAL_DASHBOARD_ML_CLIENT_ID: '',
    INFISICAL_DASHBOARD_ML_CLIENT_SECRET: '',
    INFISICAL_DASHBOARD_PROJECT_ID: '',
    INFISICAL_DASHBOARD_ENVIRONMENT: '',
    INFISICAL_SITE_URL: '',
  };
  if (process.env.GARRISON_TEST_NO_INFISICAL !== '1') {
    infisical = await InfisicalTestHarness.start();
    const creds = await infisical.issueCredentials();
    infisicalCreds = {
      INFISICAL_DASHBOARD_ML_CLIENT_ID: creds.clientId,
      INFISICAL_DASHBOARD_ML_CLIENT_SECRET: creds.clientSecret,
      INFISICAL_DASHBOARD_PROJECT_ID: creds.projectId,
      INFISICAL_DASHBOARD_ENVIRONMENT: creds.environment,
      INFISICAL_SITE_URL: creds.siteUrl,
    };
  }

  const env: HarnessEnv = {
    DASHBOARD_APP_DSN: `postgres://garrison_dashboard_app:apppass@${host}:${port}/testdb?sslmode=disable`,
    DASHBOARD_RO_DSN: `postgres://garrison_dashboard_ro:ropass@${host}:${port}/testdb?sslmode=disable`,
    BETTER_AUTH_SECRET: 'integration_test_secret_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxx',
    BETTER_AUTH_URL: 'http://localhost:3010',
    PORT: '3010',
    TEST_SUPERUSER_DSN: superUserDsn,
    ...infisicalCreds,
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

  // M4 / T016 / FR-122: build + run the dashboard via the
  // standalone runtime (node .next/standalone/server.js), not
  // `next start`. M3's harness used `bun run start` (= next
  // start) which printed a warning about output: 'standalone'
  // and diverged from the production Docker image's runtime
  // shape. Switching the test harness to standalone closes
  // the prod-vs-test parity gap M3 retro Q2 flagged.
  //
  // The build must produce .next/standalone/server.js (next
  // emits it when output: 'standalone' is set in next.config.ts —
  // already true since M3). Standalone-runtime requires the
  // .next/static + public directories to live alongside the
  // server.js entrypoint at runtime; the Dockerfile copies them
  // there explicitly. Locally we point STATIC and PUBLIC at the
  // build directory's locations via env so the standalone
  // server can find them.
  //
  // GARRISON_TEST_MODE=1 is set here AND in the runtime spawn below
  // because the locale list in lib/i18n/config.ts is read at both
  // build time (next-intl bakes the catalog list into the bundle)
  // and request time.
  if (!existsSync(join(DASHBOARD_DIR, '.next', 'standalone', 'server.js'))) {
    const buildResult = spawnSync('bun', ['run', 'build'], {
      cwd: DASHBOARD_DIR,
      env: { ...process.env, ...env, GARRISON_TEST_MODE: '1' },
      stdio: 'inherit',
    });
    if (buildResult.status !== 0) {
      throw new Error('next build failed (T016 harness standalone-build)');
    }
  }

  // Wire up .next/static + public next to the standalone server.js
  // so the runtime can resolve them at the expected relative paths.
  // Symlinks keep this idempotent and avoid copying large bundles.
  const standaloneRoot = join(DASHBOARD_DIR, '.next', 'standalone');
  const { symlinkSync, existsSync: exists, mkdirSync: mkdir } = await import('node:fs');
  const standaloneNextDir = join(standaloneRoot, '.next');
  if (!exists(standaloneNextDir)) {
    mkdir(standaloneNextDir, { recursive: true });
  }
  const standaloneStatic = join(standaloneNextDir, 'static');
  if (!exists(standaloneStatic)) {
    symlinkSync(join(DASHBOARD_DIR, '.next', 'static'), standaloneStatic, 'dir');
  }
  const standalonePublic = join(standaloneRoot, 'public');
  if (!exists(standalonePublic)) {
    symlinkSync(join(DASHBOARD_DIR, 'public'), standalonePublic, 'dir');
  }

  // Path A — server-side V8 coverage: NODE_V8_COVERAGE tells
  // Node to dump per-script coverage profiles to a directory on
  // clean shutdown. The c8 post-test step converts them to lcov.
  // Required: serverSourceMaps=true in next.config.ts so c8 can
  // map back to source. The directory lives OUTSIDE
  // coverage/integration/ so monocart's browser-coverage
  // generate() doesn't wipe it.
  const serverCoverageDir = resolve(DASHBOARD_DIR, 'coverage', 'integration-server');
  // The directory is recreated per test invocation via the
  // teardown's pre-clean step; safe to mkdir here even if it
  // exists.
  if (!existsSync(serverCoverageDir)) {
    mkdirSync(serverCoverageDir, { recursive: true });
  }

  // --import a tiny inline ESM module that installs SIGTERM /
  // SIGINT handlers BEFORE server.js loads. Required for Path A
  // coverage: NODE_V8_COVERAGE only flushes profiles on a
  // clean Node exit (process.exit / natural completion); a
  // signal-killed Node leaves the in-memory coverage tables on
  // the floor. The data: URL form sidesteps having to write a
  // wrapper file inside .next/standalone (which would survive
  // bun run build wipes).
  const SIGNAL_HANDLER_DATA_URL =
    'data:text/javascript,' +
    encodeURIComponent(
      'process.on("SIGTERM",()=>process.exit(0));process.on("SIGINT",()=>process.exit(0));',
    );

  const proc = spawn('node', ['--import', SIGNAL_HANDLER_DATA_URL, 'server.js'], {
    cwd: standaloneRoot,
    env: {
      ...process.env,
      ...env,
      GARRISON_TEST_MODE: '1',
      // Force the standalone runtime to listen on 0.0.0.0 so the
      // localhost:3010 probe below can reach it. Next reads
      // process.env.HOSTNAME at server.js startup and binds to it
      // verbatim; on machines where the OS hostname resolves to a
      // public address (some ISPs assign reverse-DNS to the IPv6
      // delegation), that means the server only accepts requests
      // on that public address and localhost loops fail.
      HOSTNAME: '0.0.0.0',
      // Server-side coverage profile dump destination (Path A).
      NODE_V8_COVERAGE: serverCoverageDir,
      // Tell the standalone runtime where to find the static
      // assets and the public dir. They live OUTSIDE the
      // standalone dir during local dev (Next emits them next
      // to .next/standalone, not inside it). The standalone
      // server.js looks at ./public and ./.next/static
      // relative to its cwd; symlinks let us run without
      // copying gigabytes around.
      // (At T019's Docker build time, the Dockerfile
      // explicitly COPYs them into the standalone dir.)
    },
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
    // SIGTERM, then wait for the process to fully exit before
    // moving on. NODE_V8_COVERAGE writes profile files at clean
    // exit time; killing harder (SIGKILL) loses them.
    const proc = dashboardProc;
    dashboardProc = null;
    proc.kill('SIGTERM');
    await new Promise<void>((resolve) => {
      const timer = setTimeout(() => {
        // 10s ceiling — Next typically exits in <2s but we want
        // generous headroom for V8 to flush coverage. If it
        // somehow hangs past 10s, fall back to SIGKILL so the
        // harness can shut down at all.
        proc.kill('SIGKILL');
        resolve();
      }, 10_000);
      proc.on('exit', () => {
        clearTimeout(timer);
        resolve();
      });
    });
  }
  if (started) {
    await started.stop();
    started = null;
  }
  if (infisical) {
    await infisical.stop();
    infisical = null;
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

/**
 * Idempotently create the Infisical folder path under the test
 * workspace's 'dev' environment. Required before dashboard's
 * createSecret because Infisical does not auto-create folders.
 *
 * Specs needing a folder for a freshly-seeded company UUID call
 * this AFTER seedFixtures and BEFORE driving the create-secret
 * form. No-op when GARRISON_TEST_NO_INFISICAL=1.
 *
 * Authenticates via the dashboard's ML credentials (which have
 * admin role on the test project) since spec workers run in
 * separate processes and don't see the harness's in-memory
 * org-token. The siteUrl + ML credentials live in env.json so
 * each worker can re-derive the access token.
 */
export async function ensureInfisicalFolder(
  env: HarnessEnv,
  folderPath: string,
): Promise<void> {
  if (!env.INFISICAL_SITE_URL) return; // GARRISON_TEST_NO_INFISICAL path
  const trimmed = folderPath.replace(/^\/+|\/+$/g, '');
  if (trimmed === '') return;

  // Universal-Auth login → access token.
  const loginRes = await fetch(`${env.INFISICAL_SITE_URL}/api/v1/auth/universal-auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      clientId: env.INFISICAL_DASHBOARD_ML_CLIENT_ID,
      clientSecret: env.INFISICAL_DASHBOARD_ML_CLIENT_SECRET,
    }),
  });
  if (!loginRes.ok) {
    const text = await loginRes.text().catch(() => '');
    throw new Error(`UA login: HTTP ${loginRes.status}: ${text}`);
  }
  const { accessToken } = (await loginRes.json()) as { accessToken: string };

  const segments = trimmed.split('/');
  let parent = '/';
  for (const seg of segments) {
    const body = {
      workspaceId: env.INFISICAL_DASHBOARD_PROJECT_ID,
      environment: env.INFISICAL_DASHBOARD_ENVIRONMENT,
      name: seg,
      path: parent,
    };
    const res = await fetch(`${env.INFISICAL_SITE_URL}/api/v1/folders`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${accessToken}`,
      },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      const lower = text.toLowerCase();
      if (!(res.status === 400 && lower.includes('already exist'))) {
        throw new Error(`ensureFolder ${parent}/${seg}: HTTP ${res.status}: ${text}`);
      }
    }
    parent = parent === '/' ? `/${seg}` : `${parent}/${seg}`;
  }
}

export async function truncateDashboardState(env: HarnessEnv): Promise<void> {
  // Connect as the test superuser — TRUNCATE is not in the
  // app role's grant set, and we don't want test infrastructure to
  // weaken the production grant matrix.
  //
  // Truncates BOTH dashboard-owned tables and M2-arc tables that
  // integration tests seed (departments / tickets / etc). M4
  // additions (vault_access_log, secret_metadata, agent_role_secrets,
  // event_outbox) are also wiped so per-spec assertions over the
  // audit log start from a clean slate. The M2-arc seed rows from
  // migrations re-appear via TRUNCATE only if the migration's seed
  // step is re-run; we leave them gone for the duration of the test
  // (each test re-seeds what it needs).
  const sql = (await import('postgres')).default;
  const client = sql(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    await client`TRUNCATE users, sessions, accounts, verifications, operator_invites,
                          companies, departments, tickets, ticket_transitions,
                          agent_instances, agents,
                          event_outbox, vault_access_log, secret_metadata,
                          agent_role_secrets
                 RESTART IDENTITY CASCADE`;
  } finally {
    await client.end();
  }
}
