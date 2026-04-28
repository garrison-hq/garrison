import { rmSync, existsSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { bootHarness } from './_harness';

// Playwright globalSetup. Boots the postgres testcontainer +
// applies migrations once before any test runs; the resulting
// DSN/secret env is written to tests/integration/.harness/env.json
// so the webServer block in playwright.config.ts can pick it up.

const ENV_FILE = join(import.meta.dirname, '.harness', 'env.json');
// Browser-coverage cache (Path B) + server-coverage cache (Path A).
// All four directories are siblings (not nested) so each monocart
// CoverageReport's generate() can't wipe siblings while emitting
// its own lcov output.
const COVERAGE_DIR = resolve(import.meta.dirname, '..', '..', 'coverage', 'integration');
const RAW_DIR = resolve(import.meta.dirname, '..', '..', 'coverage', 'integration-raw');
const SERVER_COVERAGE_DIR = resolve(import.meta.dirname, '..', '..', 'coverage', 'integration-server');
const SERVER_REPORT_DIR = resolve(import.meta.dirname, '..', '..', 'coverage', 'integration-server-report');

export default async function globalSetup() {
  // Stale env file from a previous run points at a dead testcontainer
  // (Ryuk killed it when the prior process exited). Nuke it so
  // bootHarness starts fresh and writes a new one.
  if (existsSync(ENV_FILE)) {
    rmSync(ENV_FILE);
  }
  // Clear out previous coverage dumps. The browser cache lives
  // at coverage/integration/.raw/.cache; the server cache lives
  // at coverage/integration/.server-cache; both get re-created
  // by the harness + per-spec fixture as data flows in.
  for (const d of [COVERAGE_DIR, RAW_DIR, SERVER_COVERAGE_DIR, SERVER_REPORT_DIR]) {
    if (existsSync(d)) {
      rmSync(d, { recursive: true, force: true });
    }
  }
  const env = await bootHarness();
  // Mirror env into process.env so playwright.config.ts's webServer
  // sees the values when it spawns `bun run dev`.
  process.env.DASHBOARD_APP_DSN = env.DASHBOARD_APP_DSN;
  process.env.DASHBOARD_RO_DSN = env.DASHBOARD_RO_DSN;
  process.env.BETTER_AUTH_SECRET = env.BETTER_AUTH_SECRET;
  process.env.BETTER_AUTH_URL = env.BETTER_AUTH_URL;
  process.env.PORT = env.PORT;
  process.env.INFISICAL_DASHBOARD_ML_CLIENT_ID = env.INFISICAL_DASHBOARD_ML_CLIENT_ID;
  process.env.INFISICAL_DASHBOARD_ML_CLIENT_SECRET = env.INFISICAL_DASHBOARD_ML_CLIENT_SECRET;
  process.env.INFISICAL_DASHBOARD_PROJECT_ID = env.INFISICAL_DASHBOARD_PROJECT_ID;
  process.env.INFISICAL_DASHBOARD_ENVIRONMENT = env.INFISICAL_DASHBOARD_ENVIRONMENT;
  process.env.INFISICAL_SITE_URL = env.INFISICAL_SITE_URL;
}
