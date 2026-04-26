import { rmSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { bootDb, stopHarness } from './_harness';

// Vitest globalSetup hook. Boots the Postgres testcontainer +
// applies migrations so unit tests under lib/**/*.test.ts that
// require a real database (lib/auth/invites.test.ts) can read the
// harness's env file. Reused across test files in the same vitest
// run; the container shuts down when teardown fires.
//
// Unit tests do not need the dashboard server itself — that's
// Playwright's concern. We use bootDb (DB-only) rather than
// bootHarness (DB + dashboard).

const HARNESS_DIR = join(import.meta.dirname);
const ENV_FILE = join(HARNESS_DIR, '.harness', 'env.json');

export async function setup() {
  // Stale env file from a previous run points at a dead testcontainer
  // (Ryuk killed it when the prior vitest process exited). Nuke it
  // so bootDb starts fresh and writes a new one.
  if (existsSync(ENV_FILE)) {
    rmSync(ENV_FILE);
  }
  await bootDb();
}

export async function teardown() {
  await stopHarness();
}
