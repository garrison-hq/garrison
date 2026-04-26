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

export async function setup() {
  await bootDb();
}

export async function teardown() {
  await stopHarness();
}
