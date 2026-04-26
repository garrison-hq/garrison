import { defineConfig, devices } from '@playwright/test';

// Playwright integration tests boot a Postgres testcontainer + apply
// goose + Drizzle migrations + start the built dashboard, then drive
// the real surfaces. Tests live under tests/integration/.
//
// T017's responsive checks expect 768/1024/1280 viewports as separate
// projects. T002 ships a baseline 1280 project and pre-defines the
// other two so later tasks only have to add specs.
export default defineConfig({
  testDir: './tests/integration',
  timeout: 60_000,
  expect: {
    timeout: 5_000,
  },
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: process.env.DASHBOARD_BASE_URL ?? 'http://localhost:3000',
    trace: 'retain-on-failure',
  },
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
