import { defineConfig, devices } from '@playwright/test';

// Playwright integration tests boot a Postgres testcontainer (via
// tests/integration/global-setup.ts), apply goose + Drizzle
// migrations, set role passwords + LOGIN, then start the dashboard
// dev server (via the webServer block below) against the resulting
// DSNs.
//
// T017's responsive checks expect 768/1024/1280 viewports as
// separate projects — defined here so later tasks only have to add
// specs.

export default defineConfig({
  testDir: './tests/integration',
  testMatch: '**/*.spec.ts',
  timeout: 60_000,
  expect: {
    timeout: 5_000,
  },
  reporter: process.env.CI ? 'github' : 'list',
  fullyParallel: false,
  workers: 1,
  globalSetup: './tests/integration/global-setup.ts',
  globalTeardown: './tests/integration/global-teardown.ts',
  use: {
    baseURL: 'http://localhost:3010',
    trace: 'retain-on-failure',
  },
  // Note: no webServer block. The dashboard process is started
  // inside globalSetup (tests/integration/_harness.ts ::
  // startDashboard) so it inherits the harness's DSN env directly,
  // bypassing Playwright's config-load-time env capture.
  projects: [
    {
      name: 'chromium-1280',
      use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } },
    },
    {
      name: 'chromium-1024',
      use: { ...devices['Desktop Chrome'], viewport: { width: 1024, height: 768 } },
    },
    {
      name: 'chromium-768',
      use: { ...devices['Desktop Chrome'], viewport: { width: 768, height: 1024 } },
    },
  ],
});
