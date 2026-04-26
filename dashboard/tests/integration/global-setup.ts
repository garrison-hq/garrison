import { rmSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { bootHarness } from './_harness';

// Playwright globalSetup. Boots the postgres testcontainer +
// applies migrations once before any test runs; the resulting
// DSN/secret env is written to tests/integration/.harness/env.json
// so the webServer block in playwright.config.ts can pick it up.

const ENV_FILE = join(import.meta.dirname, '.harness', 'env.json');

export default async function globalSetup() {
  // Stale env file from a previous run points at a dead testcontainer
  // (Ryuk killed it when the prior process exited). Nuke it so
  // bootHarness starts fresh and writes a new one.
  if (existsSync(ENV_FILE)) {
    rmSync(ENV_FILE);
  }
  const env = await bootHarness();
  // Mirror env into process.env so playwright.config.ts's webServer
  // sees the values when it spawns `bun run dev`.
  process.env.DASHBOARD_APP_DSN = env.DASHBOARD_APP_DSN;
  process.env.DASHBOARD_RO_DSN = env.DASHBOARD_RO_DSN;
  process.env.BETTER_AUTH_SECRET = env.BETTER_AUTH_SECRET;
  process.env.BETTER_AUTH_URL = env.BETTER_AUTH_URL;
  process.env.PORT = env.PORT;
}
