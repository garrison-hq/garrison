import { defineConfig } from 'vitest/config';

// Vitest config for unit tests colocated with the code they exercise
// (lib/**/*.test.ts, components/**/*.test.tsx).
//
// Playwright integration tests live under tests/integration/ and are
// excluded — they run via `bunx playwright test`.
export default defineConfig({
  test: {
    environment: 'node',
    include: [
      'lib/**/*.{test,spec}.{ts,tsx}',
      'components/**/*.{test,spec}.{ts,tsx}',
      'app/**/*.{test,spec}.{ts,tsx}',
    ],
    exclude: [
      'node_modules/**',
      '.next/**',
      'tests/integration/**',
    ],
    // Boot the Postgres testcontainer + apply migrations once for
    // the whole vitest run so unit tests that require a real DB
    // (lib/auth/invites.test.ts) can read the harness env file.
    globalSetup: ['./tests/integration/vitest-global-setup.ts'],
    // 60s default timeout — the container boot + migrations take
    // ~5–10s and the race-test runs two sign-ups concurrently.
    testTimeout: 60_000,
    hookTimeout: 90_000,
    // Tests share one DB — run serially to avoid TRUNCATE races
    // between specs.
    fileParallelism: false,
  },
  resolve: {
    alias: {
      '@': new URL('.', import.meta.url).pathname.replace(/\/$/, ''),
    },
  },
});
